# Changelog

All notable changes to this package are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.2.0] - 2026-10-06

### Added

- An opt-in outbox: with `WithOutbox` (`NewMemoryOutbox`, or a Redis adapter via `redisoutbox`) `Events.Emit` stores the event and returns at once with `EmitResult.Queued` true; a background worker sends the outbox in batches of `WithBatchSize` every `WithFlushInterval` through `EmitBatch`, with `WithPreSend`/`WithPostSend` hooks, `Flush()`/`Close()`. A queued `Emit` stores a copy of the event's maps and slices. Each batch is sent once, without `WithMaxRetries`: a retryable failure releases it for the next pass. `Close(ctx)` cancels a worker send in progress (its batch is re-sent by the final flush) and runs the final flush under `ctx`, returning `ctx.Err()` if it ends first.

### Changed

- `EmitResult` has a new `Queued` field; its `Event` is nil for a queued emit.

## [0.1.0] - 2026-10-01

### Added

- Emit one audit event or a batch, with generated idempotency keys and retries.
- List audit events with filters and cursor pagination, and iterate across pages.

[Unreleased]: https://github.com/jamescarr/santati-sdks/compare/go/v0.2.0...HEAD
[0.2.0]: https://github.com/jamescarr/santati-sdks/compare/go/v0.1.0...go/v0.2.0
[0.1.0]: https://github.com/jamescarr/santati-sdks/releases/tag/go/v0.1.0
