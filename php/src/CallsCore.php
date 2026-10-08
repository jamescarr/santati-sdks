<?php

declare(strict_types=1);

namespace Santati;

use Santati\Core\ApiException as CoreApiException;
use Santati\Exception\ApiException;
use Santati\Exception\AuthException;
use Santati\Exception\NotFoundException;
use Santati\Exception\RateLimitedException;
use Santati\Exception\SantatiException;
use Santati\Exception\SchemaValidationException;
use Santati\Exception\ServerException;
use Santati\Exception\TransportException;
use Santati\Exception\ValidationException;

/**
 * What the resources that call the generated core share: one attempt with the
 * core's and Guzzle's failures mapped to the SDK's exceptions, the error-body
 * and header readers, the cursor reader and the empty-object helper.
 *
 * @internal
 */
trait CallsCore
{
    /**
     * Runs one attempt, mapping everything the generated core and Guzzle throw.
     *
     * @param callable():array{0: mixed, 1: int, 2: array<string, array<int, string>>} $attempt
     *
     * @return array{0: mixed, 1: int, 2: array<string, array<int, string>>} the data, the status and the response headers
     */
    private function send(callable $attempt): array
    {
        try {
            return $attempt();
        } catch (\Throwable $e) {
            throw $this->convert($e);
        }
    }

    private function convert(\Throwable $e): SantatiException
    {
        if ($e instanceof SantatiException) {
            return $e;
        }

        if ($e instanceof \InvalidArgumentException) {
            return new ValidationException($e->getMessage(), null, null, self::fieldOf($e->getMessage()), null, $e);
        }

        if ($e instanceof CoreApiException) {
            return $this->fromCore($e);
        }

        if ($e instanceof \GuzzleHttp\Exception\GuzzleException) {
            return new TransportException($e->getMessage(), null, null, null, null, $e);
        }

        throw $e;
    }

    private function fromCore(CoreApiException $e): SantatiException
    {
        $status = (int) $e->getCode();

        if ($status === 0) {
            return new TransportException($e->getMessage(), null, null, null, null, $e);
        }

        [$code, $field, $message] = self::parseErrorBody($e->getResponseBody(), $status);
        $retryAfter = self::retryAfter($e->getResponseHeaders());

        return match (true) {
            in_array($status, [400, 413, 422], true) => $code === 'schema_validation_failed'
                ? new SchemaValidationException($message, $status, $code, $field, $retryAfter, $e)
                : new ValidationException($message, $status, $code, $field, $retryAfter, $e),
            $status === 401, $status === 403 => new AuthException($message, $status, $code, $field, $retryAfter, $e),
            $status === 404 => new NotFoundException($message, $status, $code, $field, $retryAfter, $e),
            $status === 429 => new RateLimitedException($message, $status, $code, $field, $retryAfter, $e),
            $status >= 500 && $status <= 599 => new ServerException($message, $status, $code, $field, $retryAfter, $e),
            default => new ApiException($message, $status, $code, $field, $retryAfter, $e),
        };
    }

    /**
     * @param mixed $body the response body as the generated core handed it over
     *
     * @return array{0: ?string, 1: ?string, 2: string} code, field, message
     */
    private static function parseErrorBody(mixed $body, int $status): array
    {
        if ($body instanceof \stdClass) {
            $decoded = $body;
        } elseif (is_string($body) && $body !== '') {
            $decoded = json_decode($body);
        } else {
            $decoded = null;
        }

        if ($decoded instanceof \stdClass) {
            $error = $decoded->error ?? null;

            if ($error instanceof \stdClass && isset($error->code) && is_string($error->code)) {
                return [
                    $error->code,
                    isset($error->field) && is_string($error->field) ? $error->field : null,
                    isset($error->message) && is_string($error->message) ? $error->message : 'HTTP ' . $status,
                ];
            }

            if (isset($decoded->detail) && is_string($decoded->detail)) {
                return [null, null, $decoded->detail];
            }
        }

        return [null, null, 'HTTP ' . $status];
    }

    /**
     * @param array<string, array<int, string>>|null $headers
     */
    private static function retryAfter(?array $headers): ?int
    {
        $value = self::header($headers, 'retry-after');

        return $value !== null && preg_match('/^\d+$/', $value) === 1 ? (int) $value : null;
    }

    /**
     * The first value of the response header `$name` (compared case-insensitively), or null.
     *
     * @param array<string, array<int, string>>|null $headers
     */
    private static function header(?array $headers, string $name): ?string
    {
        if (!is_array($headers)) {
            return null;
        }

        foreach ($headers as $key => $values) {
            if (strcasecmp((string) $key, $name) === 0) {
                return is_array($values) ? (string) reset($values) : (string) $values;
            }
        }

        return null;
    }

    /**
     * Best-effort field name out of a generated setter's message, e.g.
     * `invalid length for $name when calling EventActorRequest…` → `name`.
     */
    private static function fieldOf(string $message): ?string
    {
        return preg_match('/\$([A-Za-z_][A-Za-z0-9_]*)/', $message, $matches) === 1 ? $matches[1] : null;
    }

    private static function nextCursor(?string $next): ?string
    {
        if ($next === null || $next === '') {
            return null;
        }

        $query = parse_url($next, PHP_URL_QUERY);

        if (!is_string($query) || $query === '') {
            return null;
        }

        $parsed = [];
        parse_str($query, $parsed);
        $cursor = $parsed['cursor'] ?? null;

        return is_string($cursor) && $cursor !== '' ? $cursor : null;
    }

    /**
     * PHP cannot tell an empty map from an empty list in JSON, so an empty
     * object member is sent as `{}`.
     *
     * @return array<string, mixed>|\stdClass
     */
    private static function mapOrObject(array $value): array|\stdClass
    {
        return $value === [] ? new \stdClass() : $value;
    }
}
