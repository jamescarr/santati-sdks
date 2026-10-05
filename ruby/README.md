# santati

Official Ruby SDK for the [Santati](https://api.santati.io) audit-log API:
emit audit events and read them back with a team API key (`sat_sk_…`).

## Install

```sh
gem install santati
```

or, in a `Gemfile`:

```ruby
gem "santati"
```

Ruby 3.2 or newer.

## Quickstart

```ruby
require "santati"

client = Santati::Client.new(api_key: ENV.fetch("SANTATI_API_KEY"), trail: "billing")

# Emit one event. Without a trail on the event, the client's default trail is used.
result = client.events.emit(
  event: "invoice.voided",
  organization_id: "org_acme",
  actor: {type: "user", id: "usr_123", name: "Dana Ortiz"},
  targets: [{type: "invoice", id: "inv_555"}],
  data: {amount_cents: 4200}
)
result.event.id         # => "01J9Z6K3M4QX8RT2VN5B7HCWDA"
result.duplicate        # => false; true when the server replayed an earlier request
result.idempotency_key  # => a generated UUIDv4 when the caller sent none

# Emit a batch; a 207 comes back as a result, not an exception.
batch = client.events.emit_batch([
  {event: "invoice.voided"},
  {event: "invoice.paid", trail: "payments"}
])
batch.accepted # => 2
batch.rejected # => 0
batch.results.map(&:status) # => ["accepted", "duplicate"]

# Read one page.
page = client.events.list(trail: "billing", event_prefix: "invoice.", limit: 50)
page.results.first.event  # => "invoice.voided"
page.next_cursor          # => "cD00ODY=" or nil

# Or walk every page lazily.
client.events.iterate(trail: "billing").each { |event| puts event.id }
```

`emit` accepts `event:`, `trail:`, `organization_id:`, `actor:`, `targets:`,
`metadata:`, `data:`, `context:`, `created_at:` and `idempotency_key:`.
`list` and `iterate` accept the read filters (`trail:`, `event:`,
`event_prefix:`, `organization_id:`, `actor_id:`, `actor_type:`,
`target_type:`, `target_id:`, `created_after:`, `created_before:`, `q:`,
`sort:`, `limit:`; `list` also takes `cursor:`). The client's default `trail`
is never applied to reads.

The read models are the generated `SantatiCore::AuditEvent`,
`Santati::EventActor` and `Santati::EventTarget`. The event's sha256 chain hash
is read as `AuditEvent#chain_hash` (`hash` is `Object#hash`), and
`schema_version` is `nil` for events that predate the field.

## Client options

```ruby
Santati::Client.new(
  api_key: "sat_sk_…",                  # required, non-empty
  base_url: "https://api.santati.io",   # trailing "/" removed; may carry a path prefix
  trail: "billing",                     # default trail for emits
  timeout_ms: 10_000,                   # per attempt
  max_retries: 2,                       # transport, 500/502/503/504 and 429 (unless quota_exceeded)
  initial_backoff_ms: 250,
  max_backoff_ms: 8000,
  headers: {},                          # extra headers on every request
  outbox: nil,                          # when set, events.emit queues here instead of sending
  batch_size: 100,                      # envelopes per outbox request, 1..500
  flush_interval_ms: 1000,              # the worker's tick, > 0
  pre_send: nil,                        # ->(event) { event or nil to drop }
  post_send: nil                        # ->(event, outcome) { … }
)
```

Every request carries `Authorization: Api-Key <api_key>` and
`User-Agent: santati-ruby/<version>`; redirects are never followed, so the key
is never replayed to another host.

## Outbox

Without an `outbox:`, `events.emit` sends the event and returns the stored one.
With one, `emit` validates like a plain `emit`, stores the event in the outbox
and returns at once with `queued` true and a nil `event`, without making a
request; a background thread sends the outbox in batches. `close` stops the
thread and flushes what is pending.

```ruby
client = Santati::Client.new(
  api_key: ENV.fetch("SANTATI_API_KEY"),
  trail: "billing",
  outbox: Santati::MemoryOutbox.new,
  post_send: ->(event, outcome) { warn "#{event[:event]}: #{outcome.status}" }
)

result = client.events.emit(event: "invoice.voided", organization_id: "org_acme")
result.queued # => true
client.close # drains the outbox; a queued `emit` raises Santati::OutboxError afterwards
```

`client.flush` runs one send pass now. `Santati::MemoryOutbox.new` keeps up to
10,000 events in memory. `close` sends each batch once; if the endpoint is down, what it
could not send stays in the store and, in memory, is lost with the process. To survive
restarts, or to share one outbox between
processes, use the Redis adapter (Redis Streams); add `gem "redis", ">= 5"` to
your Gemfile, then:

```ruby
require "santati/outbox/redis"

outbox = Santati::RedisOutbox.new(Redis.new(url: ENV.fetch("REDIS_URL")))
client = Santati::Client.new(api_key: ENV.fetch("SANTATI_API_KEY"), trail: "billing", outbox: outbox)
```

A store failure raises `Santati::OutboxError` (`code`: `outbox_full`,
`store_unavailable` or `closed`).

## Errors

Every failure raises a `Santati::Error` subclass with `status`, `code`,
`field`, `retry_after` and `message`:

|cause|class|
|---|---|
|local validation|`Santati::ValidationError`|
|HTTP 400, 413, 422|`Santati::ValidationError`|
|HTTP 401, 403|`Santati::AuthError`|
|HTTP 404|`Santati::NotFoundError`|
|HTTP 429|`Santati::RateLimitedError`|
|HTTP 500–599|`Santati::ServerError`|
|no HTTP response|`Santati::TransportError`|
|outbox store failure, `closed`, or a raising `pre_send`|`Santati::OutboxError`|
|anything else|`Santati::ApiError`|

```ruby
begin
  client.events.emit(event: "invoice.voided")
rescue Santati::RateLimitedError => e
  sleep(e.retry_after) if e.retry_after
  retry
rescue Santati::Error => e
  warn e.message
end
```
