# @santati/node

The official TypeScript SDK for the Santati audit-log API: emit events (one or a
batch) with generated idempotency keys and retries, and read them back with
cursor pagination. Node 22+, ESM only, no runtime dependencies.

## Install

```sh
npm install @santati/node
```

## Quickstart

```ts
import { Santati } from "@santati/node";

const santati = new Santati({
  apiKey: process.env.SANTATI_API_KEY!, // sat_sk_…
  trail: "billing", // default trail for emits; never applied to reads
});

// One event. The wire names are snake_case; the inputs here are camelCase.
const { event, duplicate, idempotencyKey } = await santati.events.emit({
  event: "invoice.voided",
  organizationId: "org_acme",
  actor: { type: "user", id: "usr_123" },
  targets: [{ type: "invoice", id: "inv_555" }],
  data: { amount_cents: 4200 },
});
console.log(event?.id, duplicate, idempotencyKey); // `event` is null only for a queued emit

// A batch: one request, one result per item (`207` is a result, not an error).
const batch = await santati.events.emitBatch([
  { event: "invoice.paid", idempotencyKey: "evt-1" },
  { event: "invoice.sent" },
]);
console.log(batch.accepted, batch.rejected, batch.results);

// One page…
const page = await santati.events.list({ trail: "billing", eventPrefix: "invoice.", limit: 100 });
console.log(page.results, page.nextCursor);

// …or every page, one request at a time.
for await (const read of santati.events.iterate({ trail: "billing" })) {
  console.log(read.id, read.event);
}
```

## Outbox

Without an `outbox`, `events.emit` sends the event and resolves with the stored
one. With one, `emit` is fire-and-forget: it validates locally, stores the
event in the outbox and resolves at once with `queued: true`, `event: null` and
the idempotency key, without making a request. A background worker (every
`flushIntervalMs`) sends the outbox in batches of `batchSize` through
`events.emitBatch`; `close()` stops it and flushes what is left.

```ts
const santati = new Santati({
  apiKey: process.env.SANTATI_API_KEY!,
  trail: "billing",
  outbox: new MemoryOutbox(),
  postSend: (event, outcome) => console.log(event.event, outcome.status),
});

const { queued, idempotencyKey } = await santati.events.emit({
  event: "invoice.voided",
  organizationId: "org_acme",
});
await santati.close(); // sends anything still pending
```

`MemoryOutbox` is a bounded in-memory store (10000 events, lost on
exit). For a durable outbox shared across processes, install the optional
`ioredis` peer dependency and pass a `RedisOutbox` (a Redis stream with the
consumer group `santati`):

```sh
npm install ioredis
```

```ts
import { Redis } from "ioredis";
import { RedisOutbox } from "@santati/node/redis";

const santati = new Santati({
  apiKey: process.env.SANTATI_API_KEY!,
  trail: "billing",
  outbox: new RedisOutbox(new Redis(process.env.REDIS_URL!)),
});
```

`preSend(event)` may return a modified event, or `null` to drop it;
`postSend(event, outcome)` sees every attempt's `accepted`, `duplicate`,
`rejected` or `failed` outcome. Store failures raise `OutboxError`
(`outbox_full`, `store_unavailable`, `closed`); a `preSend` that throws, or
whose result makes the send throw a non-SDK error, is a `failed` outcome with
`OutboxError` `hook_failed`.

## Options

| option             | default                  | meaning                                        |
| ------------------ | ------------------------ | ---------------------------------------------- |
| `apiKey`           | required, non-empty      | team API key (`sat_sk_…`)                      |
| `baseUrl`          | `https://api.santati.io` | trailing `/` ignored; a path prefix is kept    |
| `trail`            | none                     | default trail for emits                        |
| `timeoutMs`        | `10000`                  | per attempt                                    |
| `maxRetries`       | `2`                      | retries after the first attempt                |
| `initialBackoffMs` | `250`                    | backoff base                                   |
| `maxBackoffMs`     | `8000`                   | backoff cap, and the largest `Retry-After`     |
| `headers`          | none                     | extra headers on every request                 |
| `outbox`           | none                     | when set, `emit` queues here                   |
| `batchSize`        | `100`                    | envelopes per outbox request, `1..500`         |
| `flushIntervalMs`  | `1000`                   | the worker's tick, `> 0`                       |
| `preSend`          | none                     | per-event hook before the request              |
| `postSend`         | none                     | per-event hook with the outcome                |

A `401`/`403`/`404`/`429`/`5xx`/transport failure and any `400`/`413`/`422` are
raised as `AuthError`, `NotFoundError`, `RateLimitedError`, `ServerError`,
`TransportError` and `ValidationError` respectively — all of them
`SantatiError`s carrying `status`, `code`, `field` and `retryAfter`. The client
retries transport failures, `500/502/503/504` and `429` (unless the code is
`quota_exceeded`), re-sending the identical request, and never follows a
redirect.

## License

Apache-2.0.
