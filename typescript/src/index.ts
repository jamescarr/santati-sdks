export { Santati, envelopeFromWire, envelopeToWire } from "./client.js";
export type { SantatiOptions } from "./client.js";
export {
  ApiError,
  AuthError,
  NotFoundError,
  OutboxError,
  RateLimitedError,
  SantatiError,
  SchemaValidationError,
  ServerError,
  TransportError,
  ValidationError,
} from "./errors.js";
export type { SantatiErrorOptions } from "./errors.js";
export { MemoryOutbox } from "./outbox.js";
export { Schemas } from "./schemas.js";
export type {
  ActorInput,
  BatchItem,
  BatchItemError,
  BatchItemStatus,
  BatchResult,
  DefinitionInput,
  DefinitionPage,
  DefinitionUpdate,
  EmitResult,
  EventInput,
  EventPage,
  IteratePageParams,
  IterateParams,
  ListParams,
  OutboxEntry,
  OutboxStore,
  PageParams,
  PostSendHook,
  PreSendHook,
  SchemaVersionPage,
  SchemaVersionResult,
  SendOutcome,
  SendStatus,
  TargetInput,
} from "./types.js";
export { VERSION } from "./version.js";

// The generated read models, re-exported: the `*ToJSON` functions are the wire
// serializers the conformance runner compares with.
export {
  AuditEventToJSON,
  EventDefinitionToJSON,
  EventSchemaVersionToJSON,
  SchemaCheckToJSON,
  StandardEventCatalogToJSON,
  StandardPackInstallResultToJSON,
} from "./core/index.js";
export type {
  AuditEvent,
  EventActor,
  EventDefinition,
  EventSchemaVersion,
  EventTarget,
  OcsfMapping,
  SchemaCheck,
  SchemaCheckFailure,
  StandardEvent,
  StandardEventCatalog,
  StandardPack,
  StandardPackInstallResult,
} from "./core/index.js";
