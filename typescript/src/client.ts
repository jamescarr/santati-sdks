import {
  AuditEventFromJSON,
  AuditEventsApi,
  Configuration,
  EventBatchResultFromJSON,
  EventEnvelopeRequestFromJSON,
  EventEnvelopeRequestToJSON,
  FetchError,
  PaginatedAuditEventListFromJSON,
  ResponseError,
} from "./core/index.js";
import type {
  AuditEvent,
  EventActorRequest,
  EventBatchResult,
  EventEnvelopeRequest,
  EventTargetRequest,
  EventsListRequest,
} from "./core/index.js";
import {
  ApiError,
  AuthError,
  NotFoundError,
  RateLimitedError,
  SantatiError,
  ServerError,
  TransportError,
  ValidationError,
} from "./errors.js";
import type { SantatiErrorOptions } from "./errors.js";
import { MemoryOutbox, OutboxWorker } from "./outbox.js";
import { withRetries, type RetryOptions } from "./retry.js";
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
  /** Where `log` keeps events until sent; default `new MemoryOutbox()`. */
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

interface ClientConfig extends RetryOptions {
  apiKey: string;
  baseUrl: string;
  trail?: string;
  timeoutMs: number;
  headers: Record<string, string>;
  outbox: OutboxStore;
  batchSize: number;
  flushIntervalMs: number;
  preSend?: PreSendHook;
  postSend?: PostSendHook;
}

/** A successful response, or the failed one an `ApiResponse` was never built from. */
interface RawResponse {
  status: number;
  headers: Headers;
  text: string;
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
    outbox: options.outbox ?? new MemoryOutbox(),
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

type ErrorKind = new (message: string, options?: SantatiErrorOptions) => SantatiError;

function errorKind(status: number): ErrorKind {
  if (status === 400 || status === 413 || status === 422) return ValidationError;
  if (status === 401 || status === 403) return AuthError;
  if (status === 404) return NotFoundError;
  if (status === 429) return RateLimitedError;
  if (status >= 500 && status <= 599) return ServerError;
  return ApiError;
}

interface ParsedBody {
  code: string | null;
  field: string | null;
  message: string | null;
}

const NO_BODY: ParsedBody = { code: null, field: null, message: null };

/** The `error` envelope, a `detail`, or nothing at all. */
function errorBody(text: string): ParsedBody {
  let parsed: unknown;
  try {
    parsed = JSON.parse(text);
  } catch {
    return NO_BODY;
  }
  if (parsed === null || typeof parsed !== "object") return NO_BODY;
  if ("error" in parsed) {
    const envelope = parsed.error;
    if (envelope !== null && typeof envelope === "object" && "code" in envelope && typeof envelope.code === "string") {
      const field = "field" in envelope ? envelope.field : null;
      const message = "message" in envelope ? envelope.message : null;
      return {
        code: envelope.code,
        field: typeof field === "string" ? field : null,
        message: typeof message === "string" ? message : null,
      };
    }
  }
  if ("detail" in parsed && typeof parsed.detail === "string") {
    return { code: null, field: null, message: parsed.detail };
  }
  return NO_BODY;
}

/** The mapped failure for a non-2xx answer. */
function failureFrom(raw: RawResponse): SantatiError {
  const body = errorBody(raw.text);
  const retryAfter = raw.headers.get("retry-after");
  const Kind = errorKind(raw.status);
  return new Kind(body.message ?? `HTTP ${raw.status}`, {
    status: raw.status,
    code: body.code,
    field: body.field,
    retryAfter: retryAfter !== null && /^\d+$/.test(retryAfter) ? Number(retryAfter) : null,
  });
}

/** A 2xx status the operation has no meaning for. */
function unexpected(raw: RawResponse): SantatiError {
  return new ApiError(`unexpected status ${raw.status} for this operation`, {
    status: raw.status,
  });
}

function decodeJson<T>(raw: RawResponse, fromJSON: (json: unknown) => T): T {
  try {
    return fromJSON(JSON.parse(raw.text));
  } catch {
    throw new ApiError("could not decode the response body", { status: raw.status });
  }
}

/**
 * The `events` resource. One generated configuration/client pair backs it, so
 * two clients never share a base URL, key or headers.
 */
export class Events {
  private readonly api: AuditEventsApi;
  private readonly config: ClientConfig;

  constructor(config: ClientConfig) {
    this.config = config;
    // The generated core applies the spec's `ApiKeyAuth` scheme from `apiKey`;
    // `User-Agent` cannot be set per call, so it rides on the configuration.
    this.api = new AuditEventsApi(
      new Configuration({
        basePath: config.baseUrl,
        apiKey: () => `Api-Key ${config.apiKey}`,
        headers: {
          "User-Agent": `santati-typescript/${VERSION}`,
          ...config.headers,
        },
      }),
    );
  }

