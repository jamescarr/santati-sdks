import assert from "node:assert/strict";
import { once } from "node:events";
import { readFileSync, readdirSync } from "node:fs";
import http from "node:http";
import { describe, test } from "node:test";

import * as santati from "../index.js";

// The language-neutral vectors live at the repo root; this file sits two
// directories deep. `npm test` also matches it: see conformance/README.md.

type Body = { json: unknown } | { text: string };

interface GatewayResponse {
  status: number;
  headers?: Record<string, string>;
  body?: Body;
  delay_ms?: number;
}

type Gateway = { unreachable: true } | { sequence: GatewayResponse[] } | GatewayResponse;

interface ClientOptions {
  api_key: string;
  trail?: string;
  timeout_ms?: number;
  max_retries?: number;
  initial_backoff_ms?: number;
  max_backoff_ms?: number;
  headers?: Record<string, string>;
  base_path?: string;
  batch_size?: number;
  flush_interval_ms?: number;
  max_pending?: number;
}

interface Hooks {
  pre_send?: { set_metadata?: Record<string, string>; drop_events?: string[]; raise?: true };
  post_send?: { raise?: true };
}

/** A vector's event: the wire's snake_case names, the facade's camelCase shapes. */
interface WireEvent {
  event?: string;
  trail?: string;
  organization_id?: string;
  actor?: santati.ActorInput;
  targets?: santati.TargetInput[];
  metadata?: Record<string, string>;
  data?: unknown;
  context?: Record<string, unknown>;
  created_at?: string;
  idempotency_key?: string;
}

interface WireParams {
  trail?: string;
  event?: string;
  event_prefix?: string;
  organization_id?: string;
  actor_id?: string;
  actor_type?: string;
  target_type?: string;
  target_id?: string;
  created_after?: string;
  created_before?: string;
  q?: string;
  sort?: string;
  limit?: number;
  cursor?: string;
}

interface ExpectedRequest {
  method: string;
  path: string;
  headers?: Record<string, string>;
  body?: unknown;
}

interface Expect {
  ok?: unknown;
  error?: { kind: string; [key: string]: unknown };
  requests?: ExpectedRequest[];
}

interface Case {
  id: string;
  feature: string;
  operation: string;
  input: {
    client: ClientOptions;
    gateway: Gateway;
    event?: WireEvent;
    events?: WireEvent[];
    params?: WireParams;
    hooks?: Hooks;
  };
  expect: Expect;
}

interface Recorded {
  method: string;
  path: string;
  headers: http.IncomingHttpHeaders;
  body: unknown;
}

const DIR = new URL("../../../conformance/", import.meta.url);

const CASES: Case[] = readdirSync(new URL("cases/", DIR))
  .filter((name) => name.endsWith(".json"))
  .sort()
  .flatMap(
    (name) =>
      (JSON.parse(readFileSync(new URL(`cases/${name}`, DIR), "utf8")) as { cases: Case[] }).cases,
  );

// An empty directory, or one this file doesn't resolve to, must fail loudly:
// zero cases would otherwise report a green run.
assert.ok(CASES.length > 0, `no conformance cases found under ${DIR.href}`);

// A namespace import, so an export that doesn't exist yet reads as `undefined`
// and fails only its own cases instead of the whole file. This also makes the
// runner a public-export check.
const ERROR_KINDS: Record<string, unknown> = {
  ValidationError: santati.ValidationError,
  AuthError: santati.AuthError,
  NotFoundError: santati.NotFoundError,
  RateLimitedError: santati.RateLimitedError,
  ServerError: santati.ServerError,
  TransportError: santati.TransportError,
  ApiError: santati.ApiError,
  OutboxError: santati.OutboxError,
};

/** The `$generated` values bound so far: one label, one value, per case. */
type Bindings = Map<string, string>;

function bodyBytes(body: Body | undefined): Buffer {
  if (body === undefined) return Buffer.alloc(0);
  return Buffer.from("json" in body ? JSON.stringify(body.json) : body.text, "utf8");
}

