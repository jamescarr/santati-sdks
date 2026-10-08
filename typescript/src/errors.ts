/** One base type and nine kinds; `status` is null when no response was answered. */
export interface SantatiErrorOptions {
  status?: number | null;
  code?: string | null;
  field?: string | null;
  retryAfter?: number | null;
}

export class SantatiError extends Error {
  /** The HTTP status, or null for a local validation or transport failure. */
  readonly status: number | null;
  /** The server's error code, when its body carried one. */
  readonly code: string | null;
  /** The request field at fault, when the server named one. */
  readonly field: string | null;
  /** The `Retry-After` header in seconds, when it was an integer. */
  readonly retryAfter: number | null;

  constructor(message: string, options: SantatiErrorOptions = {}) {
    super(message);
    this.name = new.target.name;
    this.status = options.status ?? null;
    this.code = options.code ?? null;
    this.field = options.field ?? null;
    this.retryAfter = options.retryAfter ?? null;
  }
}

/** Something the caller got wrong: a local check, or HTTP 400, 413 or 422. */
export class ValidationError extends SantatiError {}

/**
 * A `ValidationError` the server raised against the action's JSON Schema: the
 * event broke it, named a disallowed target type, or pinned an unusable
 * `schemaVersion` (HTTP 400, 413 or 422 with code `schema_validation_failed`).
 */
export class SchemaValidationError extends ValidationError {}

/** The kind of a 400, 413 or 422 whose body carried `code`. @internal */
export function validationKind(code: string | null | undefined): typeof ValidationError {
  return code === "schema_validation_failed" ? SchemaValidationError : ValidationError;
}

/** HTTP 401 or 403. */
export class AuthError extends SantatiError {}

/** HTTP 404. */
export class NotFoundError extends SantatiError {}

/** HTTP 429. */
export class RateLimitedError extends SantatiError {}

/** HTTP 500–599. */
export class ServerError extends SantatiError {}

/** No HTTP response at all: refused, DNS, TLS, timeout. */
export class TransportError extends SantatiError {}

/** Anything else: another non-2xx, an unexpected 2xx, an undecodable body. */
export class ApiError extends SantatiError {}

/**
 * The outbox store refused or failed (`outbox_full`, `store_unavailable`,
 * `closed`), or a `pre_send` hook threw (`hook_failed`). `status` is null.
 */
export class OutboxError extends SantatiError {}
