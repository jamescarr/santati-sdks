/**
 * What the `events` and `schemas` resources share: the generated core's
 * configuration, one attempt's transport and error mapping, and the decoders.
 * The retry loop is `withRetries`.
 */
import { Configuration, FetchError, ResponseError } from "./core/index.js";
import type { ClientConfig } from "./client.js";
import {
  ApiError,
  AuthError,
  NotFoundError,
  RateLimitedError,
  SantatiError,
  ServerError,
  TransportError,
  ValidationError,
  validationKind,
} from "./errors.js";
import type { SantatiErrorOptions } from "./errors.js";
import { VERSION } from "./version.js";

/** A successful response, or the failed one an `ApiResponse` was never built from. */
export interface RawResponse {
  status: number;
  headers: Headers;
  text: string;
}

/**
 * The generated core's configuration for one client. It applies the spec's
 * `ApiKeyAuth` scheme from `apiKey`; `User-Agent` cannot be set per call, so it
 * rides on the configuration.
 */
export function coreConfiguration(config: ClientConfig): Configuration {
  return new Configuration({
    basePath: config.baseUrl,
    apiKey: () => `Api-Key ${config.apiKey}`,
    headers: {
      "User-Agent": `santati-typescript/${VERSION}`,
      ...config.headers,
    },
  });
}

/** Per attempt: the timeout, and no redirect is ever followed. */
export function requestInit(timeoutMs: number): RequestInit {
  return { signal: AbortSignal.timeout(timeoutMs), redirect: "manual" };
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
  const kind = errorKind(raw.status);
  const Kind = kind === ValidationError ? validationKind(body.code) : kind;
  return new Kind(body.message ?? `HTTP ${raw.status}`, {
    status: raw.status,
    code: body.code,
    field: body.field,
    retryAfter: retryAfter !== null && /^\d+$/.test(retryAfter) ? Number(retryAfter) : null,
  });
}

/** A 2xx status the operation has no meaning for. */
export function unexpected(raw: RawResponse): SantatiError {
  return new ApiError(`unexpected status ${raw.status} for this operation`, {
    status: raw.status,
  });
}

export function decodeJson<T>(raw: RawResponse, fromJSON: (json: unknown) => T): T {
  try {
    return fromJSON(JSON.parse(raw.text));
  } catch {
    throw new ApiError("could not decode the response body", { status: raw.status });
  }
}

/** The `cursor` query parameter of the response's `next` URL, or null. */
export function nextCursor(raw: RawResponse, next: string | null | undefined): string | null {
  if (next == null) return null;
  try {
    return new URL(next).searchParams.get("cursor");
  } catch {
    throw new ApiError("could not decode the response body", { status: raw.status });
  }
}

/** One attempt: the generated call, with its failure modes mapped to kinds. */
export async function send(attempt: () => Promise<{ raw: Response }>): Promise<RawResponse> {
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
