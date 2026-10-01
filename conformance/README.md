# Conformance

Language-neutral vectors every SDK must pass, so the seven facades can't drift
from each other. `features.json` is the registry of features, operations and
error kinds; `cases/*.json` are the vectors; `check.mjs` validates the corpus
and the registry and then runs each SDK's native runner.

`mise run check:conformance [pkgs…]` is the gate; CI runs it per package.

## Layout

- `features.json` — `{"operations": [...], "error_kinds": [...], "features":
  [{"id", "description"}]}`. Every case names one `feature` and one
  `operation`; every `expect.error.kind` is one of `error_kinds`.
- `cases/<file>.json` — `{"cases": [Case, …]}`.
- `sdks.json` — the registry the checker runs; keys are package directories.
- `check.mjs` — the checker (Node, stdlib only).

## Case format

```jsonc
{
  "id": "globally-unique-string",   // the test name every SDK registers
  "feature": "<features[].id>",
  "operation": "emit" | "emit_batch" | "list" | "iterate",
  "input": {
    "client": { "api_key": "sat_sk_…", "trail"?, "timeout_ms"?, "max_retries"?,
                "initial_backoff_ms"?, "max_backoff_ms"?, "headers"?, "base_path"? },
    "gateway": Gateway,
    // operation-specific:
    "event"? | "events"? | "params"?
  },
  "expect": {
    // exactly one of:
    "ok": { /* operation-specific, below */ },
    "error": { "kind": "<error_kinds[]>", "status"?, "code"?, "field"?, "retry_after"? },
    "requests"?: [ExpectedRequest, …]
  }
}
```

The runner sets `base_url` = the mock origin + (`base_path` or `""`); with
`{"unreachable": true}` the origin is `http://127.0.0.1:1`.

### Gateway

`{"unreachable": true}` (nothing is recorded) or a Response, or
`{"sequence": [Response, …]}` where request *i* gets `sequence[min(i,
len-1)]`. A Response is `{status, headers?, body?: {"json": any} | {"text":
string}, delay_ms?}`: `json` bodies go out compact with `content-type:
application/json`, `text` bodies with `text/plain; charset=utf-8`, unless
`headers` sets `content-type`; `content-length` is always sent; `delay_ms`
sleeps after recording the request and before the status line.

### Expected results

- `ok` shapes: emit `{"event", "duplicate", "idempotency_key"}`; emit_batch
  `{"accepted", "rejected", "results": [{"index", "status", "id"?, "error"?:
  {"code", "message", "field"}}]}`; list `{"results", "next_cursor"}`;
  iterate `[…]`. Read models are serialized with the generated model's own
  serializer back to wire (snake_case) JSON. Comparison is deep equality after
  removing every object member whose value is null, recursively, on both sides.
- `error`: `kind` is compared by exact type (no subclass matching) through the
  runner's fixed kind table; each other key present (`status`, `code`, `field`,
  `retry_after`) is compared exactly. `message` is never compared.
- `requests`, when present: the recorded requests must match in count and
  order; `method` and `path` (the raw request target, query string included)
  must be equal; `headers` is a subset match on lowercase names; `body`, when
  present, is deep-equal to the JSON the SDK sent with **no** null stripping.
  `[]` means no request was made.
- `{"$generated": "<label>"}`, inside `ok` or a request `body`, matches any
  non-empty string: the same label binds the same value everywhere in the case,
  and different labels must bind different values.

Query values in vectors use only `A-Z a-z 0-9 - _ . : + =`: every generated
core encodes those identically, while spaces, `/`, `~`, `*`, `!`, `'`, `(` and
`)` differ across encoders.

## Runner contract

Every SDK ships a native runner that:

1. Loads every `cases/*.json` relative to the repo root and fails when zero
   cases load.
2. Registers one test per case, named by its `id`, and never skips one.
3. Fails on an unknown operation.
4. Uses only the package's public entry point (generated helpers it needs, such
   as `AuditEventToJSON` in TypeScript, are re-exported there).
5. Maps errors to kinds by exact type.
6. Starts a fresh mock server per case, bound to `127.0.0.1` on an ephemeral
   port, recording `{method, path, headers (lowercased), body (parsed JSON,
   null when empty)}` — except the runner's own lifecycle (start/stop), which
   is not a case concern.
7. Is registered in `sdks.json`.

## Adding a feature

Add it to `features.json` with at least one case. Every SDK then fails until it
implements the feature — that is the point.

## Registering an SDK

Add an entry to `sdks.json` keyed by the package directory. The checker fails
when a package directory is missing from it, or when it names a directory that
is not a package.

```json
{"sdks": {"<dir>": {"setup": [["<cmd>", "…"], …], "run": ["<cmd>", "…"]}}}
```

`setup` entries run in the SDK's directory, in order, then `run`. The first
non-zero exit stops that SDK's remaining steps and fails the gate.
