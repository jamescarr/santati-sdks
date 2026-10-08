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
  "operation": "emit" | "emit_batch" | "list" | "iterate" | "emit_outbox"
             | "list_definitions" | "iterate_definitions" | "get_definition" | "create_definition"
             | "update_definition" | "delete_definition" | "list_versions" | "iterate_versions"
             | "get_version" | "create_version" | "update_version" | "delete_version"
             | "publish_version" | "deprecate_version" | "check_schema"
             | "list_standard_packs" | "install_standard_packs",
  "input": {
    "client": { "api_key": "sat_sk_…", "trail"?, "timeout_ms"?, "max_retries"?,
                "initial_backoff_ms"?, "max_backoff_ms"?, "headers"?, "base_path"?,
                // emit_outbox only:
                "batch_size"?, "flush_interval_ms"?, "max_pending"? },
    "gateway": Gateway,
    // operation-specific:
    "event"? | "events"? | "params"?,
    // schema operations only (each operation reads the keys it needs):
    //   "action" (string), "version" (int), "params" ({limit?, cursor?}; iterate: {limit?}),
    //   "definition" ({action, description?, allowed_target_types?, is_active?}),
    //   "changes" ({new_action?, description?, allowed_target_types?, is_active?}),
    //   "schema" (object), "if_match" (string, update_version), "packs" ([string])
    "hooks"?   // emit_outbox only, see "The emit_outbox operation"
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

- `ok` shapes: emit `{"event", "duplicate", "idempotency_key", "queued"}`
  (an EmitResult; `event` is null for a queued result, and nulls are stripped
  before comparison, so a queued result equals `{"duplicate",
  "idempotency_key", "queued"}`); emit_batch
  `{"accepted", "rejected", "results": [{"index", "status", "id"?, "error"?:
  {"code", "message", "field"}}]}`; list `{"results", "next_cursor"}`;
  iterate `[…]`; emit_outbox `{"results": [EmitResult…], "outcomes":
  [{"event": <wire envelope>, "status", "id"?, "error"?: {"kind", "status",
  "code", "field", "retry_after"}}]}`. Read models are serialized with the
  generated model's own serializer back to wire (snake_case) JSON. Comparison
  is deep equality after removing every object member whose value is null,
  recursively, on both sides.
  The schema operations answer: the list operations `{"results", "next_cursor"}`
  (`next_cursor` the decoded cursor or null); the iterate operations `[…]`; the
  definition operations the wire `EventDefinition`; the version operations
  `{"schema_version": <wire EventSchemaVersion>, "etag"}` (`etag` the `ETag`
  header verbatim or null); `check_schema`, `list_standard_packs` and
  `install_standard_packs` the wire model; `delete_definition` and
  `delete_version` `null`.
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

### The `emit_outbox` operation

`input.events` are the events emitted, in order. The runner builds the client
**with `outbox` = `MemoryOutbox(max_pending)` when `max_pending` is present,
else `MemoryOutbox()` with its default** (a store is always configured for this
operation). `input.client.batch_size` and `flush_interval_ms` are passed to the
client as given (vectors use `60000` so the worker's tick never races
`close()`).
`input.hooks` is `{"pre_send"?: {"set_metadata"?: object, "drop_events"?:
[string], "raise"?: true}, "post_send"?: {"raise"?: true}}`.

The runner:

1. Builds the client. When `hooks.pre_send` is present it registers a
   `pre_send` that throws a plain non-SDK exception with `raise: true` (Go:
   `panic`, Rust: `panic!`), else returns null for an event whose `event` is
   in `drop_events`, else merges `set_metadata` into the event's `metadata`
   (`{...existing, ...set_metadata}`) when given, else returns the event
   unchanged. It **always** registers a `post_send` that appends `{event:
   <the stored event as wire JSON>, status, id, error: {kind, status, code,
   field, retry_after}}` (the error rendered like `expect.error`, kind by exact
   type) to the outcomes, then throws when `hooks.post_send.raise`.
2. Calls `emit` for each event in order, collecting each EmitResult in the
   emit shape; an SDK error stops emitting and is remembered.
3. Calls `close()` (Elixir: stops the `Santati.Outbox` process named by the
   client's `outbox`).
4. Reports the remembered error (the requests are still asserted), else
   `{"results", "outcomes"}`.

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
