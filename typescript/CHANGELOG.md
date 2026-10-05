# Changelog

All notable changes to `@santati/node` are documented here. Format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); this project follows
[Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- An opt-in outbox: with `outbox` (`MemoryOutbox`, or a Redis adapter via `@santati/node/redis` with the optional `ioredis` peer dependency) `events.emit` stores the event and returns at once with `EmitResult.queued` true; a background worker sends the outbox in batches of `batchSize` every `flushIntervalMs` through `emitBatch`, with `preSend`/`postSend` hooks, `flush()`/`close()`. A queued `emit` stores a copy of the event; a batch that fails with anything but a `SantatiError` (e.g. a malformed `preSend` result) is reported to `postSend` as `OutboxError` `hook_failed` and dropped, never thrown. Each batch is sent once, without `maxRetries`: a retryable failure releases it for the next pass.

### Changed

- `EmitResult` has a new `queued` field, and its `event` is `null` for a queued emit.

## [0.1.0] - 2026-10-01

### Added

- Emit one audit event or a batch, with generated idempotency keys and retries.
- List audit events with filters and cursor pagination, and iterate across pages.

[Unreleased]: https://github.com/jamescarr/santati-sdks/compare/typescript-v0.1.0...HEAD
[0.1.0]: https://github.com/jamescarr/santati-sdks/releases/tag/typescript-v0.1.0
