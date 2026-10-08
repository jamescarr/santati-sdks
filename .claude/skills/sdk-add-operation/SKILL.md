---
name: sdk-add-operation
description: Expose a new Santati control-plane API operation in all seven SDKs — add it to the generator config, regenerate, extend the surface contract, add conformance vectors, and implement each facade. Use when the user asks to add an operation, endpoint, or API method to the SDKs.
allowed-tools: [
    Bash(mise run *),
    Bash(git *),
    Bash(gh *),
]
---

## When to use

The control plane gained an operation the SDKs should expose (a new endpoint,
or a new operation on an existing path). Adding it is a seven-language change
in one PR.

## Steps

1. **Add the operationId to the generator config.**

   ```json
   // generator/config.json
   "operations": ["events_create", "events_list", "<new_operation_id>"]
   ```

   The id must match the control plane's spec exactly; `generate.py` fails
   with `operations not in spec: <id>` otherwise. If the new operation needs
   its own generated API class or models, they arrive with the regeneration.

2. **Regenerate.**

   ```sh
   mise run generate
   ```

   Prints `generated <cores>`. Confirm with `mise run check:drift` →
   `generated code is current`.

3. **Extend the contract first.** `docs/sdk-surface.md` is normative: add the
   operation's language-neutral signature, its inputs, its result shape, and
   its error mapping to the tables there, then translate it into each
   language's table (snake_case/camelCase/struct fields).

4. **Add the operation and its vectors to the conformance suite.**
   `conformance/features.json` gets at least one feature with a one-sentence
   description, `operations` gets the operation name, and `conformance/cases/`
   gets vectors following `conformance/README.md` (mock gateway, `expect.ok`
   or `expect.error`, optional `requests`). Validate the corpus alone:

   ```sh
   node conformance/check.mjs
   ```

   The checker prints `N features, M cases` and then fails every SDK that does
   not implement the operation yet — expected at this point.

5. **Implement it in all seven packages**: `python`, `typescript`, `go`,
   `rust`, `elixir`, `ruby`, `php`. Each facade follows its existing
   `emit`/`list` shape: local validation first, the generated core for the
   request, mapping to the same error kinds, retries unchanged. The
   generated core is never edited.

6. **Run the whole gate.**

   ```sh
   mise run check
   ```

   It runs `lint:workflows`, `check:drift`, `check:package` for every package,
   and `check:conformance`; the last line per SDK is `<pkg>  pass`.

7. **CHANGELOG entries** in every package that now exposes the operation, under
   `## [Unreleased]` → `### Added`.

## If it fails

- `unknown conformance operation "<op>"` from a runner — the runner's dispatch
  table (one per SDK) lacks the new operation; add it there.
- A single SDK failing while the others pass: run that SDK's runner directly
  from its directory (the `run` command in `conformance/sdks.json`) to see the
  assertion detail.
- `check:drift` failing after you edited a generated tree by accident: revert
  the tree and change the profile (`generator/config.json` or
  `.mise/lib/generate.py`) instead.

## Done when

`mise run check:conformance` prints `<pkg>  pass` for all seven packages, the
surface contract documents the operation, and all seven CHANGELOGs have an
`[Unreleased]` entry.