function bodyContentType(body: Body | undefined): Record<string, string> {
  if (body === undefined) return {};
  return "json" in body
    ? { "content-type": "application/json" }
    : { "content-type": "text/plain; charset=utf-8" };
}

/** A real HTTP server standing in for the API: one `Response` per request. */
async function startGateway(
  spec: Gateway,
  requests: Recorded[],
): Promise<{ origin: string; close: () => Promise<void> }> {
  if ("unreachable" in spec) return { origin: "http://127.0.0.1:1", close: async () => {} };

  const timers: NodeJS.Timeout[] = [];
  let index = 0;
  const server = http.createServer((req, res) => {
    const response =
      "sequence" in spec ? spec.sequence[Math.min(index++, spec.sequence.length - 1)] : spec;
    const record: Recorded = {
      method: req.method ?? "GET",
      path: req.url ?? "",
      headers: req.headers,
      body: null,
    };
    requests.push(record);
    const chunks: Buffer[] = [];
    req.on("data", (chunk: Buffer) => chunks.push(chunk));
    // A client that has already timed out may have closed the connection.
    res.on("error", () => {});
    req.on("end", () => {
      const text = Buffer.concat(chunks).toString("utf8");
      record.body = text === "" ? null : JSON.parse(text);
      const bytes = bodyBytes(response.body);
      const respond = () => {
        res.writeHead(response.status, {
          ...bodyContentType(response.body),
          ...(response.headers ?? {}),
          "content-length": String(bytes.length),
        });
        res.end(bytes.length === 0 ? undefined : bytes);
      };
      // A real delay: the timeout vector needs a response that outlives the
      // client's deadline, which fake timers cannot drive across a socket.
      if (response.delay_ms) timers.push(setTimeout(respond, response.delay_ms));
      else respond();
    });
  });

  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  const address = server.address();
  if (address === null || typeof address === "string") throw new Error("no server address");
  return {
    origin: `http://127.0.0.1:${address.port}`,
    close: async () => {
      for (const timer of timers) clearTimeout(timer);
      const closed = once(server, "close");
      server.close();
      server.closeAllConnections();
      await closed;
    },
  };
}

interface Outcome {
  event: unknown;
  status: string;
  id?: string;
  error?: ErrorRecord;
}

function errorRecord(error: santati.SantatiError): ErrorRecord {
  return {
    kind: kindOf(error) ?? error.name,
    status: error.status,
    code: error.code,
    field: error.field,
    retry_after: error.retryAfter,
  };
}

function makeClient(
  origin: string,
  options: ClientOptions,
  hooks: Hooks = {},
  outcomes: Outcome[] = [],
): santati.Santati {
  const pre = hooks.pre_send;
  return new santati.Santati({
    apiKey: options.api_key,
    baseUrl: origin + (options.base_path ?? ""),
    trail: options.trail,
    timeoutMs: options.timeout_ms,
    maxRetries: options.max_retries,
    initialBackoffMs: options.initial_backoff_ms,
    maxBackoffMs: options.max_backoff_ms,
    headers: options.headers,
    batchSize: options.batch_size,
    flushIntervalMs: options.flush_interval_ms,
    outbox:
      options.max_pending != null
        ? new santati.MemoryOutbox({ maxPending: options.max_pending })
        : undefined,
    preSend: pre
      ? (event) => {
          if (pre.raise) throw new Error("conformance pre_send");
          if (pre.drop_events?.includes(event.event)) return null;
          if (pre.set_metadata) {
            return { ...event, metadata: { ...event.metadata, ...pre.set_metadata } };
          }
          return event;
        }
      : undefined,
    postSend: (event, outcome) => {
      outcomes.push({
        event: santati.envelopeToWire(event),
        status: outcome.status,
        id: outcome.id,
        error: outcome.error && errorRecord(outcome.error),
      });
      if (hooks.post_send?.raise) throw new Error("conformance post_send");
    },
  });
}

