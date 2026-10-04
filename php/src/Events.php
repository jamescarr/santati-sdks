<?php

declare(strict_types=1);

namespace Santati;

use Santati\Core\ApiException as CoreApiException;
use Santati\Core\Model\AuditEvent;
use Santati\Core\Model\EventActorRequest;
use Santati\Core\Model\EventBatchRequest;
use Santati\Core\Model\EventBatchResult;
use Santati\Core\Model\EventEnvelopeRequest;
use Santati\Core\Model\EventTargetRequest;
use Santati\Exception\ApiException;
use Santati\Exception\AuthException;
use Santati\Exception\NotFoundException;
use Santati\Exception\OutboxException;
use Santati\Exception\RateLimitedException;
use Santati\Exception\SantatiException;
use Santati\Exception\ServerException;
use Santati\Exception\TransportException;
use Santati\Exception\ValidationException;

/**
 * `$client->events`: emit one event or a batch, list a page, iterate pages.
 *
 * Inputs are plain arrays with the wire's snake_case keys; absent or null
 * members are omitted (JSON `null` is never sent).
 */
final class Events
{
    /**
     * The `list`/`iterate` parameters, in spec (alphabetical) order.
     */
    public const LIST_PARAMS = [
        'actor_id',
        'actor_type',
        'created_after',
        'created_before',
        'cursor',
        'event',
        'event_prefix',
        'limit',
        'organization_id',
        'q',
        'sort',
        'target_id',
        'target_type',
        'trail',
    ];

    public function __construct(private readonly Client $client)
    {
    }

    /**
     * Emits one audit event. With an `outbox` the event is stored for the next
     * pass instead and the result is `queued`, with no event; no request is made.
     *
     * @param array<string, mixed> $event `event`, plus optional `trail`, `organization_id`, `actor`,
     *                                    `targets`, `metadata`, `data`, `context`, `created_at`, `idempotency_key`
     *
     * @throws ValidationException|AuthException|NotFoundException|RateLimitedException|ServerException|TransportException|ApiException|OutboxException
     */
    public function emit(array $event): EmitResult
    {
        if ($this->client->outbox !== null) {
            $stored = $this->prepare($event);
            $this->client->outbox->enqueue($stored);

            return new EmitResult(event: null, duplicate: false, idempotencyKey: (string) $stored['idempotency_key'], queued: true);
        }

        $envelope = $this->envelope($event, $this->client->trail, '');
        $request = $this->envelopeModel($envelope);
        $key = $envelope['idempotency_key'];

        return $this->client->retry->run(function () use ($request, $key): EmitResult {
            [$data, $status] = $this->send(fn () => $this->client->api->eventsCreateWithHttpInfo($request));

            if ($status !== 201 && $status !== 200) {
                throw new ApiException('HTTP ' . $status, $status);
            }

            /** @var AuditEvent $data */
            return new EmitResult(event: $data, duplicate: $status === 200, idempotencyKey: $key);
        });
    }

    /**
     * Emits a batch of one to 500 audit events with one request.
     *
     * @param list<array<string, mixed>> $events
     *
     * @throws ValidationException|AuthException|NotFoundException|RateLimitedException|ServerException|TransportException|ApiException
     */
    public function emitBatch(array $events): BatchResult
    {
        return $this->emitBatchWithStatus($events)[1];
    }

