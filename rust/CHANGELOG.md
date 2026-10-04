# Changelog

All notable changes to the `santati` crate are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- An opt-in outbox: with `Builder::outbox` (`MemoryOutbox`, or Redis Streams via the `redis` feature) `Events::emit` stores the event and returns at once with `EmitResult::queued` true; a background task sends the outbox in batches of `batch_size` every `flush_interval` through `emit_batch`, with `pre_send`/`post_send` hooks, `flush()`/`close()`.

### Changed

- `EmitResult` has a new `queued` field, and its `event` is now an `Option<AuditEvent>`, `None` for a queued emit.
- The minimum supported Rust version is now 1.88 (from 1.85), the MSRV of the
  `redis` crate behind the new `redis` feature.

## [0.1.0] - 2026-10-01

### Added

- Emit one audit event or a batch, with generated idempotency keys and retries.
- List audit events with filters and cursor pagination, and iterate across pages.

[Unreleased]: https://github.com/jamescarr/santati-sdks/compare/rust-v0.1.0...HEAD
[0.1.0]: https://github.com/jamescarr/santati-sdks/releases/tag/rust-v0.1.0
