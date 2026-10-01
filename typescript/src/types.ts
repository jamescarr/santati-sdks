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

/** The stored event plus whether the server replayed an earlier request. */
export interface EmitResult {
  event: AuditEvent;
  duplicate: boolean;
  idempotencyKey: string;
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
