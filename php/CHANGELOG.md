# Changelog

All notable changes to this package are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this package
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- `log()`: fire-and-forget emit through an outbox (in-memory by default, Redis adapter via `predis/predis`), `preSend`/`postSend` hooks, `flush()`/`close()`; pending events are also flushed at shutdown. A batch that fails with anything but a `SantatiException` (e.g. a malformed `preSend` result) is reported to `postSend` as `OutboxException` `hook_failed` and dropped, never thrown.

## [0.1.0] - 2026-10-01

### Added

- Emit one audit event or a batch, with generated idempotency keys and retries.
- List audit events with filters and cursor pagination, and iterate across pages.

[Unreleased]: https://github.com/jamescarr/santati-sdks/compare/php-v0.1.0...HEAD
[0.1.0]: https://github.com/jamescarr/santati-sdks/releases/tag/php-v0.1.0
