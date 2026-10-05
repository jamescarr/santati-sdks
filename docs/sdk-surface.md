# The SDK surface

Normative. Every SDK in this repo implements exactly this surface, and
`conformance/` tests it; a behaviour change here lands with vectors and seven
facade changes in one PR (see `AGENTS.md`).

Names are language-neutral. Each language maps them per the tables at the
bottom: `snake_case` for Python/Rust/Elixir/Ruby/PHP, `camelCase` for
TypeScript, exported struct fields for Go.

Scope boundary: no async variants of the sync-language clients, no
`metadata.<key>` read filters (the spec cannot express a templated query
parameter). Language-specific framework integrations (Python's
`santati.integrations`, behind extras) build envelopes and call the public
`emit`; they add no wire behaviour, are outside this surface and
`conformance/`, and need no counterpart in other SDKs.

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
|`outbox`|none|when set, `emit` enqueues into this `OutboxStore` instead of sending ([Outbox delivery](#outbox-delivery-emit-with-an-outbox))|
|`batch_size`|`100`|envelopes per outbox request, `1..500`; only used with an `outbox`|
|`flush_interval_ms`|`1000`|the outbox worker's tick, `> 0`. PHP has no worker and no such option; only used with an `outbox`|
|`pre_send`|none|hook, see [Hooks](#hooks); only used with an `outbox`|
|`post_send`|none|hook, see [Hooks](#hooks); only used with an `outbox`|

Construction raises a `ValidationError` (with `status` null) for an empty
`api_key` (field `api_key`), a `headers` key equal to `authorization`
case-insensitively (field `headers`), a `batch_size` outside `1..500` (field
`batch_size`) or a `flush_interval_ms` not above zero (field
`flush_interval_ms`).

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
absent → field `trail`. Nothing else is checked locally: values the server
validates (an empty metadata value, a name past 255 characters) are forwarded
unchanged, and the server's answer decides.

The body is the envelope with exactly the supplied members (snake_case wire
names), `trail` resolved, and `idempotency_key` = the caller's or a freshly
generated lowercase UUIDv4. Absent or null inputs are omitted — `null` is
never sent. `created_at` is an RFC 3339 string forwarded verbatim. `actor` is
`{type, id?, name?, metadata?}`; a target is `{type, id, name?, metadata?}`.
The `Idempotency-Key` header is never sent (the envelope key wins on the server
and the header is unsafe for batches).

`POST /api/v0/events/`:

- `201` → `EmitResult{event: AuditEvent, duplicate: false, idempotency_key, queued: false}`
- `200` → same, `duplicate: true` (the server replayed an earlier request)
- any other 2xx → `ApiError`

With an `outbox` configured, `emit` does not send: see [Outbox
delivery](#outbox-delivery-emit-with-an-outbox).

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

### Outbox delivery (emit with an outbox)

When the client has an `outbox`, `emit` is fire-and-forget. It runs exactly the
local validation above (the same code path: `ValidationError` field `event` /
`trail`), resolves `trail` (the event's, else the client's) and
`idempotency_key` (the caller's, else a fresh lowercase UUIDv4), then
`enqueue`s the *stored event* — the facade's `EventInput` shape with `trail`
and `idempotency_key` filled in, identical to the wire envelope (snake_case)
when serialized — and returns `EmitResult{event: null, duplicate: false,
idempotency_key, queued: true}`. The stored event is a snapshot: changing the
caller's objects (`metadata`, `actor`, `targets`, `data`, `context`) after a
queued `emit` returns does not change it. **A queued `emit` never makes a
request.** It starts the [worker](#worker) if it is not running (lazily, on the
first queued `emit`).

Store failures surface as `OutboxError` (status null): a full memory outbox →
`code = "outbox_full"`; any other store failure (connection refused, …) →
`code = "store_unavailable"`, `message` = the underlying error's text. A
store's own `OutboxError` passes through unchanged; any foreign
exception/error returned by `enqueue` is wrapped as `store_unavailable`.

`emit_batch`, `list` and `iterate` never touch the outbox (the worker itself
sends through `emit_batch`).

### `flush()`, `close()`

`flush()` runs **one pass** (defined under [Worker](#worker)) synchronously —
blocking or awaited — and returns. It raises `OutboxError(store_unavailable)`
when the store fails during the pass (claim/ack/release); send failures never
raise, they go through the delivery policy.

`close()` stops the worker (signals it and waits for any pass in progress),
runs one `flush()`, then releases pooled connections where the facade already
did so (Python). `close()` is idempotent: a second call returns immediately
without another pass. After `close()`, a queued `emit` raises `OutboxError` code
`closed`.

`close()` is one pass, and the pass sends each batch once: a batch that fails with
a retryable error (`TransportError`, `ServerError`, `RateLimitedError`) goes back
to the store and `close()` returns without it. A `close()` or shutdown that lands
while the ingest endpoint is down therefore leaves its backlog in the store. With
a `MemoryOutbox` that backlog is lost when the process exits — that is what an
in-memory store is. Use a `RedisOutbox` when events must outlive the process: they
stay in the stream and are re-delivered (after `visibility_ms`) to whichever process
drains it next, in any SDK.

Without an `outbox`, `flush()` returns immediately (no request, no error) and
`close()` runs no pass (Python still clears its connection pool); `close()` is
idempotent and `emit` keeps working afterwards — `closed` exists only for
queued emits.

- Python, TypeScript, Go, Rust, Ruby, PHP: `close()` on the client.
- Go: `Close(ctx)` cancels a worker send in progress instead of waiting for it (the
  batch is released and re-sent by the final flush) and runs the final flush under
  `ctx`; if `ctx` ends first it returns `ctx.Err()` and what was not sent stays in
  the store. A `Flush` running on another goroutine holds the pass lock, and
  `Close` waits for it.
- Elixir: stopping the `Santati.Outbox` process (`terminate/2`) is the close;
  a later `emit` through a client whose `outbox` names the stopped server exits
  the caller like any dead GenServer. `Santati.flush/1` runs its pass in a
  Task; the server keeps accepting queued emits meanwhile.
- PHP: `close()` = `flush()` + mark closed; the first queued `emit` also
  registers a `register_shutdown_function` that runs `flush()` unless
  `close()` already ran. With `finishRequestBeforeFlush`, the shutdown function
  first calls `fastcgi_finish_request()` when it exists, so the HTTP response is
  not held by the flush.

`flush()` and the worker's pass are mutually exclusive (one lock per client): a
pass never runs concurrently with another pass of the same client.

## Outbox

### `OutboxStore`

The interface users may implement:

```
enqueue(event: StoredEvent) → void        # store at the tail; raise/return OutboxError(outbox_full) or any error
claim(limit: int) → [OutboxEntry{id: string, event: StoredEvent}]
                                          # up to `limit` oldest entries, FIFO; a claimed entry is not returned
                                          # by another claim until released (Redis: until stale)
ack(ids: [string]) → void                 # delete permanently
release(ids: [string]) → void             # make eligible for a later claim (timing is store-specific)
```

No `close`/`size`. `claim(0)` → empty. A store MUST be safe to call from the
worker and from a queued `emit` concurrently.

**`MemoryOutbox(max_pending=10000)`** — a FIFO queue plus a claimed map behind
a mutex. `enqueue` raises `OutboxError(outbox_full)` when `len(pending) +
len(claimed) >= max_pending`. `claim(n)` moves the first `n` pending entries to
claimed; `ack` deletes from claimed; `release` moves them back to the **front**
of pending, preserving their relative order. Entry ids are decimal strings of a
per-store counter starting at `"1"`. `max_pending < 1` → construction raises
`ValidationError` field `max_pending`. Events live only as long as the process: what
is still stored at exit, including a batch `close()` could not send, is lost. Use
`RedisOutbox` to keep them.

**`RedisOutbox(client, key="santati:outbox", visibility_ms=60000)`** — takes
the user's existing Redis client (the SDK never opens connections). Redis
Streams with one consumer group; the layout is identical in all seven SDKs, so
any SDK can drain what another enqueued:

- group `santati`; consumer name = a fresh lowercase UUIDv4 per store instance;
- lazy init on the first call: `XGROUP CREATE <key> santati 0 MKSTREAM`, a
  `BUSYGROUP` error ignored;
- `enqueue`: `XADD <key> * event <compact JSON of the wire envelope>`;
- `claim(n)`: `XAUTOCLAIM <key> santati <consumer> <visibility_ms> 0-0 COUNT n`
  (re-delivers entries any consumer left pending for ≥ `visibility_ms`); when
  fewer than `n` came back, `XREADGROUP GROUP santati <consumer> COUNT <n-got>
  STREAMS <key> >`. Each entry → `{id: <stream id>, event:
  parse(fields.event)}`. An entry without an `event` field or with undecodable
  JSON is poison: `XACK` + `XDEL` it and skip it. `nil`/deleted placeholders in
  an `XAUTOCLAIM` reply (Redis 7) are skipped;
- `ack(ids)`: `XACK <key> santati ids…` then `XDEL <key> ids…`;
- `release(ids)`: no-op — the entries stay pending and `XAUTOCLAIM`
  re-delivers them after `visibility_ms`;
- any client-library error → `OutboxError(store_unavailable)`. The Redis store
  has no capacity bound. Where a library has no typed method for a command,
  its raw-command call is used.

### Worker

Started lazily by the first queued `emit`. Every `flush_interval_ms` it runs a
**pass**:

```
pass():
  loop:
    entries = store.claim(batch_size)
    if entries is empty: return
    released = false; to_send = []
    for entry in entries:
      if pre_send is set:
        try: out = pre_send(entry.event)
        except e: store.release([entry.id]); released = true
                  post_send(entry.event, {status: failed, error: OutboxError(code "hook_failed", message: text of e)}); continue
      else: out = entry.event
      if out is null: store.ack([entry.id]); continue           # dropped: no request, no post_send
      to_send.append((entry, out))
    if to_send is not empty:
      try: result = events.emit_batch([out for (entry, out) in to_send])   # one attempt: no retries inside a pass; a released batch is retried by a later pass
      except SantatiError e:
        for (entry, _) in to_send: post_send(entry.event, {status: failed, error: e})
        if e is TransportError | ServerError | RateLimitedError: store.release(ids of to_send); released = true
        else: store.ack(ids of to_send)                                     # non-retryable: dropped
      except any other exception x:                                         # e.g. emit_batch rejecting a malformed pre_send result
        for (entry, _) in to_send: post_send(entry.event, {status: failed, error: OutboxError(code "hook_failed", message: text of x)})
        store.ack(ids of to_send)                                           # non-retryable: dropped; never raised from flush()/close()
      else:
        for item in result.results (in order):
          post_send(to_send[item.index].entry.event, outcome(item))
        store.ack(ids of to_send)
    if released or len(entries) < batch_size: return
```

`outcome(item)`: `accepted`/`duplicate` → `{status, id: item.id}`; `rejected`
→ `{status: rejected, error: ValidationError{status: <the batch response's
HTTP status, 202 or 207>, code: item.error.code, field: item.error.field,
message: item.error.message}}`.

Store failures inside a tick are swallowed by the worker (the next tick
retries); inside `flush()` they raise. Any other unexpected exception in a tick
is caught so the worker survives. The worker's requests are ordinary
`emit_batch` requests ([Every request](#every-request) applies), sent once: `max_retries` does not apply inside a pass.

|language|tick|
|---|---|
|TypeScript|`setTimeout` chain with `unref()`|
|Python, Ruby|a daemon thread waiting on an event/condition with a timeout|
|Go|goroutine + `time.Timer`|
|Rust|`tokio::spawn` + `tokio::select!` on `sleep`/`Notify`|
|Elixir|`Process.send_after(self(), :tick, …)` in the `Santati.Outbox` GenServer|
|PHP|none: `flush()`, `close()` and the shutdown function only|

### Hooks

Hooks run only in outbox passes, never around a synchronous `emit`.

- `pre_send(event: StoredEvent) → StoredEvent | null`: called once per event
  per pass right before its batch request; return the event (possibly
  modified) to send it, null to drop it (acked, never sent, no `post_send`).
  It runs again on every later pass for a released entry — hooks are per
  attempt, on the stored event.
- `post_send(event: StoredEvent, outcome: SendOutcome)`; `SendOutcome{status:
  "accepted"|"duplicate"|"rejected"|"failed", id: string|null, error: <SDK
  error>|null}`. It always receives the **stored (original) event**, not the
  `pre_send` output, and every exception it throws is caught and ignored.

A hook "exception" is a raised exception, a Go panic (recovered around each
hook call) or a Rust panic (`std::panic::catch_unwind` around each hook call).

## Errors

One base type, eight kinds, each with `status` (int|null), `code`
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
|the outbox store refused or failed (`outbox_full`, `store_unavailable`, `closed`), a `pre_send` hook raised or the send raised a non-SDK error (`hook_failed`)|`OutboxError` (status null, `code` as listed)|

Elixir: a tuple, pid or reference in an event raises the JSON encoder's
`Protocol.UndefinedError` from `emit`/`emit_batch` (no request, no retry), and
from a queued `emit` when the term is a map key; it is not a `TransportError`.

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
|emit|`emit(event, *, trail=…, organization_id=…, actor=…, targets=…, metadata=…, data=…, context=…, created_at=…, idempotency_key=…)` (with an `outbox`: `EmitResult(event=None, queued=True)`)|`emit({event, trail?, organizationId?, actor?, targets?, metadata?, data?, context?, createdAt?, idempotencyKey?}): Promise<EmitResult>` (with an `outbox`: `{event: null, queued: true}`)|`Emit(ctx, EventInput) (*EmitResult, error)` (with `WithOutbox`: `Event == nil`, `Queued == true`)|`async fn emit(&self, EventInput) -> Result<EmitResult, Error>` (with `Builder::outbox`: `event: None`, `queued: true`)|`emit(client, map) :: {:ok, %Santati.EmitResult{}} \| {:error, exception}` (with `outbox:`: `%Santati.EmitResult{event: nil, queued: true}`)|`emit(event:, trail: nil, …)` (with `outbox:`: `event` nil, `queued` true)|`emit(array $event): EmitResult` (snake_case keys) (with `outbox:`: `event` null, `queued` true)|
|batch|`emit_batch(events: Sequence[EventInput])` (TypedDict)|`emitBatch(events: EventInput[])`|`EmitBatch(ctx, []EventInput)`|`emit_batch(Vec<EventInput>)`|`emit_batch(client, [map])`|`emit_batch(events)` (hashes, symbol keys)|`emitBatch(array $events)`|
|list|`list(*, trail=…, …, limit=…, cursor=…)`|`list(params?: ListParams)` (camelCase)|`List(ctx, ListParams)`|`list(ListParams)`|`list(client, keyword)`|`list(trail: nil, …, limit: nil, cursor: nil)`|`list(array $params = [])`|
|iterate|`iterate(*, trail=…, …, limit=…) -> Iterator[AuditEvent]`|`iterate(params?): AsyncGenerator<AuditEvent>`|`Iterate(ctx, ListParams) iter.Seq2[*AuditEvent, error]`|`iterate(ListParams) -> impl Stream<Item = Result<AuditEvent, Error>>`|`stream(client, keyword) :: Enumerable.t()` (raises the error exception)|`iterate(trail: nil, …, limit: nil) -> Enumerator`|`iterate(array $params = []): \Generator`|
|errors|`santati.SantatiError` + `ValidationError` … `ApiError`|same class names|`*santati.Error{Kind, Status int (0 = none), Code, Field string, RetryAfter *int, Message}`; `Kind` constants `KindValidation`…`KindAPI` whose string values are the kind names|`santati::Error` enum `Validation`, `Auth`, `NotFound`, `RateLimited`, `Server`, `Transport`, `Api`, each holding `ErrorDetails{status: Option<u16>, code, field: Option<String>, retry_after: Option<u64>, message: String}`|`Santati.ValidationError` … `Santati.ApiError` exceptions with fields `status, code, field, retry_after, message`|`Santati::Error` + `Santati::ValidationError` …|`Santati\Exception\SantatiException` + `ValidationException`, `AuthException`, `NotFoundException`, `RateLimitedException`, `ServerException`, `TransportException`, `ApiException`; getters `getStatus()`, `getErrorCode()`, `getField()`, `getRetryAfter()`|
|options|`Santati(…, outbox=None, batch_size=100, flush_interval_ms=1000, pre_send=None, post_send=None)`|`{outbox?, batchSize?, flushIntervalMs?, preSend?, postSend?}` on `SantatiOptions`|`WithOutbox(OutboxStore)`, `WithBatchSize(int)`, `WithFlushInterval(time.Duration)`, `WithPreSend(PreSendHook)`, `WithPostSend(PostSendHook)`|`Builder::outbox(impl OutboxStore + 'static)`, `.batch_size(usize)`, `.flush_interval(Duration)`, `.pre_send(impl Fn(EventInput) -> Option<EventInput> + Send + Sync + 'static)`, `.post_send(impl Fn(&EventInput, &SendOutcome) + Send + Sync + 'static)`|`Santati.Outbox.start_link(client: %Santati.Client{}, name: …, store: module \| {module, opts}, batch_size: 100, flush_interval_ms: 1000, pre_send: fun/1, post_send: fun/2)`; `{Santati.Outbox, opts}` child spec; `Santati.new(…, outbox: GenServer.server())` makes `Santati.Events.emit/2` store through that server|`Santati::Client.new(…, outbox: nil, batch_size: 100, flush_interval_ms: 1000, pre_send: nil, post_send: nil)` (callables)|`new Client(…, ?OutboxStore $outbox = null, int $batchSize = 100, ?callable $preSend = null, ?callable $postSend = null, bool $finishRequestBeforeFlush = false)`|
|flush|`client.flush() -> None`|`santati.flush(): Promise<void>`|`(*Client).Flush(ctx) error`|`async fn flush(&self) -> Result<(), Error>`|`Santati.Outbox.flush(server, timeout \\ :infinity) :: :ok \| {:error, exception}`; `Santati.flush/1` delegates|`client.flush -> nil`|`$client->flush(): void`|
|close|`client.close()` (stops the worker and flushes first, when an outbox is set)|`santati.close(): Promise<void>`|`(*Client).Close(ctx) error`|`async fn close(&self) -> Result<(), Error>`|`Santati.Outbox.stop(server, timeout \\ :infinity)` → `terminate/2` flushes|`client.close -> nil`|`$client->close(): void`|
|store interface|`santati.OutboxStore` (`typing.Protocol`): `enqueue(event: EventInput) -> None`, `claim(limit: int) -> list[OutboxEntry]`, `ack(ids: Sequence[str]) -> None`, `release(ids: Sequence[str]) -> None`|`interface OutboxStore { enqueue(event: EventInput): void \| Promise<void>; claim(limit: number): OutboxEntry[] \| Promise<OutboxEntry[]>; ack(ids: string[]): void \| Promise<void>; release(ids: string[]): void \| Promise<void> }`|`type OutboxStore interface { Enqueue(ctx, EventInput) error; Claim(ctx, limit int) ([]OutboxEntry, error); Ack(ctx, ids []string) error; Release(ctx, ids []string) error }`|`#[async_trait] pub trait OutboxStore: Send + Sync { async fn enqueue(&self, event: EventInput) -> Result<(), Error>; async fn claim(&self, limit: usize) -> Result<Vec<OutboxEntry>, Error>; async fn ack(&self, ids: Vec<String>) -> Result<(), Error>; async fn release(&self, ids: Vec<String>) -> Result<(), Error>; }`|behaviour `Santati.Outbox.Store`: `init(opts) :: {:ok, state} \| {:error, exception}`, `enqueue(state, map) :: {:ok, state} \| {:error, exception}`, `claim(state, n) :: {:ok, [%Santati.OutboxEntry{}], state} \| {:error, exception}`, `ack(state, ids) :: {:ok, state} \| {:error, exception}`, `release(state, ids) :: {:ok, state} \| {:error, exception}`|duck-typed `enqueue(event)`, `claim(limit)`, `ack(ids)`, `release(ids)`|`interface Santati\Outbox\OutboxStore { enqueue(array $event): void; claim(int $limit): array /* list<OutboxEntry> */; ack(array $ids): void; release(array $ids): void }`|
|entry / outcome|`OutboxEntry(id: str, event: EventInput)`, `SendOutcome(status: str, id: str \| None, error: SantatiError \| None)` (frozen dataclasses)|`OutboxEntry {id: string; event: EventInput}`, `SendOutcome {status: SendStatus; id?: string; error?: SantatiError}`, `type SendStatus = "accepted" \| "duplicate" \| "rejected" \| "failed"`|`OutboxEntry{ID string; Event EventInput}`, `SendOutcome{Status SendStatus; ID string; Err *Error}`, `type SendStatus string` consts `SendAccepted`/`SendDuplicate`/`SendRejected`/`SendFailed`|`OutboxEntry {id: String, event: EventInput}`, `SendOutcome {status: SendStatus, id: Option<String>, error: Option<Error>}`, `enum SendStatus {Accepted, Duplicate, Rejected, Failed}` (`as_str()` like `BatchStatus`)|`%Santati.OutboxEntry{id, event}`, `%Santati.SendOutcome{status: :accepted \| :duplicate \| :rejected \| :failed, id, error}`|`Santati::OutboxEntry`/`Santati::SendOutcome` (`Struct`s, built with keyword arguments)|`final readonly class Santati\Outbox\OutboxEntry(string $id, array $event)`, `final readonly class Santati\SendOutcome(string $status, ?string $id, ?SantatiException $error)`|
|hooks|`pre_send: Callable[[EventInput], EventInput \| None]`, `post_send: Callable[[EventInput, SendOutcome], None]`|`preSend?: (e: EventInput) => EventInput \| null \| undefined \| Promise<…>`, `postSend?: (e: EventInput, o: SendOutcome) => void \| Promise<void>`|`type PreSendHook func(EventInput) (EventInput, bool)` (`false` = drop), `type PostSendHook func(EventInput, SendOutcome)`|see options|`pre_send: (map -> map \| nil)`, `post_send: (map, SendOutcome.t -> any)`|callables|callables|
|memory store|`santati.MemoryOutbox(max_pending=10000)`|`new MemoryOutbox({maxPending?: number})`|`santati.NewMemoryOutbox(maxPending int) (*MemoryOutbox, error)`|`santati::MemoryOutbox::new(max_pending: usize) -> Result<MemoryOutbox, Error>`|`Santati.Outbox.Memory`, opts `max_pending:`|`Santati::MemoryOutbox.new(max_pending: 10_000)`|`new Santati\Outbox\MemoryOutbox(int $maxPending = 10000)`|
|redis store|`santati.outbox.redis.RedisOutbox(client: redis.Redis, *, key="santati:outbox", visibility_ms=60000)`, extra `santati[redis]` (`redis>=5`)|`import { RedisOutbox } from "@santati/node/redis"`; `new RedisOutbox(client: ioredis.Redis, {key?, visibilityMs?})`; optional peer dep `ioredis>=5`|`github.com/jamescarr/santati-sdks/go/redisoutbox`: `redisoutbox.New(client redis.Cmdable, opts ...redisoutbox.Option) *redisoutbox.Store`; `WithKey(string)`, `WithVisibility(time.Duration)`; dep `github.com/redis/go-redis/v9`|feature `redis`: `santati::RedisOutbox::new(conn: redis::aio::ConnectionManager)`, `.key(impl Into<String>)`, `.visibility(Duration)`|`Santati.Outbox.Redis`, opts `conn:` (Redix connection or name, required), `key:`, `visibility_ms:`; `{:redix, "~> 1.5", optional: true}`|`require "santati/outbox/redis"`; `Santati::RedisOutbox.new(redis, key: "santati:outbox", visibility_ms: 60_000)` (`redis` gem ≥ 5, not a gemspec dependency)|`new Santati\Outbox\RedisOutbox(\Predis\ClientInterface $client, string $key = 'santati:outbox', int $visibilityMs = 60000)`; composer `suggest` `predis/predis`|
|wire codec (custom stores)|the stored dict is the wire JSON|`envelopeToWire(e: EventInput): Record<string, unknown>`, `envelopeFromWire(json: unknown): EventInput`|`json.Marshal`/`json.Unmarshal` of `EventInput` (json tags)|`serde_json` on `EventInput` (`Serialize`/`Deserialize`)|the stored map is the wire JSON (string keys)|`JSON.generate(hash)` / `JSON.parse(s, symbolize_names: true)`|`json_encode` / `json_decode($s, true)`|
|outbox error|`santati.OutboxError`|`OutboxError`|`KindOutbox Kind = "OutboxError"`|`Error::Outbox(ErrorDetails)`, `ErrorKind::Outbox` (`as_str` → `"OutboxError"`)|`Santati.OutboxError`|`Santati::OutboxError`|`Santati\Exception\OutboxException`|

UUIDv4 generation: Python `uuid.uuid4()`, TypeScript `crypto.randomUUID()`, Go
16 bytes from `crypto/rand` with the version/variant bits set, Rust
`uuid::Uuid::new_v4()`, Elixir `:crypto.strong_rand_bytes(16)` with bits set,
Ruby `SecureRandom.uuid`, PHP `random_bytes(16)` with bits set; always
lowercase `8-4-4-4-12`.

## Generated cores

Each facade sits on the generated core from `generator/config.json`, used
through one configuration/client object per facade client — never the
generators' process-wide defaults — and validates locally before constructing
any generated model.

The generation profile strips `minLength`/`maxLength` from every string schema.
They are documentation (ingest clips an over-long value or defaults an empty
one instead of rejecting it), and a core that enforces them would reject input
the server accepts, or fail to decode a response that exceeds one. So no core
enforces a length bound and every SDK forwards what the server accepts. A
generated model that still rejects a request (a missing required member, say)
surfaces as a `ValidationError` with `status` null.

The profile keeps the spec's `ApiKeyAuth` scheme, so the python, typescript, go,
ruby and php cores apply `Authorization: Api-Key <api_key>` themselves from the
key and the `Api-Key` prefix the facade configures. The elixir and rust
facades, whose generated layer carries no auth, set the header on their own
transport. Either way the vectors assert it on every request.
