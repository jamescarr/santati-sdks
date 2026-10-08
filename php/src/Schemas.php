<?php

declare(strict_types=1);

namespace Santati;

use Santati\Core\Model\EventDefinition;
use Santati\Core\Model\EventDefinitionWriteRequest;
use Santati\Core\Model\EventSchemaDocumentRequest;
use Santati\Core\Model\EventSchemaVersion;
use Santati\Core\Model\PaginatedEventDefinitionList;
use Santati\Core\Model\PaginatedEventSchemaVersionList;
use Santati\Core\Model\PatchedEventDefinitionWriteRequest;
use Santati\Core\Model\SchemaCheck;
use Santati\Core\Model\StandardEventCatalog;
use Santati\Core\Model\StandardPackInstallRequest;
use Santati\Core\Model\StandardPackInstallResult;
use Santati\Exception\ApiException;
use Santati\Exception\AuthException;
use Santati\Exception\NotFoundException;
use Santati\Exception\RateLimitedException;
use Santati\Exception\ServerException;
use Santati\Exception\TransportException;
use Santati\Exception\ValidationException;

/**
 * `$client->schemas`: event definitions, their schema versions and the
 * standard packs.
 *
 * Every operation but {@see self::createVersion()} retries like `Events`;
 * `createVersion()` is sent once, because a repeat would create a second draft.
 * An empty `$action` throws a `ValidationException` (field `action`) before any
 * request; everything else is forwarded as given and the server decides. Any
 * 2xx status but the one an operation expects, and a 2xx body that does not
 * decode, throw an `ApiException`.
 *
 * A `$schema` is a JSON Schema document as an array. PHP cannot tell an empty
 * JSON object from an empty array: an empty object nested anywhere inside a
 * `$schema` must be passed as `\stdClass` (an empty top-level array is sent as
 * `{}`).
 */
final class Schemas
{
    use CallsCore;

    /**
     * The page parameters, in spec (alphabetical) order.
     */
    public const PAGE_PARAMS = ['cursor', 'limit'];

    private const DEFINITION_MEMBERS = ['action', 'description', 'allowed_target_types', 'is_active'];

    private const UPDATE_MEMBERS = ['new_action', 'description', 'allowed_target_types', 'is_active'];

    public function __construct(private readonly Client $client)
    {
    }

    /**
     * Fetches one page of the team's event definitions.
     *
     * @param array<string, mixed> $params `limit` and `cursor`; omitted ones are not sent
     *
     * @throws ValidationException|AuthException|NotFoundException|RateLimitedException|ServerException|TransportException|ApiException
     */
    public function listDefinitions(array $params = []): DefinitionPage
    {
        $query = $this->pageQuery($params, true);

        [$data, $status] = $this->perform(200, fn () => $this->client->definitionsApi->eventDefinitionsListWithHttpInfo(...$query));

        $page = self::decoded($data, PaginatedEventDefinitionList::class, $status);

        return new DefinitionPage(results: $page->getResults() ?? [], nextCursor: self::nextCursor($page->getNext()));
    }

    /**
     * Lazily walks every event definition, following the next cursor. A failure
     * on a later page is raised after the earlier definitions were yielded.
     *
     * @param array<string, mixed> $params `limit`
     *
     * @return \Generator<int, EventDefinition>
     *
     * @throws ValidationException|AuthException|NotFoundException|RateLimitedException|ServerException|TransportException|ApiException
     */
    public function iterateDefinitions(array $params = []): \Generator
    {
        $query = $this->pageQuery($params, false);
        $cursor = null;

        do {
            if ($cursor !== null) {
                $query['cursor'] = $cursor;
            }

            $page = $this->listDefinitions($query);

            foreach ($page->results as $definition) {
                yield $definition;
            }

            $cursor = $page->nextCursor;
        } while ($cursor !== null);
    }

    /**
     * Fetches one event definition.
     *
     * @throws ValidationException|AuthException|NotFoundException|RateLimitedException|ServerException|TransportException|ApiException
     */
    public function getDefinition(string $action): EventDefinition
    {
        self::requireAction($action);

        [$data, $status] = $this->perform(200, fn () => $this->client->definitionsApi->eventDefinitionsRetrieveWithHttpInfo($action));

        return self::decoded($data, EventDefinition::class, $status);
    }