    /**
     * {@see self::emitBatch()} that also returns the HTTP status of the response (202 or 207).
     *
     * @internal
     *
     * @param list<array<string, mixed>> $events
     *
     * @return array{0: int, 1: BatchResult}
     *
     * @throws ValidationException|AuthException|NotFoundException|RateLimitedException|ServerException|TransportException|ApiException
     */
    public function emitBatchWithStatus(array $events): array
    {
        if ($events === []) {
            throw new ValidationException('events must not be empty', null, null, 'events');
        }

        $models = [];

        foreach (array_values($events) as $index => $event) {
            if (!is_array($event)) {
                throw new ValidationException('each event must be an array', null, null, 'events[' . $index . ']');
            }

            $models[] = $this->envelopeModel($this->envelope($event, $this->client->trail, 'events[' . $index . '].'));
        }

        $request = (new EventBatchRequest())->setEvents($models);

        return $this->client->retry->run(function () use ($request): array {
            [$data, $status] = $this->send(fn () => $this->client->api->eventsCreateWithHttpInfo($request));

            if ($status !== 202 && $status !== 207) {
                throw new ApiException('HTTP ' . $status, $status);
            }

            /** @var EventBatchResult $data */
            $results = [];

            foreach ($data->getResults() ?? [] as $item) {
                $error = $item->getError();
                $results[] = new BatchItem(
                    index: (int) $item->getIndex(),
                    status: (string) $item->getStatus(),
                    id: $item->getId(),
                    error: $error === null ? null : new BatchItemError(
                        code: (string) $error->getCode(),
                        message: (string) $error->getMessage(),
                        field: $error->getField(),
                    ),
                );
            }

            return [$status, new BatchResult(
                accepted: (int) $data->getAccepted(),
                rejected: (int) $data->getRejected(),
                results: $results,
            )];
        });
    }

    /**
     * Fetches one page of audit events. The client's default trail is not applied.
     *
     * @param array<string, mixed> $params the {@see self::LIST_PARAMS} filters; omitted ones are not sent
     *
     * @throws ValidationException|AuthException|NotFoundException|RateLimitedException|ServerException|TransportException|ApiException
     */
    public function list(array $params = []): EventPage
    {
        $query = $this->listQuery($params, true);

        return $this->client->retry->run(function () use ($query): EventPage {
            [$data, $status] = $this->send(fn () => $this->client->api->eventsListWithHttpInfo(...$query));

            if ($status !== 200) {
                throw new ApiException('HTTP ' . $status, $status);
            }

            return new EventPage(
                results: $data->getResults() ?? [],
                nextCursor: self::nextCursor($data->getNext()),
            );
        });
    }

    /**
     * Lazily walks every page, following the next cursor. A failure on a later
     * page is raised after the earlier events were yielded.
     *
     * @param array<string, mixed> $params {@see self::LIST_PARAMS} without `cursor`
     *
     * @return \Generator<int, AuditEvent>
     *
     * @throws ValidationException|AuthException|NotFoundException|RateLimitedException|ServerException|TransportException|ApiException
     */
    public function iterate(array $params = []): \Generator
    {
        $query = $this->listQuery($params, false);
        $cursor = null;

        do {
            if ($cursor !== null) {
                $query['cursor'] = $cursor;
            }

            $page = $this->list($query);

            foreach ($page->results as $event) {
                yield $event;
            }

            $cursor = $page->nextCursor;
        } while ($cursor !== null);
    }

