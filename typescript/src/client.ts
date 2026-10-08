import {
  AuditEventFromJSON,
  AuditEventsApi,
  EventBatchResultFromJSON,
  EventEnvelopeRequestFromJSON,
  EventEnvelopeRequestToJSON,
  PaginatedAuditEventListFromJSON,
} from "./core/index.js";
import type {
  AuditEvent,
  EventActorRequest,
  EventBatchResult,
  EventEnvelopeRequest,
  EventTargetRequest,
  EventsListRequest,
} from "./core/index.js";
import { ValidationError } from "./errors.js";
import { coreConfiguration, decodeJson, nextCursor, requestInit, send, unexpected } from "./http.js";
import { OutboxWorker } from "./outbox.js";
import { withRetries, type RetryOptions } from "./retry.js";
import { Schemas } from "./schemas.js";
import type {
  ActorInput,
  BatchResult,
  EmitResult,
  EventInput,
  EventPage,
  IterateParams,
  ListParams,
  OutboxStore,
  PostSendHook,
  PreSendHook,
  TargetInput,
} from "./types.js";
import { VERSION } from "./version.js";

const DEFAULT_BASE_URL = "https://api.santati.io";
const DEFAULT_TIMEOUT_MS = 10_000;
const DEFAULT_MAX_RETRIES = 2;
const DEFAULT_INITIAL_BACKOFF_MS = 250;
const DEFAULT_MAX_BACKOFF_MS = 8000;
const DEFAULT_BATCH_SIZE = 100;
const DEFAULT_FLUSH_INTERVAL_MS = 1000;

export interface SantatiOptions {
  /** Team API key (`sat_sk_…`); required and non-empty. */
  apiKey: string;
  /** Defaults to `https://api.santati.io`; a trailing `/` is ignored. */
  baseUrl?: string;
  /** Default trail for emits; never applied to reads. */
  trail?: string;
  /** Per attempt. */
  timeoutMs?: number;
  /** Retries after the first attempt. */
  maxRetries?: number;
  initialBackoffMs?: number;
  maxBackoffMs?: number;
  /** Extra headers on every request; may not carry `authorization`. */
  headers?: Record<string, string>;
  /** When set, `events.emit` stores events here for the background worker instead of sending them. */
  outbox?: OutboxStore;
  /** Envelopes per outbox request, `1..500`; default 100. */
  batchSize?: number;
  /** The background worker's tick, `> 0`; default 1000. */
  flushIntervalMs?: number;
  /** Runs per event before its request; return the event, or null to drop it. */
  preSend?: PreSendHook;
  /** Runs per event after its attempt, with the outcome; exceptions are ignored. */
  postSend?: PostSendHook;
}

export interface ClientConfig extends RetryOptions {
  apiKey: string;
  baseUrl: string;
  trail?: string;
  timeoutMs: number;
  headers: Record<string, string>;
  outbox?: OutboxStore;
  batchSize: number;
  flushIntervalMs: number;
  preSend?: PreSendHook;
  postSend?: PostSendHook;
}

function resolve(options: SantatiOptions): ClientConfig {
  if (typeof options.apiKey !== "string" || options.apiKey.length === 0) {
    throw new ValidationError("apiKey must be a non-empty string", { field: "api_key" });
  }
  const headers = options.headers ?? {};
  for (const name of Object.keys(headers)) {
    if (name.toLowerCase() === "authorization") {
      throw new ValidationError(`headers must not set ${name}: the client sends it`, {
        field: "headers",
      });
    }
  }
  const batchSize = options.batchSize ?? DEFAULT_BATCH_SIZE;
  if (!Number.isInteger(batchSize) || batchSize < 1 || batchSize > 500) {
    throw new ValidationError("batchSize must be between 1 and 500", { field: "batch_size" });
  }
  const flushIntervalMs = options.flushIntervalMs ?? DEFAULT_FLUSH_INTERVAL_MS;
  if (!(flushIntervalMs > 0)) {
    throw new ValidationError("flushIntervalMs must be greater than 0", {
      field: "flush_interval_ms",
    });
  }
  return {
    apiKey: options.apiKey,
    baseUrl: (options.baseUrl ?? DEFAULT_BASE_URL).replace(/\/+$/, ""),
    trail: options.trail,
    timeoutMs: options.timeoutMs ?? DEFAULT_TIMEOUT_MS,
    maxRetries: options.maxRetries ?? DEFAULT_MAX_RETRIES,
    initialBackoffMs: options.initialBackoffMs ?? DEFAULT_INITIAL_BACKOFF_MS,
    maxBackoffMs: options.maxBackoffMs ?? DEFAULT_MAX_BACKOFF_MS,
    headers,
    outbox: options.outbox,
    batchSize,
    flushIntervalMs,
    preSend: options.preSend,
    postSend: options.postSend,
  };
}

