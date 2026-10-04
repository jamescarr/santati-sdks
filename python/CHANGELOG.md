# Changelog

All notable changes to this package are documented here. The format is based
on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- `Santati.log()`: fire-and-forget emit through an outbox — in memory by
  default (`MemoryOutbox`), or Redis Streams via
  `santati.outbox.redis.RedisOutbox` (`santati[redis]`) — sent in the
  background in batches of `batch_size` every `flush_interval_ms`, with
  `pre_send`/`post_send` hooks, `Santati.flush()`, and `OutboxError`.
  `close()` now stops the worker and flushes the outbox first.

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

[Unreleased]: https://github.com/jamescarr/santati-sdks/compare/python-v0.1.0...HEAD
[0.1.0]: https://github.com/jamescarr/santati-sdks/releases/tag/python-v0.1.0