function eventInput(raw: WireEvent = {}): santati.EventInput {
  return {
    event: raw.event ?? "",
    trail: raw.trail,
    organizationId: raw.organization_id,
    actor: raw.actor,
    targets: raw.targets,
    metadata: raw.metadata,
    data: raw.data,
    context: raw.context,
    createdAt: raw.created_at,
    idempotencyKey: raw.idempotency_key,
  };
}

function listParams(raw: WireParams = {}): santati.ListParams {
  return {
    trail: raw.trail,
    event: raw.event,
    eventPrefix: raw.event_prefix,
    organizationId: raw.organization_id,
    actorId: raw.actor_id,
    actorType: raw.actor_type,
    targetType: raw.target_type,
    targetId: raw.target_id,
    createdAfter: raw.created_after,
    createdBefore: raw.created_before,
    q: raw.q,
    sort: raw.sort,
    limit: raw.limit,
    cursor: raw.cursor,
  };
}

/** Runs the case's operation and returns it in the vector's wire shape. */
async function dispatch(c: Case, requests: Recorded[]): Promise<unknown> {
  const gateway = await startGateway(c.input.gateway, requests);
  try {
    const outcomes: Outcome[] = [];
    const client = makeClient(gateway.origin, c.input.client, c.input.hooks, outcomes);
    switch (c.operation) {
      case "emit": {
        const result = await client.events.emit(eventInput(c.input.event));
        return {
          event: santati.AuditEventToJSON(result.event),
          duplicate: result.duplicate,
          idempotency_key: result.idempotencyKey,
        };
      }
      case "emit_batch": {
        const result = await client.events.emitBatch((c.input.events ?? []).map(eventInput));
        return {
          accepted: result.accepted,
          rejected: result.rejected,
          results: result.results.map((item) => ({
            index: item.index,
            status: item.status,
            id: item.id,
            error: item.error,
          })),
        };
      }
      case "list": {
        const page = await client.events.list(listParams(c.input.params));
        return { results: page.results.map(santati.AuditEventToJSON), next_cursor: page.nextCursor };
      }
      case "iterate": {
        const events: unknown[] = [];
        for await (const event of client.events.iterate(listParams(c.input.params))) {
          events.push(santati.AuditEventToJSON(event));
        }
        return events;
      }
      case "log": {
        const keys: string[] = [];
        let failure: unknown;
        try {
          for (const event of c.input.events ?? []) keys.push(await client.log(eventInput(event)));
        } catch (error) {
          if (!(error instanceof santati.SantatiError)) throw error;
          failure = error;
        }
        await client.close();
        if (failure !== undefined) throw failure;
        return { keys, outcomes };
      }
      default:
        return assert.fail(`unknown conformance operation ${c.operation}`);
    }
  } finally {
    await gateway.close();
  }
}

/** The wire JSON of a value: `JSON.stringify` drops `undefined` members the way the encoders do. */
function wire(value: unknown): unknown {
  return JSON.parse(JSON.stringify(value));
}

/** Deep equality ignores nulls on both sides. */
function stripNulls(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(stripNulls);
  if (value === null || typeof value !== "object") return value;
  const out: Record<string, unknown> = {};
  for (const [key, member] of Object.entries(value)) {
    if (member === null) continue;
    out[key] = stripNulls(member);
  }
  return out;
}

/** `{"$generated": "<label>"}`, the one matcher extension over plain deep equality. */
function generatedLabel(expected: unknown): string | null {
  if (expected === null || typeof expected !== "object" || Array.isArray(expected)) return null;
  const entries = Object.entries(expected);
  if (entries.length !== 1 || entries[0][0] !== "$generated") return null;
  return typeof entries[0][1] === "string" ? entries[0][1] : null;
}