    /**
     * Defines an action; only the members you supply are sent.
     *
     * @param array<string, mixed> $definition `action`, plus optional `description`,
     *                                         `allowed_target_types` and `is_active`
     *
     * @throws ValidationException|AuthException|NotFoundException|RateLimitedException|ServerException|TransportException|ApiException
     */
    public function createDefinition(array $definition): EventDefinition
    {
        $members = self::members($definition, self::DEFINITION_MEMBERS, 'definition member');
        $action = $members['action'] ?? null;

        if (!is_string($action)) {
            throw new ValidationException('action must be a non-empty string', null, null, 'action');
        }

        self::requireAction($action);

        $request = self::build(static function () use ($members): EventDefinitionWriteRequest {
            $model = (new EventDefinitionWriteRequest())->setAction($members['action']);

            foreach (['description' => 'setDescription', 'allowed_target_types' => 'setAllowedTargetTypes', 'is_active' => 'setIsActive'] as $name => $setter) {
                if (array_key_exists($name, $members)) {
                    $model->{$setter}($members[$name]);
                }
            }

            return $model;
        });

        [$data, $status] = $this->perform(201, fn () => $this->client->definitionsApi->eventDefinitionsCreateWithHttpInfo($request));

        return self::decoded($data, EventDefinition::class, $status);
    }

    /**
     * Changes a definition; only the members you supply are sent, and
     * `new_action` renames the action (sent as `action`).
     *
     * @param array<string, mixed> $changes `new_action`, `description`, `allowed_target_types`, `is_active`
     *
     * @throws ValidationException|AuthException|NotFoundException|RateLimitedException|ServerException|TransportException|ApiException
     */
    public function updateDefinition(string $action, array $changes): EventDefinition
    {
        self::requireAction($action);
        $members = self::members($changes, self::UPDATE_MEMBERS, 'definition change');

        $request = self::build(static function () use ($members): PatchedEventDefinitionWriteRequest {
            $model = new PatchedEventDefinitionWriteRequest();

            foreach (['new_action' => 'setAction', 'description' => 'setDescription', 'allowed_target_types' => 'setAllowedTargetTypes', 'is_active' => 'setIsActive'] as $name => $setter) {
                if (array_key_exists($name, $members)) {
                    $model->{$setter}($members[$name]);
                }
            }

            return $model;
        });

        [$data, $status] = $this->perform(200, fn () => $this->client->definitionsApi->eventDefinitionsUpdateWithHttpInfo($action, $request));

        return self::decoded($data, EventDefinition::class, $status);
    }

    /**
     * Deletes an event definition.
     *
     * @throws ValidationException|AuthException|NotFoundException|RateLimitedException|ServerException|TransportException|ApiException
     */
    public function deleteDefinition(string $action): void
    {
        self::requireAction($action);

        $this->perform(204, fn () => $this->client->definitionsApi->eventDefinitionsDestroyWithHttpInfo($action));
    }

    /**
     * Fetches one page of an action's schema versions, newest first.
     *
     * @param array<string, mixed> $params `limit` and `cursor`; omitted ones are not sent
     *
     * @throws ValidationException|AuthException|NotFoundException|RateLimitedException|ServerException|TransportException|ApiException
     */
    public function listVersions(string $action, array $params = []): SchemaVersionPage
    {
        self::requireAction($action);
        $query = $this->pageQuery($params, true);

        [$data, $status] = $this->perform(200, fn () => $this->client->definitionsApi->schemaVersionsListWithHttpInfo($action, ...$query));

        $page = self::decoded($data, PaginatedEventSchemaVersionList::class, $status);

        return new SchemaVersionPage(results: $page->getResults() ?? [], nextCursor: self::nextCursor($page->getNext()));
    }

    /**
     * Lazily walks every schema version of an action, following the next
     * cursor. A failure on a later page is raised after the earlier versions
     * were yielded.
     *
     * @param array<string, mixed> $params `limit`
     *
     * @return \Generator<int, EventSchemaVersion>
     *
     * @throws ValidationException|AuthException|NotFoundException|RateLimitedException|ServerException|TransportException|ApiException
     */
    public function iterateVersions(string $action, array $params = []): \Generator
    {
        $query = $this->pageQuery($params, false);
        $cursor = null;

        do {
            if ($cursor !== null) {
                $query['cursor'] = $cursor;
            }

            $page = $this->listVersions($action, $query);

            foreach ($page->results as $version) {
                yield $version;
            }

            $cursor = $page->nextCursor;
        } while ($cursor !== null);
    }

