# The SDK surface

Normative. Every SDK in this repo implements exactly this surface, and
`conformance/` tests it; a behaviour change here lands with vectors and seven
facade changes in one PR (see `AGENTS.md`).

Names are language-neutral. Each language maps them per the tables at the
bottom: `snake_case` for Python/Rust/Elixir/Ruby/PHP, `camelCase` for
TypeScript, exported struct fields for Go.

Scope boundary: no background batching/flush, no async variants of the
sync-language clients, no `metadata.<key>` read filters (the spec cannot
express a templated query parameter).

## Client options

|option|default|meaning|
|---|---|---|
|`api_key`|required, non-empty|team API key (`sat_sk_…`)|
|`base_url`|`https://api.santati.io`|trailing `/` removed; may carry a path prefix. Requests go to `<base_url>/api/v0/events/`|
|`trail`|none|default trail for emits; **never** applied to reads|
|`timeout_ms`|`10000`|per attempt|
|`max_retries`|`2`|retries after the first attempt|
|`initial_backoff_ms`|`250`|backoff base|
|`max_backoff_ms`|`8000`|backoff cap|
|`headers`|none|extra headers on every request|

Construction raises a `ValidationError` (with `status` null) for an empty
`api_key` (field `api_key`) or a `headers` key equal to `authorization`
case-insensitively (field `headers`).

## Every request

- `Authorization: Api-Key <api_key>`
- `User-Agent: santati-<package dir>/<version>` (e.g. `santati-python/0.1.0`)
- the client's extra headers
- redirects are **never** followed: a 3xx is a response, so the key is never
  replayed to another host
