// Chaos driver: emits into the outbox against the gateway in SANTATI_CHAOS and prints one CHAOS_RESULT line.
// Run by `mise run chaos typescript` (chaos/run.mjs); inert unless SANTATI_CHAOS is set.
import { monitorEventLoopDelay, performance } from "node:perf_hooks";
import { setTimeout as sleep } from "node:timers/promises";

import { MemoryOutbox, Santati, SantatiError } from "../index.js";

interface ChaosConfig {
  client: {
    base_url: string;
    timeout_ms: number;
    max_retries: number;
    initial_backoff_ms: number;
    max_backoff_ms: number;
    batch_size: number;
    flush_interval_ms: number;
    max_pending: number;
  };
  emitters: number;
  events_per_emitter: number;
  emit_interval_ms: number;
  flusher_interval_ms: number;
  heartbeat_ms: number;
}

function percentile(sorted: number[], q: number): number {
  if (sorted.length === 0) return 0;
  return sorted[Math.max(Math.ceil(q * sorted.length) - 1, 0)];
}

const round3 = (value: number): number => Math.round(value * 1000) / 1000;

async function main(): Promise<void> {
  const raw = process.env.SANTATI_CHAOS;
  if (!raw) {
    console.log("SANTATI_CHAOS not set; skipping");
    return;
  }
  const cfg = JSON.parse(raw) as ChaosConfig;
  const opts = cfg.client;

  const client = new Santati({
    apiKey: "sat_sk_chaos",
    baseUrl: opts.base_url,
    trail: "chaos",
    timeoutMs: opts.timeout_ms,
    maxRetries: opts.max_retries,
    initialBackoffMs: opts.initial_backoff_ms,
    maxBackoffMs: opts.max_backoff_ms,
    batchSize: opts.batch_size,
    flushIntervalMs: opts.flush_interval_ms,
    outbox: new MemoryOutbox({ maxPending: opts.max_pending }),
  });

  const loopDelay = monitorEventLoopDelay({ resolution: 10 });
  loopDelay.enable();

  const latencies: number[] = [];
  const errors: Record<string, number> = {};
  const countError = (key: string): void => {
    errors[key] = (errors[key] ?? 0) + 1;
  };
  let queued = 0;
  let crashes = 0;
  let flushCalls = 0;
  let emitting = true;

  const flusher = async (): Promise<void> => {
    while (emitting) {
      await sleep(cfg.flusher_interval_ms);
      if (!emitting) break;
      flushCalls += 1;
      try {
        await client.flush();
      } catch {
        // A failed flush pass is the SDK's to report through post_send; the driver only counts calls.
      }
    }
  };

  const emitter = async (index: number): Promise<void> => {
    try {
      for (let seq = 0; seq < cfg.events_per_emitter; seq += 1) {
        const start = performance.now();
        try {
          const result = await client.events.emit({
            event: "chaos.event",
            metadata: { emitter: String(index), seq: String(seq) },
          });
          if (result.queued) queued += 1;
        } catch (error) {
          if (error instanceof SantatiError) countError(`${error.name}:${error.code}`);
          else countError(`${error instanceof Error ? error.name : "Error"}:unexpected`);
        }
        latencies.push(performance.now() - start);
        if (cfg.emit_interval_ms > 0) await sleep(cfg.emit_interval_ms);
      }
    } catch {
      crashes += 1;
    }
  };

  const flusherDone = cfg.flusher_interval_ms > 0 ? flusher() : Promise.resolve();
  await Promise.all(Array.from({ length: cfg.emitters }, (_, index) => emitter(index)));
  emitting = false;
  await flusherDone;

  const closeStart = performance.now();
  try {
    await client.close();
  } catch (error) {
    if (error instanceof SantatiError) countError(`${error.name}:${error.code}`);
    else countError(`${error instanceof Error ? error.name : "Error"}:unexpected`);
  }
  const closeMs = performance.now() - closeStart;
  loopDelay.disable();

  const sorted = [...latencies].sort((a, b) => a - b);
  const result = {
    sdk: "typescript",
    emits: sorted.length,
    queued,
    errors,
    caller_exits: crashes,
    emit_ms: {
      p50: round3(percentile(sorted, 0.5)),
      p99: round3(percentile(sorted, 0.99)),
      max: round3(sorted.length > 0 ? sorted[sorted.length - 1] : 0),
    },
    // An empty histogram reports 9.2e18; the loop was observed whenever it holds a sample.
    heartbeat_max_lag_ms: loopDelay.count > 0 ? round3(loopDelay.max / 1e6) : 0,
    close_ms: round3(closeMs),
    flush_calls: flushCalls,
  };
  console.log(`CHAOS_RESULT ${JSON.stringify(result)}`);
}

await main();
