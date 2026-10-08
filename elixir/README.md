# santati

Official Elixir SDK for the [Santati](https://github.com/jamescarr/santati-sdks) audit-log API:
emit one audit event or a batch, then list and iterate over what was recorded.
The wire surface is shared with the Python, TypeScript, Go, Rust, Ruby and PHP
SDKs and pinned by this repository's conformance suite.

## Installation

Add `santati` to your dependencies:

```elixir
def deps do
  [
    {:santati, "~> 0.1"}
  ]
end
```

## Quickstart

```elixir
{:ok, client} =
  Santati.new(
    api_key: System.fetch_env!("SANTATI_API_KEY"),
    trail: "billing"
  )

# Emit one event. The trail resolves from the event first, then the client.
{:ok, result} =
  Santati.Events.emit(client, %{
    event: "invoice.voided",
    organization_id: "org_acme",
    actor: %{type: "user", id: "usr_123", name: "Dana Ortiz"},
    targets: [%{type: "invoice", id: "inv_555"}],
    data: %{amount_cents: 4200, currency: "usd"}
  })

result.event.id
#=> "01J9Z6K3M4QX8RT2VN5B7HCWDA"
```

An emit without an `idempotency_key` gets a freshly generated UUIDv4, reused
verbatim by every retry of that emit — so a retried request can never index the
event twice. A replay answers with `duplicate: true`.

```elixir
# Emit a batch. Items that carry their own key keep it.
{:ok, batch} =
  Santati.Events.emit_batch(client, [
    %{event: "invoice.voided"},
    %{event: "invoice.paid", trail: "payments"}
  ])

batch.accepted
#=> 2

batch.results
#=> [%Santati.BatchItem{index: 0, status: "accepted", ...}, ...]
```

A partially rejected batch answers `207` and is still an `{:ok, batch}`: read
`batch.rejected` and each item's `error` (`:code`, `:message`, `:field`).

```elixir
# List one page, newest first, and read the cursor of the next one.
{:ok, page} =
  Santati.Events.list(client,
    trail: "billing",
    event: "invoice.voided",
    created_after: "2026-09-01T00:00:00+00:00",
    limit: 50
  )

page.results
#=> [%SantatiCore.Model.AuditEvent{}, ...]

page.next_cursor
#=> "cD00ODY=" | nil
```

The client's default trail is never applied to reads, and only the parameters
you pass are sent.

```elixir
# Iterate every matching event, following next cursors lazily.
client
|> Santati.Events.stream(trail: "billing", limit: 500)
|> Stream.map(& &1.event)
|> Enum.take(10)
```

`stream/2` yields `SantatiCore.Model.AuditEvent` structs and raises the error of
the page that failed — events from earlier pages have already been yielded.

Pin an emit to one published schema version of its action with the event's
`schema_version` (an integer, forwarded unchanged):

```elixir
Santati.Events.emit(client, %{event: "invoice.voided", schema_version: 2})
```

## Schemas

`Santati.Schemas` manages event definitions, their JSON Schema versions and the
standard packs. Every function takes the client first and answers
`{:ok, value}` or `{:error, exception}`:

```elixir
{:ok, _definition} = Santati.Schemas.create_definition(client, %{action: "invoice.voided"})
{:ok, draft} = Santati.Schemas.create_version(client, "invoice.voided", %{"type" => "object"})

{:ok, _updated} =
  Santati.Schemas.update_version(
    client,
    "invoice.voided",
    draft.schema_version.version,
    %{"type" => "object"},
    if_match: draft.etag
  )

{:ok, _published} = Santati.Schemas.publish_version(client, "invoice.voided", draft.schema_version.version)
```

`create_version/3` is sent once, because a repeat would create a second draft.

## Outbox

Without an outbox, `Santati.Events.emit/2` sends the event. Pass `outbox:` — a
`Santati.Outbox` process, by pid or registered name — to `Santati.new/1` and
`emit/2` is fire-and-forget: it validates the event, stores it in the outbox and
answers `{:ok, %Santati.EmitResult{queued: true, event: nil}}` with the
idempotency key. The `Santati.Outbox` process sends the outbox in batches in the
background; `pre_send` and `post_send` hooks observe every event, and stopping
the process flushes it.

```elixir
{:ok, _pid} = Santati.Outbox.start_link(client: client, name: MyApp.Santati)
{:ok, client} = Santati.new(api_key: "sat_sk_...", trail: "billing", outbox: MyApp.Santati)

{:ok, %Santati.EmitResult{queued: true}} =
  Santati.Events.emit(client, %{event: "invoice.voided"})

Santati.Outbox.stop(MyApp.Santati)
```

Events are held in memory by default. Stopping the outbox sends each batch once; if the
endpoint is down, what it could not send stays in the store and, in memory, is lost with
the process. For a durable outbox use the Redis adapter:
add `{:redix, "~> 1.5"}` to your dependencies and pass
`store: {Santati.Outbox.Redis, conn: redix_conn}`.

## Errors

`Santati.new/1`, `Santati.Events.emit/2`, `emit_batch/2` and `list/2` answer
`{:ok, result}` or `{:error, exception}`. Every exception carries the same five
attributes:

| kind | when |
|---|---|
| `Santati.ValidationError` | local validation (`status` is `nil`), or HTTP 400, 413, 422 (any code but `schema_validation_failed`) |
| `Santati.SchemaValidationError` | HTTP 400, 413, 422 whose code is `schema_validation_failed`: the event broke the action's schema, named a disallowed target type, or pinned an unusable `schema_version` |
| `Santati.AuthError` | HTTP 401, 403 |
| `Santati.NotFoundError` | HTTP 404 |
| `Santati.RateLimitedError` | HTTP 429 |
| `Santati.ServerError` | HTTP 5xx |
| `Santati.TransportError` | no response at all: refused, DNS, TLS, timeout (`status` is `nil`) |
| `Santati.ApiError` | any other status, an unexpected 2xx, or an undecodable 2xx body |

An Elixir exception cannot subclass another, so `%Santati.ValidationError{}` does
not match a `Santati.SchemaValidationError`: match both when you handle
validation failures.

```elixir
case Santati.Events.emit(client, %{event: "invoice.voided"}) do
  {:ok, result} -> result.event.id
  {:error, %Santati.ValidationError{field: field}} -> {:invalid, field}
  {:error, %Santati.AuthError{}} -> :unauthorized
  {:error, %Santati.RateLimitedError{retry_after: seconds}} -> {:wait, seconds}
  {:error, error} -> {:failed, Exception.message(error)}
end
```

An event JSON cannot represent (a tuple, a pid or a reference anywhere in it)
is a bug in the caller, not a failed request: `emit/2` and `emit_batch/2` raise
`Protocol.UndefinedError`, and so does a queued `Santati.Events.emit/2` when the
bad term is a map key. A bad value is accepted by a queued `emit/2` with the
memory store; the outbox then reports its batch to `post_send` as
`Santati.OutboxError` `hook_failed` and drops it. The Redis store refuses it in a
queued `emit/2` as `Santati.OutboxError` `store_unavailable`.

## Retries

An emit, a batch and a list retry transport failures, `500`/`502`/`503`/`504`
and `429` (unless the code is `quota_exceeded`) up to `max_retries` times, with
the same body and the same idempotency keys. A `Retry-After` header is honoured
unless it is longer than `max_backoff_ms`, in which case the error is raised
immediately. See `Santati.new/1` for the `timeout_ms`, `max_retries`,
`initial_backoff_ms` and `max_backoff_ms` options.

## License

Apache-2.0. See `LICENSE`.
