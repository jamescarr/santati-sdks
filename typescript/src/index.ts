export { Santati } from "./client.js";
export type { SantatiOptions } from "./client.js";
export {
  ApiError,
  AuthError,
  NotFoundError,
  RateLimitedError,
  SantatiError,
  ServerError,
  TransportError,
  ValidationError,
} from "./errors.js";
export type { SantatiErrorOptions } from "./errors.js";
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
  TargetInput,
} from "./types.js";
export { VERSION } from "./version.js";

// The generated read models, re-exported: `AuditEventToJSON` is the wire
// serializer the conformance runner compares with.
export { AuditEventToJSON } from "./core/index.js";
export type { AuditEvent, EventActor, EventTarget } from "./core/index.js";
