# Changelog

All notable changes to this gem are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- An opt-in outbox: with `outbox:` (`Santati::MemoryOutbox`, or Redis Streams via `require "santati/outbox/redis"` and the `redis` gem) `events.emit` stores the event and returns at once with `EmitResult#queued` true; a background worker sends the outbox in batches of `batch_size` every `flush_interval_ms`, with `pre_send`/`post_send` hooks, `flush()`/`close()`. A queued `emit` stores a copy of the event; a batch that fails with anything but a `Santati::Error` (e.g. a malformed `pre_send` result) is reported to `post_send` as `OutboxError` `hook_failed` and dropped, never raised.

### Changed

- `EmitResult` has a new `queued` field, and its `event` is `nil` for a queued emit.

## [0.1.0] - 2026-10-01

### Added

- Emit one audit event or a batch, with generated idempotency keys and retries.
- List audit events with filters and cursor pagination, and iterate across pages.

[Unreleased]: https://github.com/jamescarr/santati-sdks/compare/ruby-v0.1.0...HEAD
[0.1.0]: https://github.com/jamescarr/santati-sdks/releases/tag/ruby-v0.1.0
