# Changelog

All notable changes to this package are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- `santati_event_schema` resource: the current JSON Schema of one event action, kept in sync as numbered schema versions. A changed document publishes a new version (the server deprecates the old one); `publish = false` keeps a draft that is edited in place. Import by `<action>` or `<action>/<version>`.

## [0.1.0] - 2026-10-08

### Added

- Terraform provider: `santati_trail` and `santati_log_stream` resources; `santati_trails`, `santati_organizations` and `santati_event_definitions` data sources.

[Unreleased]: https://github.com/jamescarr/santati-sdks/compare/terraform-v0.1.0...HEAD
[0.1.0]: https://github.com/jamescarr/santati-sdks/releases/tag/terraform-v0.1.0
