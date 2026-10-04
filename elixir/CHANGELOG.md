# Changelog

All notable changes to this package are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and this package
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- `Santati.log/2`: fire-and-forget emit through a supervised `Santati.Outbox` process (in-memory by default, Redis adapter via `Santati.Outbox.Redis` and the optional `redix` dependency), background batching, `pre_send`/`post_send` hooks, `Santati.flush/1` and `Santati.Outbox.stop/1` (flushes on terminate). A batch whose send raises (e.g. on a malformed `pre_send` result) is reported to `post_send` as `Santati.OutboxError` `hook_failed` and dropped instead of crashing the process.

## [0.1.0] - 2026-10-01

### Added

- Emit one audit event or a batch, with generated idempotency keys and retries.
- List audit events with filters and cursor pagination, and iterate across pages.

[Unreleased]: https://github.com/jamescarr/santati-sdks/compare/elixir-v0.1.0...HEAD
[0.1.0]: https://github.com/jamescarr/santati-sdks/releases/tag/elixir-v0.1.0