    /**
     * Runs one attempt, mapping everything the generated core and Guzzle throw.
     *
     * @param callable():array{0: mixed, 1: int, 2: array<string, array<int, string>>} $attempt
     *
     * @return array{0: mixed, 1: int}
     */
    private function send(callable $attempt): array
    {
        try {
            [$data, $status] = $attempt();
        } catch (\Throwable $e) {
            throw $this->convert($e);
        }

        return [$data, $status];
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
            in_array($status, [400, 413, 422], true) => new ValidationException($message, $status, $code, $field, $retryAfter, $e),
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
        if (!is_array($headers)) {
            return null;
        }

        foreach ($headers as $name => $values) {
            if (strcasecmp((string) $name, 'retry-after') !== 0) {
                continue;
            }

            $value = is_array($values) ? (string) reset($values) : (string) $values;

            return preg_match('/^\d+$/', $value) === 1 ? (int) $value : null;
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

    /**
     * Validates an `emit` input for the outbox and returns it as the stored
     * event: the input with the resolved `trail` and `idempotency_key`.
     *
     * @internal
     *
     * @param array<string, mixed> $event
     *
     * @return array<string, mixed>
     *
     * @throws ValidationException
     */
    public function prepare(array $event): array
    {
        $envelope = $this->envelope($event, $this->client->trail, '');
        $stored = [
            'event' => $envelope['event'],
            'trail' => $envelope['trail'],
            'idempotency_key' => $envelope['idempotency_key'],
        ];

        foreach (['created_at', 'organization_id'] as $field) {
            if (isset($envelope[$field])) {
                $stored[$field] = $envelope[$field];
            }
        }

        if (isset($envelope['actor'])) {
            $stored['actor'] = self::members($event['actor'], ['type', 'id', 'name', 'metadata']);
        }

        if (isset($envelope['targets'])) {
            $stored['targets'] = array_map(
                static fn (array $target): array => self::members($target, ['type', 'id', 'name', 'metadata']),
                array_values($event['targets'])
            );
        }

        foreach (['metadata', 'context', 'data'] as $field) {
            if (isset($envelope[$field])) {
                $stored[$field] = $event[$field];
            }
        }

        return $stored;
    }

    /**
     * @param array<string, mixed> $input
     * @param list<string>         $names
     *
     * @return array<string, mixed>
     */
    private static function members(array $input, array $names): array
    {
        $out = [];

        foreach ($names as $name) {
            if (($input[$name] ?? null) !== null) {
                $out[$name] = $input[$name];
            }
        }

        return $out;
    }

    /**
     * Validates an emit input and returns the wire envelope: the resolved
     * trail, a generated idempotency key, and only the supplied members.
     *
     * @param array<string, mixed> $event
     *
     * @return array<string, mixed>
     */
    private function envelope(array $event, ?string $trail, string $prefix): array
    {
        $name = $event['event'] ?? null;

        if (!is_string($name) || $name === '') {
            throw new ValidationException('event must be a non-empty string', null, null, $prefix . 'event');
        }

        $resolvedTrail = $event['trail'] ?? $trail;

        if (!is_string($resolvedTrail) || $resolvedTrail === '') {
            throw new ValidationException('trail must be a non-empty string', null, null, $prefix . 'trail');
        }

        $key = $event['idempotency_key'] ?? null;
        $envelope = [
            'event' => $name,
            'trail' => $resolvedTrail,
            'idempotency_key' => is_string($key) && $key !== '' ? $key : self::uuid4(),
        ];

        foreach (['created_at', 'organization_id'] as $field) {
            $value = $event[$field] ?? null;

            if ($value === null) {
                continue;
            }

            if (!is_string($value)) {
                throw new ValidationException($field . ' must be a string', null, null, $prefix . $field);
            }

            $envelope[$field] = $value;
        }

        try {
            if (($actor = $event['actor'] ?? null) !== null) {
                if (!is_array($actor)) {
                    throw new ValidationException('actor must be an array', null, null, $prefix . 'actor');
                }

                $envelope['actor'] = $this->actor($actor);
            }

            if (($targets = $event['targets'] ?? null) !== null) {
                if (!is_array($targets)) {
                    throw new ValidationException('targets must be an array', null, null, $prefix . 'targets');
                }

                $list = [];

                foreach (array_values($targets) as $index => $target) {
                    if (!is_array($target)) {
                        throw new ValidationException(
                            'each target must be an array',
                            null,
                            null,
                            $prefix . 'targets[' . $index . ']'
                        );
                    }

                    $list[] = $this->target($target);
                }

                $envelope['targets'] = $list;
            }

            foreach (['metadata' => 'metadata', 'context' => 'context'] as $field) {
                $value = $event[$field] ?? null;

                if ($value === null) {
                    continue;
                }

                if (!is_array($value)) {
                    throw new ValidationException($field . ' must be an array', null, null, $prefix . $field);
                }

                $envelope[$field] = self::mapOrObject($value);
            }

            if (($data = $event['data'] ?? null) !== null) {
                $envelope['data'] = $data;
            }
        } catch (\InvalidArgumentException $e) {
            throw new ValidationException(
                $e->getMessage(),
                null,
                null,
                $prefix . self::fieldOf($e->getMessage()),
                null,
                $e
            );
        }

        return $envelope;
    }

    /**
     * @param array<string, mixed> $actor
     */
    private function actor(array $actor): EventActorRequest
    {
        $model = (new EventActorRequest())->setType(self::stringMember($actor, 'type'));

        if (($id = $actor['id'] ?? null) !== null) {
            $model->setId(self::stringMember($actor, 'id'));
        }

        if (($name = $actor['name'] ?? null) !== null) {
            $model->setName(self::stringMember($actor, 'name'));
        }

        if (($metadata = $actor['metadata'] ?? null) !== null) {
            $model->setMetadata(self::mapOrObject(self::assocMember($actor, 'metadata')));
        }

        return $model;
    }

    /**
     * @param array<string, mixed> $target
     */
    private function target(array $target): EventTargetRequest
    {
        $model = (new EventTargetRequest())
            ->setType(self::stringMember($target, 'type'))
            ->setId(self::stringMember($target, 'id'));

        if (($name = $target['name'] ?? null) !== null) {
            $model->setName(self::stringMember($target, 'name'));
        }

        if (($metadata = $target['metadata'] ?? null) !== null) {
            $model->setMetadata(self::mapOrObject(self::assocMember($target, 'metadata')));
        }

        return $model;
    }

    /**
     * @param array<string, mixed> $envelope
     */
    private function envelopeModel(array $envelope): EventEnvelopeRequest
    {
        $model = (new EventEnvelopeRequest())
            ->setEvent($envelope['event'])
            ->setTrail($envelope['trail'])
            ->setIdempotencyKey($envelope['idempotency_key']);

        foreach (['created_at' => 'setCreatedAt', 'organization_id' => 'setOrganizationId'] as $field => $setter) {
            if (isset($envelope[$field])) {
                $model->{$setter}($envelope[$field]);
            }
        }

        foreach (['actor' => 'setActor', 'targets' => 'setTargets', 'metadata' => 'setMetadata'] as $field => $setter) {
            if (isset($envelope[$field])) {
                $model->{$setter}($envelope[$field]);
            }
        }

        if (isset($envelope['data'])) {
            $model->setData($envelope['data']);
        }

        if (isset($envelope['context'])) {
            $model->setContext($envelope['context']);
        }

        return $model;
    }

    /**
     * @param array<string, mixed> $params
     *
     * @return array<string, mixed> named arguments for the generated list call
     */
    private function listQuery(array $params, bool $allowCursor): array
    {
        $query = [];

        foreach ($params as $key => $value) {
            $key = (string) $key;

            if (!in_array($key, self::LIST_PARAMS, true)) {
                throw new ValidationException("unknown list parameter '" . $key . "'", null, null, $key);
            }

            if ($key === 'cursor' && !$allowCursor) {
                throw new ValidationException('iterate manages the cursor itself', null, null, 'cursor');
            }

            if ($value === null) {
                continue;
            }

            if (!is_scalar($value)) {
                throw new ValidationException('list parameters must be scalars', null, null, $key);
            }

            $query[$key] = $key === 'limit' ? (int) $value : (string) $value;
        }

        return $query;
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

    /**
     * @param array<string, mixed> $input
     */
    private static function stringMember(array $input, string $key): string
    {
        $value = $input[$key] ?? null;

        if (!is_string($value) || $value === '') {
            throw new ValidationException($key . ' must be a non-empty string', null, null, $key);
        }

        return $value;
    }

    /**
     * @param array<string, mixed> $input
     *
     * @return array<string, mixed>
     */
    private static function assocMember(array $input, string $key): array
    {
        $value = $input[$key] ?? null;

        if (!is_array($value)) {
            throw new ValidationException($key . ' must be an array', null, null, $key);
        }

        return $value;
    }

    private static function uuid4(): string
    {
        $bytes = random_bytes(16);
        $bytes[6] = chr((ord($bytes[6]) & 0x0f) | 0x40);
        $bytes[8] = chr((ord($bytes[8]) & 0x3f) | 0x80);

        return vsprintf('%s%s-%s-%s-%s-%s%s%s', str_split(bin2hex($bytes), 4));
    }
}
