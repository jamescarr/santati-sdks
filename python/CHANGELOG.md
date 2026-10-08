# Changelog

All notable changes to this package are documented here. The format is based
on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- `client.schemas`: event definitions (`list_definitions`, `iterate_definitions`,
  `get_definition`, `create_definition`, `update_definition`,
  `delete_definition`), their schema versions (`list_versions`,
  `iterate_versions`, `get_version`, `create_version`, `update_version` with
  `if_match`, `delete_version`, `publish_version`, `deprecate_version`,
  `check_schema`) and the standard packs (`list_standard_packs`,
  `install_standard_packs`). `create_version` is sent once, never retried.
- `schema_version=` on `events.emit`, on `emit_batch` items and on queued
  events, to pin an emit to one published version of the action's schema.
- `SchemaValidationError`, a subclass of `ValidationError`.

### Changed

- A 400, 413 or 422 whose code is `schema_validation_failed` (and a rejected
  outbox item with that code) now raises `SchemaValidationError`; existing
  `except ValidationError` handlers still catch it.

## [0.2.0] - 2026-10-06

### Added

- An opt-in outbox: with `outbox=` (`MemoryOutbox`, or Redis Streams via
  `santati.outbox.redis.RedisOutbox`, `santati[redis]`) `events.emit` stores the
  event and returns at once with `EmitResult.queued` true; a background worker
  sends the outbox in batches of `batch_size` every `flush_interval_ms`
  through `emit_batch`, with `pre_send`/`post_send` hooks, `Santati.flush()`,
  and `OutboxError`. `close()` now stops the worker and flushes the outbox
  first. A queued `emit` stores a copy of the event; a batch that fails with
  anything but a `SantatiError` (e.g. a malformed `pre_send` result) is
  reported to `post_send` as `OutboxError` `hook_failed` and dropped, never
  raised. Each batch is sent once, without `max_retries`: a retryable failure releases it for the next pass.

### Changed

- `EmitResult` has a new `queued` field, and its `event` is `None` for a
  queued emit.

### Fixed

- `events.emit_batch` raises `santati.ValidationError` (with `field`) for an
  item the request model rejects, such as an actor without `type`, instead of
  a raw `pydantic.ValidationError`.

## [0.1.0] - 2026-10-01

### Added

- Framework integrations under `santati.integrations`, each behind an extra:
  Django auth signals (`santati[django]`), LangChain/LangGraph tool calls
  (`santati[langchain]`), OpenAI Agents SDK tool calls
  (`santati[openai-agents]`), Pydantic AI tool executions
  (`santati[pydantic-ai]`) and Claude Agent SDK tool calls
  (`santati[claude-agent-sdk]`). Each sends events inline through `client` or
  hands them to a `dispatch` callable, and never raises into the framework.
- Emit one audit event or a batch, with generated idempotency keys and retries.
- List audit events with filters and cursor pagination, and iterate across pages.

[Unreleased]: https://github.com/jamescarr/santati-sdks/compare/python-v0.2.0...HEAD
[0.2.0]: https://github.com/jamescarr/santati-sdks/compare/python-v0.1.0...python-v0.2.0
[0.1.0]: https://github.com/jamescarr/santati-sdks/releases/tag/python-v0.1.0
