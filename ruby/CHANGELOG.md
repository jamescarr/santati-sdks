# Changelog

All notable changes to this gem are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- `log()`: fire-and-forget emit through an outbox (in-memory by default, Redis adapter via `require "santati/outbox/redis"` and the `redis` gem), background batching, `pre_send`/`post_send` hooks, `flush()`/`close()`. `log()` stores a copy of the event; a batch that fails with anything but a `Santati::Error` (e.g. a malformed `pre_send` result) is reported to `post_send` as `OutboxError` `hook_failed` and dropped, never raised.

## [0.1.0] - 2026-10-01

### Added

- Emit one audit event or a batch, with generated idempotency keys and retries.
- List audit events with filters and cursor pagination, and iterate across pages.

[Unreleased]: https://github.com/jamescarr/santati-sdks/compare/ruby-v0.1.0...HEAD
[0.1.0]: https://github.com/jamescarr/santati-sdks/releases/tag/ruby-v0.1.0
