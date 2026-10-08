# Changelog

All notable changes to this package are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this package
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- `$client->schemas`: event definitions (`listDefinitions`, `iterateDefinitions`, `getDefinition`, `createDefinition`, `updateDefinition`, `deleteDefinition`), their schema versions (`listVersions`, `iterateVersions`, `getVersion`, `createVersion`, `updateVersion` with `$ifMatch`, `deleteVersion`, `publishVersion`, `deprecateVersion`, `checkSchema`) and the standard packs (`listStandardPacks`, `installStandardPacks`), with the `DefinitionPage`, `SchemaVersionPage` and `SchemaVersionResult` value objects. `createVersion()` is sent once, never retried.
- `schema_version` (an integer) on `events->emit()`, on `emitBatch()` items and on queued events, to pin an emit to one published version of the action's schema.
- `Santati\Exception\SchemaValidationException`, a subclass of `ValidationException`.

### Changed

- A 400, 413 or 422 whose code is `schema_validation_failed` (and a rejected outbox item with that code) now throws `SchemaValidationException`; existing `catch (ValidationException)` blocks still catch it.
- `ValidationException` is no longer `final`.

## [0.2.0] - 2026-10-06

### Added

- An opt-in outbox: with `outbox:` (`MemoryOutbox`, or a Redis adapter via `predis/predis`) `events->emit()` stores the event and returns at once with `EmitResult::$queued` true; `flush()`/`close()` (and the end of the script) send the outbox in batches of `batchSize` through `emitBatch()`, with `preSend`/`postSend` hooks. A batch that fails with anything but a `SantatiException` (e.g. a malformed `preSend` result) is reported to `postSend` as `OutboxException` `hook_failed` and dropped, never thrown. Each batch is sent once, without `maxRetries`: a retryable failure releases it for the next pass. With `finishRequestBeforeFlush: true` the end-of-script flush first calls `fastcgi_finish_request()` under PHP-FPM, so it does not delay the response.

### Changed

- `EmitResult` has a new `queued` field, and its `event` is null for a queued emit.

## [0.1.0] - 2026-10-01

### Added

- Emit one audit event or a batch, with generated idempotency keys and retries.
- List audit events with filters and cursor pagination, and iterate across pages.

[Unreleased]: https://github.com/jamescarr/santati-sdks/compare/php-v0.2.0...HEAD
[0.2.0]: https://github.com/jamescarr/santati-sdks/compare/php-v0.1.0...php-v0.2.0
[0.1.0]: https://github.com/jamescarr/santati-sdks/releases/tag/php-v0.1.0
