//! Chaos driver: emits into the outbox against the gateway in `SANTATI_CHAOS`
//! and prints one `CHAOS_RESULT` line. Run by `mise run chaos rust`
//! (`chaos/run.mjs`); inert unless `SANTATI_CHAOS` is set, so a plain
//! `cargo test` only prints a skip line.

use std::collections::{BTreeMap, HashMap};
use std::sync::atomic::{AtomicBool, AtomicU64, Ordering};
use std::sync::Arc;
use std::time::{Duration, Instant};

use santati::{EventInput, MemoryOutbox, Santati};
use serde_json::{json, Value};

fn ms(duration: Duration) -> f64 {
    (duration.as_secs_f64() * 1000.0 * 1000.0).round() / 1000.0
}

fn percentile(sorted: &[f64], q: f64) -> f64 {
    if sorted.is_empty() {
        return 0.0;
    }
    let index = ((q * sorted.len() as f64).ceil() as usize).saturating_sub(1);
    sorted[index.min(sorted.len() - 1)]
}

fn millis(value: &Value) -> Duration {
    Duration::from_millis(value.as_u64().expect("a non-negative integer"))
}

#[derive(Default)]
struct Emitted {
    latencies: Vec<f64>,
    queued: u64,
    errors: BTreeMap<String, u64>,
}

fn main() {
    let Ok(raw) = std::env::var("SANTATI_CHAOS") else {
        println!("SANTATI_CHAOS not set; skipping");
        return;
    };
    let cfg: Value = serde_json::from_str(&raw).expect("SANTATI_CHAOS is JSON");

    let runtime = tokio::runtime::Builder::new_multi_thread()
        .worker_threads(4)
        .enable_all()
        .build()
        .expect("a tokio runtime");
    let result = runtime.block_on(run(cfg));
    println!("CHAOS_RESULT {result}");
}

async fn run(cfg: Value) -> Value {
    let opts = &cfg["client"];
    let client = Santati::builder("sat_sk_chaos")
        .base_url(opts["base_url"].as_str().expect("base_url"))
        .trail("chaos")
        .timeout(millis(&opts["timeout_ms"]))
        .max_retries(opts["max_retries"].as_u64().expect("max_retries") as u32)
        .backoff(
            millis(&opts["initial_backoff_ms"]),
            millis(&opts["max_backoff_ms"]),
        )
        .batch_size(opts["batch_size"].as_u64().expect("batch_size") as usize)
        .flush_interval(millis(&opts["flush_interval_ms"]))
        .outbox(
            MemoryOutbox::new(opts["max_pending"].as_u64().expect("max_pending") as usize)
                .expect("a memory outbox"),
        )
        .build()
        .expect("a client");

    let heartbeat_every = millis(&cfg["heartbeat_ms"]);
    let emitters = cfg["emitters"].as_u64().expect("emitters");
    let per_emitter = cfg["events_per_emitter"]
        .as_u64()
        .expect("events_per_emitter");
    let emit_interval = millis(&cfg["emit_interval_ms"]);
    let flusher_interval = millis(&cfg["flusher_interval_ms"]);

    // Heartbeat: how late a sleep of `heartbeat_ms` wakes up, in nanoseconds.
    let max_lag_ns = Arc::new(AtomicU64::new(0));
    let stop_heartbeat = Arc::new(AtomicBool::new(false));
    let heartbeat = {
        let (max_lag_ns, stop) = (max_lag_ns.clone(), stop_heartbeat.clone());
        tokio::spawn(async move {
            while !stop.load(Ordering::Relaxed) {
                let start = Instant::now();
                tokio::time::sleep(heartbeat_every).await;
                let lag = start.elapsed().saturating_sub(heartbeat_every);
                max_lag_ns.fetch_max(lag.as_nanos() as u64, Ordering::Relaxed);
            }
        })
    };

    // Flusher.
    let flush_calls = Arc::new(AtomicU64::new(0));
    let stop_flusher = Arc::new(AtomicBool::new(false));
    let flusher = (!flusher_interval.is_zero()).then(|| {
        let (client, calls, stop) = (client.clone(), flush_calls.clone(), stop_flusher.clone());
        tokio::spawn(async move {
            loop {
                tokio::time::sleep(flusher_interval).await;
                if stop.load(Ordering::Relaxed) {
                    break;
                }
                calls.fetch_add(1, Ordering::Relaxed);
                let _ = client.flush().await;
            }
        })
    });

    // Emitters.
    let tasks: Vec<_> = (0..emitters)
        .map(|emitter| {
            let client = client.clone();
            tokio::spawn(async move {
                let mut out = Emitted::default();
                for seq in 0..per_emitter {
                    let event = EventInput {
                        event: "chaos.event".to_string(),
                        metadata: Some(HashMap::from([
                            ("emitter".to_string(), emitter.to_string()),
                            ("seq".to_string(), seq.to_string()),
                        ])),
                        ..EventInput::default()
                    };
                    let start = Instant::now();
                    let result = client.events().emit(event).await;
                    out.latencies.push(start.elapsed().as_secs_f64() * 1000.0);
                    match result {
                        Ok(emitted) if emitted.queued => out.queued += 1,
                        Ok(_) => {}
                        Err(error) => {
                            let key =
                                format!("{}:{}", error.kind().as_str(), error.code().unwrap_or(""));
                            *out.errors.entry(key).or_default() += 1;
                        }
                    }
                    if !emit_interval.is_zero() {
                        tokio::time::sleep(emit_interval).await;
                    }
                }
                out
            })
        })
        .collect();

    let mut latencies = Vec::new();
    let mut queued = 0;
    let mut errors: BTreeMap<String, u64> = BTreeMap::new();
    let mut caller_exits = 0;
    for task in tasks {
        match task.await {
            Ok(out) => {
                latencies.extend(out.latencies);
                queued += out.queued;
                for (key, n) in out.errors {
                    *errors.entry(key).or_default() += n;
                }
            }
            Err(_) => caller_exits += 1,
        }
    }
    stop_flusher.store(true, Ordering::Relaxed);
    if let Some(flusher) = flusher {
        let _ = flusher.await;
    }

    let close_start = Instant::now();
    if let Err(error) = client.close().await {
        let key = format!("{}:{}", error.kind().as_str(), error.code().unwrap_or(""));
        *errors.entry(key).or_default() += 1;
    }
    let close_ms = ms(close_start.elapsed());

    stop_heartbeat.store(true, Ordering::Relaxed);
    let _ = heartbeat.await;

    latencies.sort_by(f64::total_cmp);
    let round3 = |value: f64| (value * 1000.0).round() / 1000.0;
    json!({
        "sdk": "rust",
        "emits": latencies.len(),
        "queued": queued,
        "errors": errors,
        "caller_exits": caller_exits,
        "emit_ms": {
            "p50": round3(percentile(&latencies, 0.50)),
            "p99": round3(percentile(&latencies, 0.99)),
            "max": round3(latencies.last().copied().unwrap_or(0.0)),
        },
        "heartbeat_max_lag_ms": ms(Duration::from_nanos(max_lag_ns.load(Ordering::Relaxed))),
        "close_ms": close_ms,
        "flush_calls": flush_calls.load(Ordering::Relaxed),
    })
}