    /**
     * Fetches one schema version with its `ETag`.
     *
     * @throws ValidationException|AuthException|NotFoundException|RateLimitedException|ServerException|TransportException|ApiException
     */
    public function getVersion(string $action, int $version): SchemaVersionResult
    {
        self::requireAction($action);

        return $this->versionResult(
            $this->perform(200, fn () => $this->client->definitionsApi->schemaVersionsRetrieveWithHttpInfo($action, $version))
        );
    }

    /**
     * Creates a draft from a JSON Schema document. Sent once: it is never retried.
     *
     * @param array<string, mixed>|\stdClass $schema nested empty objects must be `\stdClass`
     *
     * @throws ValidationException|AuthException|NotFoundException|RateLimitedException|ServerException|TransportException|ApiException
     */
    public function createVersion(string $action, array|\stdClass $schema): SchemaVersionResult
    {
        self::requireAction($action);
        $request = self::document($schema);

        return $this->versionResult(
            $this->perform(201, fn () => $this->client->definitionsApi->schemaVersionsCreateWithHttpInfo($action, $request), false)
        );
    }

    /**
     * Replaces a draft's document. With `$ifMatch` (an `ETag` you read) a
     * concurrent edit throws an `ApiException` of status 412 instead of being
     * overwritten.
     *
     * @param array<string, mixed>|\stdClass $schema nested empty objects must be `\stdClass`
     *
     * @throws ValidationException|AuthException|NotFoundException|RateLimitedException|ServerException|TransportException|ApiException
     */
    public function updateVersion(string $action, int $version, array|\stdClass $schema, ?string $ifMatch = null): SchemaVersionResult
    {
        self::requireAction($action);
        $request = self::document($schema);

        return $this->versionResult(
            $this->perform(200, fn () => $this->client->definitionsApi->schemaVersionsUpdateWithHttpInfo($action, $version, $request, $ifMatch))
        );
    }

    /**
     * Deletes a draft; a published version throws an `ApiException` of status 409.
     *
     * @throws ValidationException|AuthException|NotFoundException|RateLimitedException|ServerException|TransportException|ApiException
     */
    public function deleteVersion(string $action, int $version): void
    {
        self::requireAction($action);

        $this->perform(204, fn () => $this->client->definitionsApi->schemaVersionsDestroyWithHttpInfo($action, $version));
    }

    /**
     * Publishes a draft: it becomes immutable and validates ingest.
     *
     * @throws ValidationException|AuthException|NotFoundException|RateLimitedException|ServerException|TransportException|ApiException
     */
    public function publishVersion(string $action, int $version): SchemaVersionResult
    {
        self::requireAction($action);

        return $this->versionResult(
            $this->perform(200, fn () => $this->client->definitionsApi->schemaVersionsPublishWithHttpInfo($action, $version))
        );
    }

    /**
     * Starts the migration window of a superseded version.
     *
     * @throws ValidationException|AuthException|NotFoundException|RateLimitedException|ServerException|TransportException|ApiException
     */
    public function deprecateVersion(string $action, int $version): SchemaVersionResult
    {
        self::requireAction($action);

        return $this->versionResult(
            $this->perform(200, fn () => $this->client->definitionsApi->schemaVersionsDeprecateWithHttpInfo($action, $version))
        );
    }

    /**
     * Dry-runs a document against the action's newest stored events; nothing is stored.
     *
     * @param array<string, mixed>|\stdClass $schema nested empty objects must be `\stdClass`
     *
     * @throws ValidationException|AuthException|NotFoundException|RateLimitedException|ServerException|TransportException|ApiException
     */
    public function checkSchema(string $action, array|\stdClass $schema): SchemaCheck
    {
        self::requireAction($action);
        $request = self::document($schema);

        [$data, $status] = $this->perform(200, fn () => $this->client->definitionsApi->schemaVersionsCheckWithHttpInfo($action, $request));

        return self::decoded($data, SchemaCheck::class, $status);
    }

    /**
     * Fetches the standard catalog: every pack and its actions.
     *
     * @throws ValidationException|AuthException|NotFoundException|RateLimitedException|ServerException|TransportException|ApiException
     */
    public function listStandardPacks(): StandardEventCatalog
    {
        [$data, $status] = $this->perform(200, fn () => $this->client->definitionsApi->standardEventsListWithHttpInfo());

        return self::decoded($data, StandardEventCatalog::class, $status);
    }