function actorRequest(actor: ActorInput): EventActorRequest {
  const request: EventActorRequest = { type: actor.type };
  if (actor.id != null) request.id = actor.id;
  if (actor.name != null) request.name = actor.name;
  if (actor.metadata != null) request.metadata = actor.metadata;
  return request;
}

function targetRequest(target: TargetInput): EventTargetRequest {
  const request: EventTargetRequest = { type: target.type, id: target.id };
  if (target.name != null) request.name = target.name;
  if (target.metadata != null) request.metadata = target.metadata;
  return request;
}

/**
 * The generated envelope for the wire: exactly the supplied members, `trail`
 * resolved against the client's default, and the replay key set. Absent and
 * null inputs are omitted, never sent as `null`.
 */
function buildEnvelope(
  defaultTrail: string | undefined,
  input: EventInput,
  fieldPrefix: string,
  idempotencyKey: string,
): EventEnvelopeRequest {
  if (input.event == null || input.event.length === 0) {
    throw new ValidationError("event must be a non-empty string", {
      field: `${fieldPrefix}event`,
    });
  }
  const trail = input.trail != null && input.trail.length > 0 ? input.trail : defaultTrail;
  if (trail == null || trail.length === 0) {
    throw new ValidationError("trail must be set on the event or the client", {
      field: `${fieldPrefix}trail`,
    });
  }
  const envelope: EventEnvelopeRequest = { event: input.event, trail };
  if (input.organizationId != null) envelope.organizationId = input.organizationId;
  if (input.actor != null) envelope.actor = actorRequest(input.actor);
  if (input.targets != null) envelope.targets = input.targets.map(targetRequest);
  if (input.metadata != null) envelope.metadata = input.metadata;
  if (input.data != null) envelope.data = input.data;
  if (input.context != null) envelope.context = input.context;
  if (input.createdAt != null) envelope.createdAt = input.createdAt;
  envelope.idempotencyKey = idempotencyKey;
  if (input.schemaVersion != null) envelope.schemaVersion = input.schemaVersion;
  return envelope;
}

/**
 * A stored event (`trail` and `idempotencyKey` filled in) as its wire envelope
 * (snake_case), the form custom `OutboxStore`s should persist.
 */
export function envelopeToWire(input: EventInput): Record<string, unknown> {
  return EventEnvelopeRequestToJSON(
    buildEnvelope(undefined, input, "", input.idempotencyKey as string),
  ) as unknown as Record<string, unknown>;
}

/** The inverse of `envelopeToWire`. */
export function envelopeFromWire(json: unknown): EventInput {
  const envelope = EventEnvelopeRequestFromJSON(json);
  const input: EventInput = { event: envelope.event };
  if (envelope.trail != null) input.trail = envelope.trail;
  if (envelope.organizationId != null) input.organizationId = envelope.organizationId;
  if (envelope.actor != null) input.actor = envelope.actor as ActorInput;
  if (envelope.targets != null) input.targets = envelope.targets as TargetInput[];
  if (envelope.metadata != null) input.metadata = envelope.metadata;
  if (envelope.data != null) input.data = envelope.data;
  if (envelope.context != null) input.context = envelope.context;
  if (envelope.createdAt != null) input.createdAt = envelope.createdAt as unknown as string;
  if (envelope.idempotencyKey != null) input.idempotencyKey = envelope.idempotencyKey;
  if (envelope.schemaVersion != null) input.schemaVersion = envelope.schemaVersion;
  return input;
}

/** Only the supplied filters, in the generated request's own camelCase. */
function listRequest(params: ListParams): EventsListRequest {
  const request: EventsListRequest = {};
  if (params.actorId != null) request.actorId = params.actorId;
  if (params.actorType != null) request.actorType = params.actorType;
  if (params.createdAfter != null) request.createdAfter = params.createdAfter;
  if (params.createdBefore != null) request.createdBefore = params.createdBefore;
  if (params.cursor != null) request.cursor = params.cursor;
  if (params.event != null) request.event = params.event;
  if (params.eventPrefix != null) request.eventPrefix = params.eventPrefix;
  if (params.limit != null) request.limit = params.limit;
  if (params.organizationId != null) request.organizationId = params.organizationId;
  if (params.q != null) request.q = params.q;
  if (params.sort != null) request.sort = params.sort as EventsListRequest["sort"];
  if (params.targetId != null) request.targetId = params.targetId;
  if (params.targetType != null) request.targetType = params.targetType;
  if (params.trail != null) request.trail = params.trail;
  return request;
}

/**
 * The `events` resource. One generated configuration/client pair backs it, so
 * two clients never share a base URL, key or headers.
 */
export class Events {
  private readonly api: AuditEventsApi;
  private readonly config: ClientConfig;
  /** The outbox worker when the client has an `outbox`. @internal */
  readonly outbox: OutboxWorker | undefined;

