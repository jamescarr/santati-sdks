# Changelog

All notable changes to this package are documented here. The format is based
on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
