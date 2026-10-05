# santati-sdks — agent guide

Polyglot SDK monorepo for the Santati control plane: seven packages
(`python`, `typescript`, `go`, `rust`, `elixir`, `ruby`, `php`), each an
openapi-generator core plus a hand-written facade, kept identical on the wire
by a shared conformance suite.

`CLAUDE.md` is a symlink to this file.

## Repo map

| Path | What lives there |
| --- | --- |
| `spec/santati-v0.yaml` | the control plane's public spec, vendored by `mise run spec:sync` |
| `spec/santati-v0.sdk.yaml` | the *generation profile*: `spec/santati-v0.yaml` after the prunes in `.mise/lib/generate.py` — what the generators actually read |
| `.mise/lib/generate.py` | the profile rewrites + openapi-generator runs; `--check` is `mise run check:drift` |
| `generator/config.json` | the image, the `operations` allow-list, and each core's generator, properties, name mappings and copy targets |
| `docs/sdk-surface.md` | **normative** behaviour of every facade |
| `conformance/` | `features.json` + `cases/*.json` vectors, one native runner per SDK, `check.mjs` |
| `chaos/` | manual outbox load and fault-injection harness: `gateway.mjs` (fault-injecting ingest stand-in), `scenarios.json` (scenarios and budgets), `sdks.json` (how to run each driver), `run.mjs`; the drivers live in each package's test tree (`python/tests/chaos_driver.py`, `typescript/src/chaos/driver.ts`, `go/chaos_test.go`, `rust/tests/chaos.rs`, `elixir/test/chaos_driver.exs`, `ruby/test/chaos_driver.rb`, `php/tests/chaos_driver.php`) |
| `<pkg>/` (seven) | the facade, its manifest, `CHANGELOG.md`, `README.md`, `LICENSE`, and the generated core |
| `.mise/tasks/` | every workflow command (`mise tasks ls`) |
| `.claude/skills/` | `sdk-release`, `sdk-spec-sync`, `sdk-add-operation`, `sdk-conformance` |
| `docs/releasing.md` | secrets, one-time registry setup, the release flow |

## Tasks

| Task | Does |
| --- | --- |
| `mise run status` | version, last tag, published?, commits since — per package |
| `mise run deps [pkgs…]` | fetch dependencies |
| `mise run format [pkgs…]` | format (generated trees excluded) |
| `mise run spec:sync [ref]` | re-vendor `spec/santati-v0.yaml` from the control plane |
| `mise run generate [cores…]` | rebuild the profile and regenerate the cores |
| `mise run check` | everything CI runs: `lint:workflows`, `check:drift`, `check:package` ×7, `check:conformance` |
| `mise run check:package <pkg>` | one package: format, lint, type-check, test, and build the way it publishes |
| `mise run check:conformance [pkgs…]` | validate the corpus and run the SDKs against it |
| `mise run check:drift` | regenerate into a temp tree and diff |
| `mise run chaos [pkgs…]` | manual, not part of `check`: run each SDK's outbox against `chaos/gateway.mjs` faults (slow, blackhole, refused, flaky, recovery) and judge emit latency, loop lag, `close()` time and delivery against `chaos/scenarios.json`; `CHAOS_SCENARIOS=a,b` selects scenarios |
| `mise run lint:workflows` | actionlint |
| `mise run release:prepare` / `release:preflight` / `release:tag` / `release:watch` / `release:verify` / `release:notes` | see `docs/releasing.md` |

CI runs exactly these tasks; a task that is green locally is green there.

## Rules

1. **Generated code is never hand-edited.** The generated trees are
   `<pkg>/src/santati_core` (python), `<pkg>/src/core` (typescript),
   `go/internal/core`, `rust/src/generated/models`, `elixir/lib/santati_core`,
   `ruby/lib/santati_core.rb` + `lib/santati_core/`. To change them, change
   `generator/config.json` or the profile rewrites in `.mise/lib/generate.py`
   and run `mise run generate`; `mise run check:drift` fails any PR whose
   committed trees do not match a fresh generation. Formatters and linters
   exclude the generated trees; compilers and type-checkers include them.
