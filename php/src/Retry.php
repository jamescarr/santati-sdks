<?php

declare(strict_types=1);

namespace Santati;

use Santati\Exception\RateLimitedException;
use Santati\Exception\SantatiException;
use Santati\Exception\ServerException;
use Santati\Exception\TransportException;

/**
 * The SDK's retry policy: every attempt re-sends the identical request.
 *
 * Retryable: transport failures, 500/502/503/504 and 429 unless the error code
 * is `quota_exceeded`. Before retry *n* a `Retry-After` value wins outright,
 * unless it exceeds the configured backoff cap.
 */
final class Retry
{
    public function __construct(
        public readonly int $maxRetries = 2,
        public readonly int $initialBackoffMs = 250,
        public readonly int $maxBackoffMs = 8000,
    ) {
    }

    /**
     * Runs one logical request, retrying a retryable failure.
     *
     * @template T
     *
     * @param callable():T $attempt
     *
     * @return T
     *
     * @throws SantatiException the last failure, once retries are exhausted
     */
    public function run(callable $attempt): mixed
    {
        $attempts = 1 + max(0, $this->maxRetries);

        for ($n = 1; ; $n++) {
            try {
                return $attempt();
            } catch (SantatiException $e) {
                if ($n >= $attempts || !$this->retryable($e) || !$this->pauseBefore($n, $e->getRetryAfter())) {
                    throw $e;
                }
            }
        }
    }

    public function retryable(SantatiException $error): bool
    {
        if ($error instanceof TransportException) {
            return true;
        }

        if ($error instanceof ServerException) {
            return in_array($error->getStatus(), [500, 502, 503, 504], true);
        }

        if ($error instanceof RateLimitedException) {
            return $error->getErrorCode() !== 'quota_exceeded';
        }

        return false;
    }

    /**
     * @return bool false when the caller must stop retrying and raise now
     */
    private function pauseBefore(int $attempt, ?int $retryAfter): bool
    {
        if ($retryAfter !== null) {
            $waitMs = $retryAfter * 1000;

            if ($waitMs > $this->maxBackoffMs) {
                return false;
            }

            if ($waitMs > 0) {
                usleep($waitMs * 1000);
            }

            return true;
        }

        $cap = (int) min($this->initialBackoffMs * (2 ** ($attempt - 1)), $this->maxBackoffMs);

        if ($cap > 0) {
            usleep(random_int(0, $cap) * 1000);
        }

        return true;
    }
}
