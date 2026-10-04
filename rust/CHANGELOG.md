# Changelog

All notable changes to the `santati` crate are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- `log()`: fire-and-forget emit through an outbox (in-memory by default, Redis adapter via the `redis` feature), background batching, `pre_send`/`post_send` hooks, `flush()`/`close()`.

### Changed

- The minimum supported Rust version is now 1.88 (from 1.85), the MSRV of the
  `redis` crate behind the new `redis` feature.

## [0.1.0] - 2026-10-01

### Added

- Emit one audit event or a batch, with generated idempotency keys and retries.
- List audit events with filters and cursor pagination, and iterate across pages.

[Unreleased]: https://github.com/jamescarr/santati-sdks/compare/rust-v0.1.0...HEAD
[0.1.0]: https://github.com/jamescarr/santati-sdks/releases/tag/rust-v0.1.0