function compare(expected: unknown, actual: unknown, bindings: Bindings, path: string): void {
  const label = generatedLabel(expected);
  if (label !== null) {
    assert.ok(
      typeof actual === "string" && actual.length > 0,
      `${path}: expected a non-empty generated string, got ${JSON.stringify(actual)}`,
    );
    const bound = bindings.get(label);
    if (bound === undefined) {
      for (const [other, value] of bindings) {
        assert.notEqual(value, actual, `${path}: labels ${other} and ${label} bound one value`);
      }
      bindings.set(label, actual);
    } else {
      assert.equal(actual, bound, `${path}: "$generated" label ${label}`);
    }
    return;
  }
  if (Array.isArray(expected)) {
    assert.ok(Array.isArray(actual), `${path}: expected an array, got ${JSON.stringify(actual)}`);
    assert.equal(actual.length, expected.length, `${path}: length`);
    expected.forEach((item, index) => compare(item, actual[index], bindings, `${path}[${index}]`));
    return;
  }
  if (expected !== null && typeof expected === "object") {
    assert.ok(
      actual !== null && typeof actual === "object" && !Array.isArray(actual),
      `${path}: expected an object, got ${JSON.stringify(actual)}`,
    );
    const members = Object.fromEntries(Object.entries(actual));
    assert.deepEqual(Object.keys(members).sort(), Object.keys(expected).sort(), `${path}: members`);
    for (const [key, member] of Object.entries(expected)) {
      compare(member, members[key], bindings, `${path}.${key}`);
    }
    return;
  }
  assert.deepEqual(actual, expected, path);
}

/** The kind of a mapped error, by exact type: no subclass matching. */
function kindOf(error: unknown): string | null {
  if (!(error instanceof Error)) return null;
  for (const [kind, klass] of Object.entries(ERROR_KINDS)) {
    if (error.constructor === klass) return kind;
  }
  return null;
}

function assertRequests(
  actual: Recorded[],
  expected: ExpectedRequest[],
  bindings: Bindings,
): void {
  assert.equal(
    actual.length,
    expected.length,
    `expected ${expected.length} requests, got ${actual.length}: ${JSON.stringify(actual)}`,
  );
  actual.forEach((got, index) => {
    const want = expected[index];
    assert.equal(got.method, want.method, `request ${index} method`);
    assert.equal(got.path, want.path, `request ${index} path`);
    for (const [name, value] of Object.entries(want.headers ?? {})) {
      assert.equal(got.headers[name.toLowerCase()], value, `request ${index} header ${name}`);
    }
    // Absent means "not asserted": not every vector pins the body.
    if ("body" in want) compare(want.body, got.body, bindings, `request ${index} body`);
  });
}

interface ErrorRecord {
  kind: string;
  status: number | null;
  code: string | null;
  field: string | null;
  retry_after: number | null;
}

async function runCase(c: Case): Promise<void> {
  const requests: Recorded[] = [];
  const bindings: Bindings = new Map();
  let ok: unknown;
  let failure: ErrorRecord | undefined;
  try {
    ok = await dispatch(c, requests);
  } catch (error) {
    if (!(error instanceof santati.SantatiError)) throw error;
    const kind = kindOf(error);
    if (kind === null) throw error;
    failure = {
      kind,
      status: error.status,
      code: error.code,
      field: error.field,
      retry_after: error.retryAfter,
    };
  }

  const expect = c.expect;
  if ("ok" in expect) {
    assert.equal(failure, undefined, `expected ok, got ${JSON.stringify(failure)}`);
    compare(stripNulls(wire(expect.ok)), stripNulls(wire(ok)), bindings, "ok");
  } else if (expect.error) {
    const want = expect.error;
    assert.ok(failure, `expected error ${JSON.stringify(want)}, got ok ${JSON.stringify(ok)}`);
    assert.equal(failure.kind, want.kind, "error kind");
    for (const key of ["status", "code", "field", "retry_after"] as const) {
      if (key in want) assert.deepEqual(failure[key], want[key], `${failure.kind}.${key}`);
    }
  } else {
    assert.equal(failure, undefined, `expected no error, got ${JSON.stringify(failure)}`);
  }

  if (expect.requests) assertRequests(requests, expect.requests, bindings);
}

describe("conformance", () => {
  for (const c of CASES) {
    test(c.id, async () => runCase(c));
  }
});
