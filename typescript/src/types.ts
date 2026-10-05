/** The facade's public shapes. Wire (snake_case) names are the generated core's job. */
import type { AuditEvent } from "./core/index.js";

/** Who acted on an event. */
export interface ActorInput {
  type: string;
  id?: string;
  name?: string;
  metadata?: Record<string, string>;
}

/** One object an event acted on. */
export interface TargetInput {
  type: string;
  id: string;
  name?: string;
  metadata?: Record<string, string>;
}

/** One event to emit, on its own or in a batch. */
export interface EventInput {
  event: string;
  trail?: string;
  organizationId?: string;
  actor?: ActorInput;
  targets?: TargetInput[];
  metadata?: Record<string, string>;
  data?: unknown;
  context?: Record<string, unknown>;
  createdAt?: string;
  idempotencyKey?: string;
}

/** Filters for `list` and `iterate`; absent members are not sent. */
export interface ListParams {
  trail?: string;
  event?: string;
  eventPrefix?: string;
  organizationId?: string;
  actorId?: string;
  actorType?: string;
  targetType?: string;
  targetId?: string;
  createdAfter?: string;
  createdBefore?: string;
  q?: string;
  sort?: string;
  limit?: number;
  cursor?: string;
}

/** `iterate` takes the same filters minus `cursor`: pages are followed for you. */
export type IterateParams = Omit<ListParams, "cursor">;

/**
 * The stored event (`null` when queued), whether the server replayed an
 * earlier request, the key that was used, and whether the event went to the
 * outbox instead of the API.
 */
export interface EmitResult {
  event: AuditEvent | null;
  duplicate: boolean;
  idempotencyKey: string;
  queued: boolean;
}

export interface BatchItemError {
  code: string;
  message: string;
  field?: string | null;
}

export type BatchItemStatus = "accepted" | "duplicate" | "rejected";

export interface BatchItem {
  index: number;
  status: BatchItemStatus;
  id?: string;
  error?: BatchItemError;
}

export interface BatchResult {
  accepted: number;
  rejected: number;
  results: BatchItem[];
}

export interface EventPage {
  results: AuditEvent[];
  nextCursor: string | null;
}

/** One entry an `OutboxStore` hands to the worker: its store id and the stored event. */
export interface OutboxEntry {
  id: string;
  event: EventInput;
}

/**
 * Where a queued `emit` keeps events until the worker sends them. Methods may be
 * synchronous or return a promise. Stored events carry `trail` and
 * `idempotencyKey`; use `envelopeToWire` / `envelopeFromWire` to serialize them.
 */
export interface OutboxStore {
  /** Stores at the tail; throws `OutboxError` (`outbox_full`) or any error. */
  enqueue(event: EventInput): void | Promise<void>;
  /** Up to `limit` oldest entries, FIFO; claimed entries are not returned again until released. */
  claim(limit: number): OutboxEntry[] | Promise<OutboxEntry[]>;
  /** Deletes permanently. */
  ack(ids: string[]): void | Promise<void>;
  /** Makes entries eligible for a later claim. */
  release(ids: string[]): void | Promise<void>;
}

export type SendStatus = "accepted" | "duplicate" | "rejected" | "failed";

/** What happened to one event in one attempt; passed to `postSend`. */
export interface SendOutcome {
  status: SendStatus;
  id?: string;
  error?: import("./errors.js").SantatiError;
}

/** Return the event (possibly modified) to send it; `null` or `undefined` drops it. */
export type PreSendHook = (
  event: EventInput,
) => EventInput | null | undefined | Promise<EventInput | null | undefined>;

export type PostSendHook = (event: EventInput, outcome: SendOutcome) => void | Promise<void>;
