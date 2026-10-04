# Changelog

All notable changes to this package are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and this package
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- An opt-in outbox: `Santati.new(…, outbox: server)` names a supervised `Santati.Outbox` process, and `Santati.Events.emit/2` then stores the event there and returns at once with `Santati.EmitResult.queued` true (in-memory store by default, Redis adapter via `Santati.Outbox.Redis` and the optional `redix` dependency); the process sends the outbox in batches, with `pre_send`/`post_send` hooks, `Santati.flush/1` and `Santati.Outbox.stop/1` (flushes on terminate). A batch whose send raises (e.g. on a malformed `pre_send` result, or an event JSON cannot encode) is reported to `post_send` as `Santati.OutboxError` `hook_failed` and dropped instead of crashing the process; an event `emit/2` raises on raises in the caller of a queued `emit/2`, not in the process.

### Changed

- `Santati.EmitResult` has a new `queued` field, and its `event` is `nil` for a queued emit.

### Fixed

- `Santati.Events.emit/2` and `emit_batch/2` raise `Protocol.UndefinedError` for an event JSON cannot represent (e.g. a tuple in `data`) instead of answering a `Santati.TransportError` after retrying the same body.

## [0.1.0] - 2026-10-01

### Added

- Emit one audit event or a batch, with generated idempotency keys and retries.
- List audit events with filters and cursor pagination, and iterate across pages.

[Unreleased]: https://github.com/jamescarr/santati-sdks/compare/elixir-v0.1.0...HEAD
[0.1.0]: https://github.com/jamescarr/santati-sdks/releases/tag/elixir-v0.1.0
