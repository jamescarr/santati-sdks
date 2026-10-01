<?php

declare(strict_types=1);

namespace Santati;

use GuzzleHttp\Client as HttpClient;
use Santati\Core\Api\AuditEventsApi;
use Santati\Core\Configuration;
use Santati\Exception\ValidationException;

/**
 * A Santati API client: one API key, one generated core client.
 */
final class Client
{
    public const DEFAULT_BASE_URL = 'https://api.santati.io';

    public readonly string $baseUrl;

    /**
     * The default trail for emits; never applied to reads.
     */
    public readonly ?string $trail;

    public readonly Retry $retry;

    public readonly Events $events;

    /**
     * The generated core API client, wired to this client's configuration.
     */
    public readonly AuditEventsApi $api;

    /**
     * @param string                $apiKey           team API key (`sat_sk_…`), required and non-empty
     * @param string                $baseUrl          may carry a path prefix; a trailing slash is dropped
     * @param string|null           $trail            default trail for emits
     * @param int                   $timeoutMs        per attempt
     * @param int                   $maxRetries       retries after the first attempt
     * @param int                   $initialBackoffMs backoff base
     * @param int                   $maxBackoffMs     backoff cap
     * @param array<string, string> $headers          extra headers on every request
     *
     * @throws ValidationException on an empty $apiKey or an `authorization` header
     */
    public function __construct(
        string $apiKey,
        string $baseUrl = self::DEFAULT_BASE_URL,
        ?string $trail = null,
        int $timeoutMs = 10000,
        int $maxRetries = 2,
        int $initialBackoffMs = 250,
        int $maxBackoffMs = 8000,
        array $headers = [],
    ) {
        if ($apiKey === '') {
            throw new ValidationException('api_key must not be empty', null, null, 'api_key');
        }

        foreach (array_keys($headers) as $name) {
            if (strcasecmp((string) $name, 'authorization') === 0) {
                throw new ValidationException("headers must not set 'authorization'", null, null, 'headers');
            }
        }

        $this->baseUrl = rtrim($baseUrl, '/');
        $this->trail = $trail === null || $trail === '' ? null : $trail;
        $this->retry = new Retry($maxRetries, $initialBackoffMs, $maxBackoffMs);

        // The generated core applies the spec's ApiKeyAuth scheme from this key and prefix.
        $config = (new Configuration())
            ->setHost($this->baseUrl)
            ->setApiKey('Authorization', $apiKey)
            ->setApiKeyPrefix('Authorization', 'Api-Key')
            ->setUserAgent(self::userAgent());

        $timeout = $timeoutMs / 1000;
        $http = new HttpClient([
            // A 3xx is a response, never a replay of the key to another host.
            'allow_redirects' => false,
            'timeout' => $timeout,
            'connect_timeout' => $timeout,
            'headers' => self::mergeHeaders($headers),
        ]);

        $this->api = new AuditEventsApi($http, $config);
        $this->events = new Events($this);
    }

    public static function userAgent(): string
    {
        return 'santati-php/' . Version::VERSION;
    }

    /**
     * @param array<string, string> $headers
     *
     * @return array<string, string>
     */
    private static function mergeHeaders(array $headers): array
    {
        $merged = ['User-Agent' => self::userAgent()];

        foreach ($headers as $name => $value) {
            $name = (string) $name;

            foreach (array_keys($merged) as $existing) {
                if (strcasecmp((string) $existing, $name) === 0) {
                    unset($merged[$existing]);
                }
            }

            $merged[$name] = (string) $value;
        }

        return $merged;
    }
}
