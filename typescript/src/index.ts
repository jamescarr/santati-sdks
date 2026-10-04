export { Santati, envelopeFromWire, envelopeToWire } from "./client.js";
export type { SantatiOptions } from "./client.js";
export {
  ApiError,
  AuthError,
  NotFoundError,
  OutboxError,
  RateLimitedError,
  SantatiError,
  ServerError,
  TransportError,
  ValidationError,
} from "./errors.js";
export type { SantatiErrorOptions } from "./errors.js";
export { MemoryOutbox } from "./outbox.js";
export type {
  ActorInput,
  BatchItem,
  BatchItemError,
  BatchItemStatus,
  BatchResult,
  EmitResult,
  EventInput,
  EventPage,
  IterateParams,
  ListParams,
  OutboxEntry,
  OutboxStore,
  PostSendHook,
  PreSendHook,
  SendOutcome,
  SendStatus,
  TargetInput,
} from "./types.js";
export { VERSION } from "./version.js";

// The generated read models, re-exported: `AuditEventToJSON` is the wire
// serializer the conformance runner compares with.
export { AuditEventToJSON } from "./core/index.js";
export type { AuditEvent, EventActor, EventTarget } from "./core/index.js";