2. **Behaviour changes go through the contract.** A behaviour change is one PR
   that updates `docs/sdk-surface.md`, adds or changes vectors in
   `conformance/`, and implements it in all seven facades. `mise run check`
   enforces the last part.
3. **Every user-visible change gets a `## [Unreleased]` entry** in each
   affected package's `CHANGELOG.md`; `release:prepare` refuses a package with
   none.
4. **Facades validate locally first** and construct generated models only
   afterwards; local validation is limited to what `docs/sdk-surface.md` lists
   (the profile strips length bounds, so the server is the only validator of
   values). A generated model's rejection of anything else surfaces as a
   `ValidationError` (`status` null), never as a generator exception.
5. **Never publish from a private repo state**: Go, PHP (Packagist) and
   TypeScript (npm provenance) need this repository public. See
   `docs/releasing.md`.

## How each facade uses its generated core

Every facade owns one configuration/client object per client instance (never
the generators' process-wide defaults) and re-exports the generated read
models (`AuditEvent`, `EventActor`, `EventTarget`) from its public entry point.

The profile keeps the spec's `ApiKeyAuth` scheme, so python, typescript, go, ruby
and php hand the key and the `Api-Key` prefix to the core's own api-key support;
elixir and rust set `Authorization` on their own transport (their generated
layer has no auth).

| Package | Generated layer used | Why (and what is deliberately unused) |
| --- | --- | --- |
| python | `santati_core.Configuration`/`ApiClient` + `AuditEventsApi`, via `events_*_without_preload_content` | the raw `urllib3.HTTPResponse` keeps the status (`200` vs `201`), the body of a `202`/`207` and the `Retry-After` header reachable; the preload variant raises and discards them |
| typescript | `core/Configuration` + `AuditEventsApi`, via `eventsCreateRaw`/`eventsListRaw` with `redirect: 'manual'` and an `AbortSignal` timeout | the typed methods run `202`/`207` bodies through `AuditEventFromJSON` and lose the batch result; `ResponseError.response` carries the raw status/headers |
| go | `internal/core` configuration + `AuditEventsAPI`, with `CheckRedirect` returning `ErrUseLastResponse` | `Execute()` on `202`/`207` fails to decode into `AuditEvent` by design; the facade reads `GenericOpenAPIError.Body()` into `EventBatchResult` |
| rust | **only** `src/models` (mounted as `crate::models` in `lib.rs`) | the generated reqwest API layer drops response headers, so `Retry-After` would be unreachable; the facade drives `reqwest::Client` itself (`redirect::Policy::none()`, `retry::never()`) |
| elixir | `SantatiCore.Connection` + `Tesla` + `SantatiCore.Deserializer` | `SantatiCore.Api.AuditEvents` returns `{:ok, struct}` for both `200` and `201` and `{:ok, env}` for mapped 4xx, so status discrimination and error bodies are impossible through it; the generated request structs also encode absent fields as `null` |
| ruby | `SantatiCore::ApiClient` + `events_create_with_http_info(debug_return_type: "String")` | the generated return type is hard-coded to `AuditEvent`, which turns `202`/`207` into an empty `AuditEvent`; `api_client.rb` also needs `faraday`, `faraday-multipart` and `marcel` |
| php | `Santati\Core\Configuration` + `AuditEventsApi` via `eventsCreateWithHttpInfo`/`eventsListWithHttpInfo` | `[...]` tuples carry the raw status and headers the facade needs; the generated core has no timeout/redirect knobs, so the facade configures Guzzle itself |

## Releasing

`mise run status` → `mise run release:prepare <bump> <pkgs…>` → merge the PR →
`git switch main && git pull` → `mise run release:tag <pkgs…>` →
`mise run release:watch <pkg>` → `mise run release:verify <pkgs…>`. Secrets
and one-time registry setup: `docs/releasing.md`. The release skills are
`.claude/skills/sdk-release`, `sdk-spec-sync`, `sdk-add-operation` and
`sdk-conformance`.
