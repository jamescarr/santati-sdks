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
  headers: {}                           # extra headers on every request
)
```

Every request carries `Authorization: Api-Key <api_key>` and
`User-Agent: santati-ruby/<version>`; redirects are never followed, so the key
is never replayed to another host.

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
