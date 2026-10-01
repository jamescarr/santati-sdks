---
name: sdk-spec-sync
description: Re-vendor the control plane's public OpenAPI spec, regenerate every SDK core, and deal with what the diff implies across the seven facades and the conformance suite. Use when the user asks to sync the spec, update the SDKs from the control plane, or when spec/santati-v0.yaml or the generated cores look stale.
allowed-tools: [
    Bash(mise run *),
    Bash(git *),
    Bash(gh *),
]
---

## When to use

The control plane shipped a spec change (its `docs/openapi/v0.yaml`), or
`spec-sync.yml` opened a PR, or `mise run check:drift` fails on a checkout
whose generated code is stale.

## Steps

1. **Vendor the spec and regenerate.**

   ```sh
   mise run spec:sync                 # main; or a branch/tag/sha of the control plane
   mise run generate
   ```

   `spec:sync` prints `spec/santati-v0.yaml: <version> (<ref>)` and the diff
   stat; `generate` prints `generated <cores>`. Both were also run by
   `spec-sync.yml` when it opened the PR.

2. **Confirm the generation is reproducible.**

   ```sh
   mise run check:drift
   ```

   Prints `generated code is current`, or the diff plus
   `generated code is stale: run mise run generate`.

3. **Read the profile diff and decide what it means.**

   ```sh
   git diff spec/santati-v0.sdk.yaml
   ```

   - New **response** fields, new enum members, widened types: nothing to do —
     the facades return the generated models.
   - New **request** fields or parameters: expose them in every facade per
     `docs/sdk-surface.md` (client options, `emit`, `emit_batch`, `list`,
     `iterate`), add vectors to `conformance/cases/`, and implement all seven
     SDKs in this PR. Until then `mise run check:conformance` fails — that is
     the point.
   - A changed operationId or path: `generator/config.json`'s `operations`
     list and the facade's `list` parameters change too.
   - A change the profile's rewrites should absorb (a `readOnly` key, a
     `date-time` format, a length bound, a `oneOf`): check
     `.mise/lib/generate.py`'s comments; if the rewrite needs to change, change
     it there and regenerate, because `check:drift` runs that script.

4. **Run everything.**

   ```sh
   mise run check                     # workflows, drift, every package, conformance
   ```

   Compile or conformance failures after a spec change are the signal that a
   facade needs updating (step 3), not something to silence.

5. **Write CHANGELOG entries.** Every package the change touches gets an
   `[Unreleased]` entry saying what its users gain — a new field on events
   read back, a new filter, a new error kind.

6. **Open the PR** (skip if `spec-sync.yml` already did):

   ```sh
   gh pr create --title "chore(spec): sync santati-v0.yaml to <version>" --body "…"
   ```

   The control plane expects that title shape for a spec sync
   (`docs/openapi/README.md` there).

## If it fails

- `operations not in spec: <id>` — the spec no longer has that operationId;
  update `generator/config.json` (or the control plane's operation, if the
  rename was unintended).
- `components.schemas.EventBatchItemResult is not exactly the expected oneOf`
  — the control plane restructured that schema; regenerate the profile's flat
  replacement in `.mise/lib/generate.py` for the new shape before anything
  else.
- `could not fetch docs/openapi/v0.yaml at <ref>` — the ref does not exist, or
  `gh` is not authenticated (`gh auth status`); the control-plane repo is
  private, so the fetch needs a token with contents read.
- A generated core fails to compile: read the compiler error in that core and
  adjust the profile (`generate.py`) rather than the generated file.

## Done when

`mise run check` exits 0 on the branch, no generated file was hand-edited
(`mise run check:drift` is the proof), and the PR is titled
`chore(spec): sync santati-v0.yaml to <version>`.
