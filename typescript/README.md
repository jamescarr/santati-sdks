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
console.log(event.id, duplicate, idempotencyKey);

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

A `401`/`403`/`404`/`429`/`5xx`/transport failure and any `400`/`413`/`422` are
raised as `AuthError`, `NotFoundError`, `RateLimitedError`, `ServerError`,
`TransportError` and `ValidationError` respectively — all of them
`SantatiError`s carrying `status`, `code`, `field` and `retryAfter`. The client
retries transport failures, `500/502/503/504` and `429` (unless the code is
`quota_exceeded`), re-sending the identical request, and never follows a
redirect.

## License

Apache-2.0.
