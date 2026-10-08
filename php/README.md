# santati/santati-php

The official PHP SDK for the [Santati](https://santati.io) audit-log API.

Requires PHP 8.2 or newer; built on the Guzzle client.

```bash
composer require santati/santati-php
```

## Quickstart

```php
require 'vendor/autoload.php';

use Santati\Client;

$client = new Client(apiKey: 'sat_sk_…', trail: 'billing');

// Emit one event. The idempotency key is generated for you.
$result = $client->events->emit([
    'event' => 'invoice.voided',
    'organization_id' => 'org_acme',
    'actor' => ['type' => 'user', 'id' => 'usr_123'],
    'targets' => [['type' => 'invoice', 'id' => 'inv_555']],
    'metadata' => ['plan' => 'pro'],
    'data' => ['amount_cents' => 4200],
]);

printf("%s (duplicate: %s)\n", $result->event->getId(), $result->duplicate ? 'yes' : 'no');

// Or a batch, one request for up to 500 events.
$batch = $client->events->emitBatch([
    ['event' => 'invoice.voided', 'idempotency_key' => 'void-1'],
    ['event' => 'invoice.paid', 'trail' => 'payments'],
]);

printf("%d accepted, %d rejected\n", $batch->accepted, $batch->rejected);

// List one page …
$page = $client->events->list(['trail' => 'billing', 'limit' => 50]);

foreach ($page->results as $event) {
    printf("%s %s\n", $event->getCreatedAt(), $event->getEvent());
}

// … or walk every page lazily.
foreach ($client->events->iterate(['trail' => 'billing', 'limit' => 200]) as $event) {
    printf("%s %s\n", $event->getCreatedAt(), $event->getEvent());
}

// Pin an emit to one published schema version of its action.
$client->events->emit(['event' => 'invoice.voided', 'schema_version' => 2]);

// Manage event definitions and their JSON Schema versions.
$client->schemas->createDefinition(['action' => 'invoice.voided', 'allowed_target_types' => ['invoice']]);
$draft = $client->schemas->createVersion('invoice.voided', ['type' => 'object']);
$client->schemas->updateVersion('invoice.voided', $draft->schemaVersion->getVersion(), ['type' => 'object'], $draft->etag);
$client->schemas->publishVersion('invoice.voided', $draft->schemaVersion->getVersion());
```

PHP cannot tell an empty JSON object from an empty array: an empty object nested
inside a schema document must be passed as `\stdClass`. `createVersion()` is
sent once, because a repeat would create a second draft.

```php
```

## Errors

Every failure is a `Santati\Exception\SantatiException` with `getStatus()`,
`getErrorCode()`, `getField()` and `getRetryAfter()`: `ValidationException`
(local validation, or HTTP 400/413/422), `SchemaValidationException` (a
`ValidationException` for an HTTP 400/413/422 whose code is
`schema_validation_failed`: the event broke the action's schema, named a
disallowed target type, or pinned an unusable `schema_version`),
`AuthException` (401/403), `NotFoundException` (404), `RateLimitedException`
(429), `ServerException` (5xx), `TransportException` (no response: refused,
DNS, TLS, timeout) and `ApiException` (anything else, including an
undecodable 2xx body).

```php
use Santati\Exception\RateLimitedException;

try {
    $client->events->emit(['event' => 'invoice.voided']);
} catch (RateLimitedException $e) {
    echo $e->getRetryAfter(); // seconds, from the Retry-After header — or null
}
```

Transport failures, 500/502/503/504 and 429 (unless the code is
`quota_exceeded`) are retried up to `maxRetries` times with the same body and
the same idempotency keys; `Retry-After` is honoured unless it exceeds
`maxBackoffMs`.

## Client options

```php
$client = new Client(
    apiKey: 'sat_sk_…',                    // required, non-empty
    baseUrl: 'https://api.santati.io',     // a trailing slash is dropped; a path prefix is kept
    trail: 'billing',                      // default trail for emits, never applied to reads
    timeoutMs: 10000,                      // per attempt
    maxRetries: 2,
    initialBackoffMs: 250,
    maxBackoffMs: 8000,
    headers: ['x-source' => 'billing-job'], // extra headers on every request
    outbox: null,                          // when set, events->emit() queues here instead of sending
);
```

The read models themselves come from the generated core:
`Santati\Core\Model\AuditEvent`, `…\EventActor` and `…\EventTarget` have a
getter per wire field (`getId()`, `getEvent()`, `getCreatedAt()`,
`getSchemaVersion()`, …). `EmitResult`, `BatchResult`, `BatchItem`,
`BatchItemError`, `EventPage`, `DefinitionPage`, `SchemaVersionPage` and
`SchemaVersionResult` are the facade's own value objects.

Emit and batch inputs are plain arrays with the wire's `snake_case` keys; every
absent or `null` member is omitted from the request. `schema_version` is an
integer (a non-integer raises a `ValidationException`) that pins the event to
one published schema version of its action. `list()` and `iterate()`
accept the audit-event filters (`trail`, `event`, `event_prefix`,
`organization_id`, `actor_id`, `actor_type`, `target_type`, `target_id`,
`created_after`, `created_before`, `q`, `sort`, `limit`, `cursor`) as
`[$name => $value]`; unknown names raise a `ValidationException`.

## Outbox

Without an `outbox`, `events->emit()` sends the event and returns the stored
one. With one, `emit()` validates like a plain `emit()`, stores the event in the
outbox and returns at once with `queued` true and a null `event`, without making
a request. `flush()` sends what is stored in batches (`batchSize`, default 100)
through `events->emitBatch()`; `close()` flushes and closes the client, and the
end of the script flushes anything still pending. PHP has no background worker,
so nothing is sent before one of those. Each batch is sent once, without the
client's retries: a retryable failure leaves the batch in the outbox for the
next `flush()`, so one flush waits at most one `timeoutMs` per batch.

Under PHP-FPM, pass `finishRequestBeforeFlush: true` and the end-of-script flush
first calls `fastcgi_finish_request()`, so the response reaches the client
before the flush runs. `flush()` and `close()` called explicitly never do.

```php
$client = new Client(
    apiKey: 'sat_sk_…',
    trail: 'billing',
    outbox: new Santati\Outbox\MemoryOutbox(),
    postSend: fn (array $event, Santati\SendOutcome $o) => error_log($event['event'] . ' ' . $o->status),
);

$result = $client->events->emit(['event' => 'invoice.voided', 'organization_id' => 'org_acme']);
// $result->queued === true, $result->event === null

$client->close();
```

`preSend` receives each stored event and returns it (possibly modified) or
`null` to drop it. Events left in a `MemoryOutbox` are lost when the
process dies, including a batch `close()` or the shutdown flush could not send because the
endpoint was down; pass `outbox: new Santati\Outbox\RedisOutbox($predis)` (a
`Predis\ClientInterface`; install it with `composer require predis/predis`) to
keep them in a Redis Stream that any Santati SDK can drain.

## License

Apache-2.0.
