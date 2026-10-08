<?php

declare(strict_types=1);

namespace Santati;

use GuzzleHttp\Client as HttpClient;
use Santati\Core\Api\AuditEventsApi;
use Santati\Core\Api\EventDefinitionsApi;
use Santati\Core\Configuration;
use Santati\Exception\OutboxException;
use Santati\Exception\ValidationException;
use Santati\Outbox\Outbox;
use Santati\Outbox\OutboxStore;

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
     * The generated core API client for event definitions, schema versions and
     * standard packs, wired to this client's configuration.
     */
    public readonly EventDefinitionsApi $definitionsApi;

    /**
     * `$client->schemas`: event definitions, their schema versions and the standard packs.
     */
    public readonly Schemas $schemas;

    /**
     * @internal the outbox behind a queued `events->emit()`; null without an `outbox`
     */
    public readonly ?Outbox $outbox;

    /**
     * @param string                $apiKey           team API key (`sat_sk_…`), required and non-empty
     * @param string                $baseUrl          may carry a path prefix; a trailing slash is dropped
     * @param string|null           $trail            default trail for emits
     * @param int                   $timeoutMs        per attempt
     * @param int                   $maxRetries       retries after the first attempt
     * @param int                   $initialBackoffMs backoff base
     * @param int                   $maxBackoffMs     backoff cap
     * @param array<string, string> $headers          extra headers on every request
     * @param OutboxStore|null      $outbox           when set, `events->emit()` stores events here instead of sending them; default none
     * @param int                   $batchSize        envelopes per outbox request, 1 to 500
     * @param callable|null         $preSend          `fn (array $event): ?array`, return the event (maybe modified) or null to drop it
     * @param callable|null         $postSend         `fn (array $event, SendOutcome $outcome): void`
     * @param bool                  $finishRequestBeforeFlush when true and `fastcgi_finish_request()` exists (PHP-FPM), the shutdown flush first finishes the HTTP response
     *
     * @throws ValidationException on an empty $apiKey, an `authorization` header or a $batchSize outside 1..500
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
        ?OutboxStore $outbox = null,
        int $batchSize = 100,
        ?callable $preSend = null,
        ?callable $postSend = null,
        bool $finishRequestBeforeFlush = false,
    ) {
        if ($apiKey === '') {
            throw new ValidationException('api_key must not be empty', null, null, 'api_key');
        }

        foreach (array_keys($headers) as $name) {
            if (strcasecmp((string) $name, 'authorization') === 0) {
                throw new ValidationException("headers must not set 'authorization'", null, null, 'headers');
            }
        }

        if ($batchSize < 1 || $batchSize > 500) {
            throw new ValidationException('batch_size must be between 1 and 500', null, null, 'batch_size');
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
        $this->definitionsApi = new EventDefinitionsApi($http, $config);
        $this->events = new Events($this);
        $this->schemas = new Schemas($this);
        $this->outbox = $outbox === null ? null : new Outbox($this->events, $outbox, $batchSize, $preSend, $postSend, $finishRequestBeforeFlush);
    }

    /**
     * Runs one pass: sends everything stored in batches of `batchSize`.
     * Send failures go through the delivery policy and `postSend`; only a
     * failing store raises. Does nothing without an `outbox`.
     *
     * @throws OutboxException when the store fails
     */
    public function flush(): void
    {
        $this->outbox?->flush();
    }

    /**
     * Flushes and marks the client closed; later queued emits raise
     * `OutboxException` (`closed`). Calling it again does nothing, and it does
     * nothing without an `outbox`.
     *
     * @throws OutboxException when the store fails
     */
    public function close(): void
    {
        $this->outbox?->close();
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
