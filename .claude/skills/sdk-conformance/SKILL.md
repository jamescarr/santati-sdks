---
name: sdk-conformance
description: Add, change or debug the language-neutral conformance vectors that keep the seven Santati SDKs identical on the wire. Use when the user asks to add a conformance case, change a vector, debug a failing SDK in the suite, or run the conformance gate for one SDK.
allowed-tools: [
    Bash(mise run *),
    Bash(git *),
    Bash(node *),
]
---

## When to use

A behaviour needs pinning across all seven SDKs, a vector needs changing to
match `docs/sdk-surface.md`, or one SDK fails the gate and the others pass.

## Steps

1. **Read the rules first.** `conformance/README.md` defines the case format
   (client/gateway/`expect`, `$generated` labels, request matching) and the
   runner contract; `docs/sdk-surface.md` is the behaviour being tested. A
   vector that contradicts the surface doc is a bug in the vector.

2. **Write or change the vector** in `conformance/cases/<file>.json`, keeping
   the file's existing groupings and the corpus' conventions:
   - every case names a `feature` from `conformance/features.json` and an
     `operation` from its `operations` list;
   - query values use only `A-Z a-z 0-9 - _ . : + =` (encoders differ on
     everything else);
   - error expectations name a `kind` from `error_kinds` and compare only the
     keys present (`message` is never compared);
   - `{"$generated": "<label>"}` binds a value across the case: repeated
     labels mean "the same value", different labels mean "different values".

3. **Validate the corpus.**

   ```sh
   mise run check:conformance              # every SDK
   mise run check:conformance python go    # just these
   ```

   The checker first validates the corpus (duplicate ids, unknown
   features/operations, shapes) and prints `N features, M cases` plus a
   per-feature count, then runs each selected SDK and prints
   `<pkg>  pass` or `<pkg>  FAIL (exit N)`.

4. **Debug one SDK** when only it fails: run its `run` command from its own
   directory (the same command the checker uses), after its `setup`:

   ```sh
   cd python && uv run pytest tests/test_conformance.py -k emit.generated_key -x
   ```

   Substitute `npm run conformance` (typescript), `go test -run TestConformance
   ./...` (go), `cargo test --locked --test conformance` (rust), `mix test
   test/conformance_test.exs` (elixir), `bundle exec ruby -Ilib -Itest
   test/conformance_test.rb` (ruby), `vendor/bin/phpunit
   tests/ConformanceTest.php` (php) — all listed in `conformance/sdks.json`.

## If it fails

- `duplicate case id "…"`, `unknown feature "…"`, `unknown operation "…"`,
  `expect must have exactly one of "ok" or "error"`,
  `input.client must be an object with a string api_key` — the corpus
  validation; fix the vector, never the checker's expectation.
- `<pkg>  FAIL` — the SDK and the vector disagree. Decide which is wrong by
  reading `docs/sdk-surface.md`; if the SDK is wrong, fix the SDK (all seven
  must keep passing); if the vector is wrong, fix the vector and re-run every
  SDK.
- A case that hangs: the mock gateway's `delay_ms` plus a client that does not
  apply `timeout_ms`; check the SDK's timeout wiring.
- Never skip, filter, or quarantine a case to make the gate green.

## Done when

`mise run check:conformance` prints `N features, M cases`, per-feature counts
and `<pkg>  pass` for all seven, and the new vectors are the only behaviour
change (docs/CHANGELOGs updated if user-visible).
