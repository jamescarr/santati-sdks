import { setTimeout as delay } from "node:timers/promises";

import { SantatiError, TransportError } from "./errors.js";

export interface RetryOptions {
  /** Retries after the first attempt. */
  maxRetries: number;
  /** Backoff base; retry *n* waits in `[0, min(initial * 2^(n-1), max)]`. */
  initialBackoffMs: number;
  /** Backoff cap, and the largest `Retry-After` this client will honour. */
  maxBackoffMs: number;
}

/** Transport failures, 500/502/503/504, and 429 unless the quota is exhausted. */
export function isRetryable(error: SantatiError): boolean {
  if (error instanceof TransportError) return true;
  if (error.status === 429) return error.code !== "quota_exceeded";
  return (
    error.status === 500 || error.status === 502 || error.status === 503 || error.status === 504
  );
}

/** A uniform random integer in `[0, min(initial * 2^(retry-1), max)]`. */
function backoffMs(retry: number, options: RetryOptions): number {
  const cap = Math.min(options.initialBackoffMs * 2 ** (retry - 1), options.maxBackoffMs);
  return Math.floor(Math.random() * (cap + 1));
}

/**
 * Runs `attempt` until it succeeds or the retries are exhausted. Every attempt
 * re-sends the identical request, so `attempt` must build nothing per attempt.
 */
export async function withRetries<T>(
  attempt: () => Promise<T>,
  options: RetryOptions,
): Promise<T> {
  for (let retry = 1; ; retry += 1) {
    try {
      return await attempt();
    } catch (error) {
      if (
        !(error instanceof SantatiError) ||
        !isRetryable(error) ||
        retry > options.maxRetries
      ) {
        throw error;
      }
      const wait = error.retryAfter === null ? backoffMs(retry, options) : error.retryAfter * 1000;
      // A server-requested wait past the cap means this client is done waiting.
      if (error.retryAfter !== null && wait > options.maxBackoffMs) throw error;
      await delay(wait);
    }
  }
}