  constructor(config: ClientConfig) {
    this.config = config;
    this.api = new AuditEventsApi(coreConfiguration(config));
    this.outbox =
      config.outbox === undefined
        ? undefined
        : new OutboxWorker(
            this,
            config.outbox,
            config.batchSize,
            config.flushIntervalMs,
            config.preSend,
            config.postSend,
          );
  }

  /**
   * Indexes one event. A replay of the same key answers `duplicate: true`.
   * With an `outbox`, stores the event for the background worker and returns
   * at once with `queued: true` and no event; never makes a request. Rejects
   * with `OutboxError` when the store refuses or the client is closed.
   */
  async emit(input: EventInput): Promise<EmitResult> {
    const idempotencyKey = input.idempotencyKey ?? crypto.randomUUID();
    const eventIngestRequest = buildEnvelope(this.config.trail, input, "", idempotencyKey);
    if (this.outbox !== undefined) {
      // A snapshot, so later changes to the caller's objects do not reach the stored event.
      await this.outbox.enqueue(
        structuredClone({ ...input, trail: eventIngestRequest.trail, idempotencyKey }),
      );
      return { event: null, duplicate: false, idempotencyKey, queued: true };
    }
    const raw = await withRetries(
      () =>
        send(() =>
          this.api.eventsCreateRaw({ eventIngestRequest }, requestInit(this.config.timeoutMs)),
        ),
      this.config,
    );
    if (raw.status !== 200 && raw.status !== 201) throw unexpected(raw);
    return {
      event: decodeJson(raw, AuditEventFromJSON),
      duplicate: raw.status === 200,
      idempotencyKey,
      queued: false,
    };
  }

  /** Indexes up to 500 events; a `207` is a per-item result, not an error. */
  async emitBatch(events: EventInput[]): Promise<BatchResult> {
    return (await this.emitBatchWithStatus(events)).result;
  }

  /** `emitBatch` plus the response's HTTP status (202 or 207). Used by the outbox worker, which sends once (`retries = false`). @internal */
  async emitBatchWithStatus(
    events: EventInput[],
    retries = true,
  ): Promise<{ result: BatchResult; status: number }> {
    if (events.length === 0) {
      throw new ValidationError("events must not be empty", { field: "events" });
    }
    const envelopes = events.map((event, index) =>
      buildEnvelope(this.config.trail, event, `events[${index}].`, event.idempotencyKey ?? crypto.randomUUID()),
    );
    const raw = await withRetries(
      () =>
        send(() =>
          this.api.eventsCreateRaw(
            { eventIngestRequest: { events: envelopes } },
            requestInit(this.config.timeoutMs),
          ),
        ),
      retries ? this.config : { ...this.config, maxRetries: 0 },
    );
    if (raw.status !== 202 && raw.status !== 207) throw unexpected(raw);
    const result: EventBatchResult = decodeJson(raw, EventBatchResultFromJSON);
    return {
      status: raw.status,
      result: {
        accepted: result.accepted,
        rejected: result.rejected,
        results: result.results.map((item) => ({
          index: item.index,
          status: item.status,
          id: item.id,
          error: item.error && { code: item.error.code, message: item.error.message, field: item.error.field },
        })),
      },
    };
  }

  /** One page of events. The client's default trail does not apply here. */
  async list(params: ListParams = {}): Promise<EventPage> {
    const raw = await withRetries(
      () => send(() => this.api.eventsListRaw(listRequest(params), requestInit(this.config.timeoutMs))),
      this.config,
    );
    if (raw.status !== 200) throw unexpected(raw);
    const page = decodeJson(raw, PaginatedAuditEventListFromJSON);
    return { results: page.results, nextCursor: nextCursor(raw, page.next) };
  }

  /** Every matching event, page by page; a later failure surfaces after earlier yields. */
  async *iterate(params: IterateParams = {}): AsyncGenerator<AuditEvent, void, undefined> {
    let cursor: string | null = null;
    for (;;) {
      const page = await this.list(cursor === null ? params : { ...params, cursor });
      yield* page.results;
      if (page.nextCursor === null) return;
      cursor = page.nextCursor;
    }
  }
}

/** An audit-log client for one team API key. */
export class Santati {
  /** Emit, batch, list and iterate on `/api/v0/events/`. */
  readonly events: Events;
  /** Event definitions, schema versions and standard packs on `/api/v0/event-definitions/` and `/api/v0/standard-events/`. */
  readonly schemas: Schemas;

  constructor(options: SantatiOptions) {
    const config = resolve(options);
    this.events = new Events(config);
    this.schemas = new Schemas(config);
  }

  /** Runs one outbox pass now. Rejects with `OutboxError` if the store fails. No-op without an outbox. */
  flush(): Promise<void> {
    return this.events.outbox?.flush() ?? Promise.resolve();
  }

  /**
   * Stops the outbox worker, then flushes once. Idempotent; a queued `emit`
   * afterwards rejects with `closed`. No-op without an outbox.
   */
  close(): Promise<void> {
    return this.events.outbox?.close() ?? Promise.resolve();
  }
}
