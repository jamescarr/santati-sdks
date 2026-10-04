# Changelog

All notable changes to `@santati/node` are documented here. Format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); this project follows
[Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- `log()`: fire-and-forget emit through an outbox (in-memory by default, Redis adapter via `@santati/node/redis` with the optional `ioredis` peer dependency), background batching, `preSend`/`postSend` hooks, `flush()`/`close()`. `log()` stores a copy of the event; a batch that fails with anything but a `SantatiError` (e.g. a malformed `preSend` result) is reported to `postSend` as `OutboxError` `hook_failed` and dropped, never thrown.

## [0.1.0] - 2026-10-01

### Added

- Emit one audit event or a batch, with generated idempotency keys and retries.
- List audit events with filters and cursor pagination, and iterate across pages.

[Unreleased]: https://github.com/jamescarr/santati-sdks/compare/typescript-v0.1.0...HEAD
[0.1.0]: https://github.com/jamescarr/santati-sdks/releases/tag/typescript-v0.1.0