- no library-level retries (the facade's own retry loop is the only one)

## Operations

### `emit(event, trail?, organization_id?, actor?, targets?, metadata?, data?, context?, created_at?, idempotency_key?) → EmitResult`

Local validation before any request: an empty `event` → `ValidationError` with
field `event`; the resolved trail (the event's, else the client's) empty or
absent → field `trail`.

The body is the envelope with exactly the supplied members (snake_case wire
names), `trail` resolved, and `idempotency_key` = the caller's or a freshly
generated lowercase UUIDv4. Absent or null inputs are omitted — `null` is
never sent. `created_at` is an RFC 3339 string forwarded verbatim. `actor` is
`{type, id?, name?, metadata?}`; a target is `{type, id, name?, metadata?}`.
The `Idempotency-Key` header is never sent (the envelope key wins on the server
and the header is unsafe for batches).

`POST /api/v0/events/`:

- `201` → `EmitResult{event: AuditEvent, duplicate: false, idempotency_key}`
- `200` → same, `duplicate: true` (the server replayed an earlier request)
- any other 2xx → `ApiError`

### `emit_batch(events) → BatchResult`

An empty list → `ValidationError` field `events`. Per item the `emit` rules
with fields `events[<i>].event` and `events[<i>].trail`; the first failing item
wins. Body `{"events": [envelope…]}`, one generated key per item lacking one.

- `202` or `207` → `BatchResult{accepted, rejected, results: [BatchItem{index,
  status: accepted|duplicate|rejected, id?, error?: BatchItemError{code,
  message, field?}}]}` (207 is a result, not an error)
- other 2xx → `ApiError`

The 500-event and 1 MiB caps are enforced by the server (`413` →
`ValidationError`).

### `list(trail?, event?, event_prefix?, organization_id?, actor_id?, actor_type?, target_type?, target_id?, created_after?, created_before?, q?, sort?, limit?, cursor?) → EventPage`

`GET /api/v0/events/` with only the supplied parameters, in spec (alphabetical)
order, encoded by the generated core. The client's default `trail` does not
apply to reads. `200` → `EventPage{results: [AuditEvent], next_cursor}` where
`next_cursor` is the decoded `cursor` query parameter of the response's `next`
URL, or null when `next` is null or carries no `cursor`.

### `iterate(<the same parameters minus cursor>)`

A lazy sequence of `AuditEvent`: calls `list` with the same parameters plus
`cursor = next_cursor` until `next_cursor` is null. An error on a later page is
raised after the earlier events were yielded.

## Errors

One base type, seven kinds, each with `status` (int|null), `code`
(string|null), `field` (string|null), `retry_after` (int seconds|null) and
`message`:

|condition|kind|
|---|---|
|local validation|`ValidationError` (status null)|
|HTTP 400, 413, 422|`ValidationError`|
|HTTP 401, 403|`AuthError`|
|HTTP 404|`NotFoundError`|
|HTTP 429|`RateLimitedError`|
|HTTP 500–599|`ServerError`|
|no HTTP response (refused, DNS, TLS, timeout)|`TransportError` (status null)|
|any other non-2xx, an unexpected 2xx, an undecodable 2xx body|`ApiError`|

Error-body parsing: a JSON object with an `error` object holding a string
`code` → `code`, `field` (string or null) and `message` come from it; a JSON
object with a string `detail` → `message = detail`; otherwise `message =
"HTTP <status>"`. `retry_after` is the integer value of `Retry-After` when it
matches `^\d+$`, else null.

## Retries

Attempts = 1 + `max_retries`. Retryable: transport failures, 500/502/503/504,
and 429 unless `code == "quota_exceeded"`.

Before retry *n* (1-based): if the failed attempt carried `retry_after = R`,
wait `R*1000` ms, but if that exceeds `max_backoff_ms` stop and raise now;
otherwise wait a uniform random integer in `[0, min(initial_backoff_ms *
2^(n-1), max_backoff_ms)]` ms. Every attempt re-sends the identical request —
same body, same idempotency keys.

## Per-language surface

`EmitResult`, `BatchResult`, `BatchItem`, `BatchItemError` and `EventPage` are
facade types; `AuditEvent`, `EventActor` and `EventTarget` are the generated
read models, re-exported from the public entry point.

| |Python|TypeScript|Go|Rust|Elixir|Ruby|PHP|
|---|---|---|---|---|---|---|---|
|package|PyPI `santati`, import `santati`|npm `@santati/node`|`github.com/jamescarr/santati-sdks/go`, package `santati`|crate `santati`|Hex `santati`|gem `santati`|Composer `santati/santati-php`|
|client|`Santati(api_key, *, base_url, trail, timeout_ms, max_retries, initial_backoff_ms, max_backoff_ms, headers)`, context manager + `close()`|`new Santati({apiKey, baseUrl, trail, timeoutMs, maxRetries, initialBackoffMs, maxBackoffMs, headers})`|`NewClient(apiKey string, opts ...Option) (*Client, error)`; `WithBaseURL`, `WithTrail`, `WithTimeout(time.Duration)`, `WithMaxRetries(int)`, `WithBackoff(initial, max time.Duration)`, `WithHeader(k, v)`|`Santati::builder(api_key).base_url(..).trail(..).timeout(Duration).max_retries(u32).backoff(Duration, Duration).header(k, v).build() -> Result<Santati, Error>`|`Santati.new(keyword) :: {:ok, %Santati.Client{}} \| {:error, %Santati.ValidationError{}}`|`Santati::Client.new(api_key:, base_url:, trail:, timeout_ms:, max_retries:, initial_backoff_ms:, max_backoff_ms:, headers:)`|`new Santati\Client(apiKey:, baseUrl:, trail:, timeoutMs:, maxRetries:, initialBackoffMs:, maxBackoffMs:, headers:)`|
|resource|`client.events`|`santati.events`|`client.Events`|`client.events()`|module `Santati.Events`|`client.events`|`$client->events` (public readonly)|
|emit|`emit(event, *, trail=…, organization_id=…, actor=…, targets=…, metadata=…, data=…, context=…, created_at=…, idempotency_key=…)`|`emit({event, trail?, organizationId?, actor?, targets?, metadata?, data?, context?, createdAt?, idempotencyKey?}): Promise<EmitResult>`|`Emit(ctx, EventInput) (*EmitResult, error)`|`async fn emit(&self, EventInput) -> Result<EmitResult, Error>`|`emit(client, map) :: {:ok, %Santati.EmitResult{}} \| {:error, exception}`|`emit(event:, trail: nil, …)`|`emit(array $event): EmitResult` (snake_case keys)|
|batch|`emit_batch(events: Sequence[EventInput])` (TypedDict)|`emitBatch(events: EventInput[])`|`EmitBatch(ctx, []EventInput)`|`emit_batch(Vec<EventInput>)`|`emit_batch(client, [map])`|`emit_batch(events)` (hashes, symbol keys)|`emitBatch(array $events)`|
|list|`list(*, trail=…, …, limit=…, cursor=…)`|`list(params?: ListParams)` (camelCase)|`List(ctx, ListParams)`|`list(ListParams)`|`list(client, keyword)`|`list(trail: nil, …, limit: nil, cursor: nil)`|`list(array $params = [])`|
|iterate|`iterate(*, trail=…, …, limit=…) -> Iterator[AuditEvent]`|`iterate(params?): AsyncGenerator<AuditEvent>`|`Iterate(ctx, ListParams) iter.Seq2[*AuditEvent, error]`|`iterate(ListParams) -> impl Stream<Item = Result<AuditEvent, Error>>`|`stream(client, keyword) :: Enumerable.t()` (raises the error exception)|`iterate(trail: nil, …, limit: nil) -> Enumerator`|`iterate(array $params = []): \Generator`|
|errors|`santati.SantatiError` + `ValidationError` … `ApiError`|same class names|`*santati.Error{Kind, Status int (0 = none), Code, Field string, RetryAfter *int, Message}`; `Kind` constants `KindValidation`…`KindAPI` whose string values are the kind names|`santati::Error` enum `Validation`, `Auth`, `NotFound`, `RateLimited`, `Server`, `Transport`, `Api`, each holding `ErrorDetails{status: Option<u16>, code, field: Option<String>, retry_after: Option<u64>, message: String}`|`Santati.ValidationError` … `Santati.ApiError` exceptions with fields `status, code, field, retry_after, message`|`Santati::Error` + `Santati::ValidationError` …|`Santati\Exception\SantatiException` + `ValidationException`, `AuthException`, `NotFoundException`, `RateLimitedException`, `ServerException`, `TransportException`, `ApiException`; getters `getStatus()`, `getErrorCode()`, `getField()`, `getRetryAfter()`|

UUIDv4 generation: Python `uuid.uuid4()`, TypeScript `crypto.randomUUID()`, Go
16 bytes from `crypto/rand` with the version/variant bits set, Rust
`uuid::Uuid::new_v4()`, Elixir `:crypto.strong_rand_bytes(16)` with bits set,
Ruby `SecureRandom.uuid`, PHP `random_bytes(16)` with bits set; always
lowercase `8-4-4-4-12`.

## Generated cores

Each facade sits on the generated core from `generator/config.json`, used
through one configuration/client object per facade client — never the
generators' process-wide defaults — and validates locally before constructing
any generated model. A generated request model that rejects a spec constraint
(e.g. an over-long name) surfaces as a `ValidationError` with `status` null.
