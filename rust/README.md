# santati (Rust)

The official Rust SDK for the [Santati](https://santati.io) audit-log API:
emit one audit event or a batch, list events with filters and cursor
pagination, and iterate across pages. The crate is async and runs on
[Tokio](https://tokio.rs).

## Install

```sh
cargo add santati
cargo add tokio --features macros,rt-multi-thread
```

## Quickstart

```rust
use santati::{ActorInput, EventInput, ListParams, Santati, StreamExt};

#[tokio::main]
async fn main() -> Result<(), santati::Error> {
    let client = Santati::builder("sat_sk_...")
        .trail("billing") // the default trail for emits
        .build()?;
    let events = client.events();

    // Emit one event: the idempotency key is generated for you, and a
    // replay of an earlier request reports `duplicate: true`.
    let result = events
        .emit(EventInput {
            event: "invoice.voided".into(),
            organization_id: Some("org_acme".into()),
            actor: Some(ActorInput {
                r#type: "user".into(),
                id: Some("usr_123".into()),
                ..Default::default()
            }),
            ..Default::default()
        })
        .await?;
    if let Some(event) = &result.event {
        println!("{} (duplicate: {})", event.id, result.duplicate);
    }

    // Emit a batch: a 207 is a `BatchResult` whose rejected items carry the
    // server's error, not a failure of the call.
    let batch = events
        .emit_batch(vec![
            EventInput::new("invoice.voided"),
            EventInput::new("invoice.paid"),
        ])
        .await?;
    println!("{} accepted, {} rejected", batch.accepted, batch.rejected);

    // List one page: only the filters you set are sent, and the client's
    // default trail never applies to reads.
    let page = events
        .list(ListParams {
            trail: Some("billing".into()),
            created_after: Some("2026-09-01T00:00:00+00:00".into()),
            limit: Some(50),
            ..Default::default()
        })
        .await?;
    println!("{} events, next cursor: {:?}", page.results.len(), page.next_cursor);

    // Iterate every page: the stream is lazy, and an error on a later page
    // arrives after the earlier events. `iterate` returns a `!Unpin` stream,
    // so pin it before polling.
    let mut all = std::pin::pin!(events.iterate(ListParams {
        trail: Some("billing".into()),
        ..Default::default()
    }));
    while let Some(event) = all.next().await {
        println!("{}", event?.event);
    }

    Ok(())
}
```

## Options

| option | default | meaning |
|---|---|---|
| `api_key` | required | team API key (`sat_sk_…`); sent as `Authorization: Api-Key <key>` |
| `base_url` | `https://api.santati.io` | trailing `/` removed; may carry a path prefix |
| `trail` | none | default trail for emits; never applied to reads |
| `timeout` | 10s | per attempt |
| `max_retries` | 2 | retries after the first attempt |
| `backoff(initial, max)` | 250ms, 8s | exponential backoff bounds |
| `header(name, value)` | none | extra header on every request |
| `outbox(store)` | none | when set, `emit` queues into `store` instead of sending |

Building fails with a `ValidationError` (status `None`) for an empty `api_key`
(field `api_key`) or an `Authorization` header (field `headers`). Redirects are
never followed, and the SDK's own retry loop is the only retry: transport
failures, 500/502/503/504 and 429 (unless the code is `quota_exceeded`) are
retried with the identical request, honouring `Retry-After` when the server
sends one.

## Outbox

Without an outbox, `emit` sends the event. With `Builder::outbox`, `emit` is
fire-and-forget: it validates like a plain `emit`, stores the envelope in the
outbox and returns at once with `queued` set and no event, without making a
request. A background task sends the outbox in batches; `close` stops it and
drains what is left, so call it before exiting.

```rust
use santati::{EventInput, MemoryOutbox, Santati};

#[tokio::main]
async fn main() -> Result<(), santati::Error> {
    let client = Santati::builder("sat_sk_...")
        .trail("billing")
        .outbox(MemoryOutbox::new(10_000)?)
        .build()?;
    let result = client.events().emit(EventInput::new("invoice.voided")).await?;
    assert!(result.queued);
    client.close().await?;
    Ok(())
}
```

`MemoryOutbox` is the in-process store (lost on exit). `close()` sends each batch once; if
the endpoint is down, what it could not send stays in the store and, in memory, is lost
with the process. Tune the worker with
`batch_size`, `flush_interval`, `pre_send` and `post_send` on the
builder. For a Redis Streams store, enable the optional `redis` feature
(`santati = { version = "0.1", features = ["redis"] }`) and pass
`RedisOutbox::new(connection_manager)` to `Builder::outbox`.

## Errors

One `Error` enum with eight kinds, each carrying `ErrorDetails`:

```rust
use santati::ErrorKind;

match client.events().list(Default::default()).await {
    Err(error) if error.kind() == ErrorKind::RateLimited => {
        eprintln!("slow down, retry after {:?}s", error.retry_after());
    }
    Err(error) => eprintln!("{}: {}", error.kind(), error.message()),
    Ok(page) => println!("{} events", page.results.len()),
}
```

`ErrorKind` is `Validation`, `Auth`, `NotFound`, `RateLimited`, `Server`,
`Transport`, `Api` or `Outbox`; `error.status()`, `error.code()` and `error.field()`
carry the server's side of the failure when there is one.

## Notes

- `AuditEvent`, `EventActor` and `EventTarget` are the generated read models,
  re-exported here so you never import the generated core directly.
- The exact wire contract, error mapping and retry policy are normative in
  [`docs/sdk-surface.md`](../docs/sdk-surface.md), and enforced by the shared
  vectors in [`conformance/`](../conformance/README.md).

## License

Apache-2.0.
