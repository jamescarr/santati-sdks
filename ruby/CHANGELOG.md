# Changelog

All notable changes to this gem are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.3.0] - 2026-10-08

### Added

- `client.schemas`: event definitions (`list_definitions`, `iterate_definitions`, `get_definition`, `create_definition`, `update_definition`, `delete_definition`), their schema versions (`list_versions`, `iterate_versions`, `get_version`, `create_version`, `update_version` with `if_match:`, `delete_version`, `publish_version`, `deprecate_version`, `check_schema`) and the standard packs (`list_standard_packs`, `install_standard_packs`). `create_version` is sent once, never retried.
- `schema_version:` on `events.emit`, on `emit_batch` items and on queued events, to pin an emit to one published version of the action's schema.
- `Santati::SchemaValidationError`, a subclass of `ValidationError`.

### Changed

- A 400, 413 or 422 whose code is `schema_validation_failed` (and a rejected outbox item with that code) now raises `Santati::SchemaValidationError`; existing `rescue Santati::ValidationError` clauses still catch it.

## [0.2.0] - 2026-10-06

### Added

- An opt-in outbox: with `outbox:` (`Santati::MemoryOutbox`, or Redis Streams via `require "santati/outbox/redis"` and the `redis` gem) `events.emit` stores the event and returns at once with `EmitResult#queued` true; a background worker sends the outbox in batches of `batch_size` every `flush_interval_ms`, with `pre_send`/`post_send` hooks, `flush()`/`close()`. A queued `emit` stores a copy of the event; a batch that fails with anything but a `Santati::Error` (e.g. a malformed `pre_send` result) is reported to `post_send` as `OutboxError` `hook_failed` and dropped, never raised. Each batch is sent once, without `max_retries`: a retryable failure releases it for the next pass.

### Changed

- `EmitResult` has a new `queued` field, and its `event` is `nil` for a queued emit.

## [0.1.0] - 2026-10-01

### Added

- Emit one audit event or a batch, with generated idempotency keys and retries.
- List audit events with filters and cursor pagination, and iterate across pages.

[Unreleased]: https://github.com/jamescarr/santati-sdks/compare/ruby-v0.3.0...HEAD
[0.3.0]: https://github.com/jamescarr/santati-sdks/compare/ruby-v0.2.0...ruby-v0.3.0
[0.2.0]: https://github.com/jamescarr/santati-sdks/compare/ruby-v0.1.0...ruby-v0.2.0
[0.1.0]: https://github.com/jamescarr/santati-sdks/releases/tag/ruby-v0.1.0