    /**
     * Installs packs by slug; the slugs are forwarded unchanged and the server judges them.
     *
     * @param list<string> $packs
     *
     * @throws ValidationException|AuthException|NotFoundException|RateLimitedException|ServerException|TransportException|ApiException
     */
    public function installStandardPacks(array $packs): StandardPackInstallResult
    {
        $request = self::build(static fn (): StandardPackInstallRequest => (new StandardPackInstallRequest())->setPacks(array_values($packs)));

        [$data, $status] = $this->perform(200, fn () => $this->client->definitionsApi->standardEventsInstallWithHttpInfo($request));

        return self::decoded($data, StandardPackInstallResult::class, $status);
    }

    /**
     * One operation: runs the generated call through {@see CallsCore::send()}
     * (retried unless `$retry` is false) and requires the `$expected` 2xx status.
     *
     * @param callable():array{0: mixed, 1: int, 2: array<string, array<int, string>>} $call
     *
     * @return array{0: mixed, 1: int, 2: array<string, array<int, string>>}
     */
    private function perform(int $expected, callable $call, bool $retry = true): array
    {
        $attempt = function () use ($expected, $call): array {
            $answer = $this->send($call);

            if ($answer[1] !== $expected) {
                throw new ApiException('HTTP ' . $answer[1], $answer[1]);
            }

            return $answer;
        };

        return $retry ? $this->client->retry->run($attempt) : $attempt();
    }

    /**
     * @param array{0: mixed, 1: int, 2: array<string, array<int, string>>} $answer
     */
    private function versionResult(array $answer): SchemaVersionResult
    {
        return new SchemaVersionResult(
            schemaVersion: self::decoded($answer[0], EventSchemaVersion::class, $answer[1]),
            etag: self::header($answer[2], 'etag'),
        );
    }

    /**
     * @template T of object
     *
     * @param class-string<T> $class
     *
     * @return T
     */
    private static function decoded(mixed $data, string $class, int $status): object
    {
        if (!$data instanceof $class) {
            throw new ApiException('could not decode the response body', $status);
        }

        return $data;
    }

    private static function requireAction(string $action): void
    {
        if ($action === '') {
            throw new ValidationException('action must be a non-empty string', null, null, 'action');
        }
    }

    /**
     * The supplied (non-null) members of `$input`; an unknown key is rejected.
     *
     * @param array<string, mixed> $input
     * @param list<string>         $allowed
     *
     * @return array<string, mixed>
     */
    private static function members(array $input, array $allowed, string $what): array
    {
        $members = [];

        foreach ($input as $key => $value) {
            $key = (string) $key;

            if (!in_array($key, $allowed, true)) {
                throw new ValidationException('unknown ' . $what . " '" . $key . "'", null, null, $key);
            }

            if ($value !== null) {
                $members[$key] = $value;
            }
        }

        return $members;
    }

    /**
     * Builds a generated request model; a setter rejecting a value is a local validation failure.
     *
     * @template T of object
     *
     * @param callable():T $make
     *
     * @return T
     */
    private static function build(callable $make): object
    {
        try {
            return $make();
        } catch (\InvalidArgumentException $e) {
            throw new ValidationException($e->getMessage(), null, null, self::fieldOf($e->getMessage()), null, $e);
        }
    }

    /**
     * @param array<string, mixed>|\stdClass $schema
     */
    private static function document(array|\stdClass $schema): EventSchemaDocumentRequest
    {
        return self::build(
            static fn (): EventSchemaDocumentRequest => (new EventSchemaDocumentRequest())
                ->setSchema(is_array($schema) ? self::mapOrObject($schema) : $schema)
        );
    }

    /**
     * @param array<string, mixed> $params
     *
     * @return array<string, mixed> named arguments for the generated list call
     */
    private function pageQuery(array $params, bool $allowCursor): array
    {
        $query = [];

        foreach ($params as $key => $value) {
            $key = (string) $key;

            if (!in_array($key, self::PAGE_PARAMS, true)) {
                throw new ValidationException("unknown page parameter '" . $key . "'", null, null, $key);
            }

            if ($key === 'cursor' && !$allowCursor) {
                throw new ValidationException('iterate manages the cursor itself', null, null, 'cursor');
            }

            if ($value === null) {
                continue;
            }

            if (!is_scalar($value)) {
                throw new ValidationException('page parameters must be scalars', null, null, $key);
            }

            $query[$key] = $key === 'limit' ? (int) $value : (string) $value;
        }

        return $query;
    }
}
