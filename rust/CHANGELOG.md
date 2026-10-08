# Changelog

All notable changes to the `santati` crate are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.3.0] - 2026-10-08

### Added

- `Santati::schemas()`: event definitions (`list_definitions`, `iterate_definitions`, `get_definition`, `create_definition`, `update_definition`, `delete_definition`), their schema versions (`list_versions`, `iterate_versions`, `get_version`, `create_version`, `update_version` with `if_match`, `delete_version`, `publish_version`, `deprecate_version`, `check_schema`) and the standard packs (`list_standard_packs`, `install_standard_packs`). `create_version` is sent once, never retried.
- `EventInput::schema_version` on `Events::emit`, `emit_batch` items and queued events, to pin an emit to one published version of the action's schema.
- `ErrorKind::SchemaValidation` and `Error::SchemaValidation`.

### Changed

- A 400, 413 or 422 whose code is `schema_validation_failed` (and a rejected outbox item with that code) is now `Error::SchemaValidation` instead of `Error::Validation`; callers that match only `Validation` must match the new kind too, and `ErrorKind`/`Error` (which are not `#[non_exhaustive]`) gain a variant, so exhaustive matches need a new arm.
- `EventInput` gains the public field `schema_version`; code that builds it with struct literals needs `..Default::default()`.

## [0.2.0] - 2026-10-06

### Added

- An opt-in outbox: with `Builder::outbox` (`MemoryOutbox`, or Redis Streams via the `redis` feature) `Events::emit` stores the event and returns at once with `EmitResult::queued` true; a background task sends the outbox in batches of `batch_size` every `flush_interval` through `emit_batch`, with `pre_send`/`post_send` hooks, `flush()`/`close()`. Each batch is sent once, without `max_retries`: a retryable failure releases it for the next pass.

### Changed

- `EmitResult` has a new `queued` field, and its `event` is now an `Option<AuditEvent>`, `None` for a queued emit.
- The minimum supported Rust version is now 1.88 (from 1.85), the MSRV of the
  `redis` crate behind the new `redis` feature.

## [0.1.0] - 2026-10-01

### Added

- Emit one audit event or a batch, with generated idempotency keys and retries.
- List audit events with filters and cursor pagination, and iterate across pages.

[Unreleased]: https://github.com/jamescarr/santati-sdks/compare/rust-v0.3.0...HEAD
[0.3.0]: https://github.com/jamescarr/santati-sdks/compare/rust-v0.2.0...rust-v0.3.0
[0.2.0]: https://github.com/jamescarr/santati-sdks/compare/rust-v0.1.0...rust-v0.2.0
[0.1.0]: https://github.com/jamescarr/santati-sdks/releases/tag/rust-v0.1.0