  /** Indexes one event. A replay of the same key answers `duplicate: true`. */
  async emit(input: EventInput): Promise<EmitResult> {
    const idempotencyKey = input.idempotencyKey ?? crypto.randomUUID();
    const eventIngestRequest = buildEnvelope(this.config.trail, input, "", idempotencyKey);
    const raw = await withRetries(
      () => this.send(() => this.api.eventsCreateRaw({ eventIngestRequest }, this.init())),
      this.config,
    );
    if (raw.status !== 200 && raw.status !== 201) throw unexpected(raw);
    return {
      event: decodeJson(raw, AuditEventFromJSON),
      duplicate: raw.status === 200,
      idempotencyKey,
    };
  }

  /** Indexes up to 500 events; a `207` is a per-item result, not an error. */
  async emitBatch(events: EventInput[]): Promise<BatchResult> {
    return (await this.emitBatchWithStatus(events)).result;
  }

  /** `emitBatch` plus the response's HTTP status (202 or 207). Used by the outbox worker. @internal */
  async emitBatchWithStatus(
    events: EventInput[],
  ): Promise<{ result: BatchResult; status: number }> {
    if (events.length === 0) {
      throw new ValidationError("events must not be empty", { field: "events" });
    }
    const envelopes = events.map((event, index) =>
      buildEnvelope(this.config.trail, event, `events[${index}].`, event.idempotencyKey ?? crypto.randomUUID()),
    );
    const raw = await withRetries(
      () =>
        this.send(() =>
          this.api.eventsCreateRaw({ eventIngestRequest: { events: envelopes } }, this.init()),
        ),
      this.config,
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
      () => this.send(() => this.api.eventsListRaw(listRequest(params), this.init())),
      this.config,
    );
    if (raw.status !== 200) throw unexpected(raw);
    const page = decodeJson(raw, PaginatedAuditEventListFromJSON);
    return { results: page.results, nextCursor: this.nextCursor(raw, page.next) };
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

  /** The `cursor` query parameter of the response's `next` URL, or null. */
  private nextCursor(raw: RawResponse, next: string | null | undefined): string | null {
    if (next == null) return null;
    try {
      return new URL(next).searchParams.get("cursor");
    } catch {
      throw new ApiError("could not decode the response body", { status: raw.status });
    }
  }

  private init(): RequestInit {
    return { signal: AbortSignal.timeout(this.config.timeoutMs), redirect: "manual" };
  }

  /** One attempt: the generated call, with its failure modes mapped to kinds. */
  private async send(attempt: () => Promise<{ raw: Response }>): Promise<RawResponse> {
    const response = await attempt()
      .then(({ raw }) => raw)
      .catch((error: unknown) => {
        // A non-2xx answer is an error object carrying the response, not a
        // throw of its own; anything fetch could not answer at all is transport.
        if (error instanceof ResponseError) return error.response;
        if (error instanceof FetchError) throw new TransportError(error.cause?.message ?? error.message);
        throw error;
      });
    const raw = {
      status: response.status,
      headers: response.headers,
      text: await response.text(),
    };
    // Classified here, inside the attempt, so the retry loop sees kinds.
    if (raw.status < 200 || raw.status >= 300) throw failureFrom(raw);
    return raw;
  }
}

/** An audit-log client for one team API key. */
export class Santati {
  /** Emit, batch, list and iterate on `/api/v0/events/`. */
  readonly events: Events;
  private readonly config: ClientConfig;
  private readonly outbox: OutboxWorker;

  constructor(options: SantatiOptions) {
    this.config = resolve(options);
    this.events = new Events(this.config);
    this.outbox = new OutboxWorker(
      this.events,
      this.config.outbox,
      this.config.batchSize,
      this.config.flushIntervalMs,
      this.config.preSend,
      this.config.postSend,
    );
  }

  /**
   * Fire-and-forget emit: validates like `events.emit`, stores the resolved
   * event in the outbox and returns its idempotency key. Never makes a
   * request; a background worker sends it. Rejects with `OutboxError` when the
   * store refuses or the client is closed.
   */
  async log(input: EventInput): Promise<string> {
    const key = input.idempotencyKey ?? crypto.randomUUID();
    const envelope = buildEnvelope(this.config.trail, input, "", key);
    return this.outbox.log({ ...input, trail: envelope.trail, idempotencyKey: key });
  }

  /** Runs one outbox pass now. Rejects with `OutboxError` if the store fails. */
  flush(): Promise<void> {
    return this.outbox.flush();
  }

  /** Stops the worker, then flushes once. Idempotent; `log` afterwards rejects with `closed`. */
  close(): Promise<void> {
    return this.outbox.close();
  }
}
