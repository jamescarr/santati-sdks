<?php

declare(strict_types=1);

namespace Santati\Tests;

use PHPUnit\Framework\Attributes\DataProvider;
use PHPUnit\Framework\TestCase;
use Santati\Client;
use Santati\Core\ObjectSerializer;
use Santati\Exception\ApiException;
use Santati\Exception\AuthException;
use Santati\Exception\NotFoundException;
use Santati\Exception\OutboxException;
use Santati\Exception\RateLimitedException;
use Santati\Exception\SantatiException;
use Santati\Exception\ServerException;
use Santati\Exception\TransportException;
use Santati\Exception\ValidationException;
use Santati\Outbox\MemoryOutbox;
use Santati\SendOutcome;

/**
 * Runs every vector in `conformance/cases/*.json` against the PHP facade,
 * with one `php -S` mock gateway per case.
 */
final class ConformanceTest extends TestCase
{
    /**
     * The fixed kind table: errors map to a kind by exact class.
     */
    private const KINDS = [
        ValidationException::class => 'ValidationError',
        AuthException::class => 'AuthError',
        NotFoundException::class => 'NotFoundError',
        RateLimitedException::class => 'RateLimitedError',
        ServerException::class => 'ServerError',
        TransportException::class => 'TransportError',
        ApiException::class => 'ApiError',
        OutboxException::class => 'OutboxError',
    ];

    /**
     * @var array{0: resource, 1: int}|null
     */
    private ?array $server = null;

    public static function cases(): iterable
    {
        $cases = [];

        foreach (glob(dirname(__DIR__, 2) . '/conformance/cases/*.json') ?: [] as $file) {
            $document = json_decode((string) file_get_contents($file), true, 512, JSON_THROW_ON_ERROR);

            foreach ($document['cases'] as $case) {
                $cases[$case['id']] = [$case];
            }
        }

        if ($cases === []) {
            throw new \RuntimeException('no conformance cases loaded from conformance/cases');
        }

        return $cases;
    }

    protected function tearDown(): void
    {
        $this->stopGateway();
    }

    #[DataProvider('cases')]
    public function test_case(array $case): void
    {
        $input = $case['input'];
        $gateway = $input['gateway'];
        $id = $case['id'];
        $dir = sys_get_temp_dir() . '/santati-conformance-' . bin2hex(random_bytes(6));
        mkdir($dir, 0700, true);

        try {
            if (($gateway['unreachable'] ?? false) === true) {
                $origin = 'http://127.0.0.1:1';
            } else {
                $origin = 'http://127.0.0.1:' . $this->startGateway($dir, $gateway);
            }

            $bindings = [];
            $thrown = null;
            $produced = null;
            $outcomes = [];

            try {
                $produced = $this->produce($this->client($input['client'], $origin, $input['hooks'] ?? null, $outcomes, $case['operation'] === 'emit_outbox'), $case, $outcomes);
            } catch (\Throwable $e) {
                $thrown = $e;
            }

            if (array_key_exists('ok', $case['expect'])) {
                self::assertNull(
                    $thrown,
                    $thrown === null ? '' : 'unexpected ' . $thrown::class . ': ' . $thrown->getMessage()
                );
                $this->assertMatches(
                    self::stripNulls($case['expect']['ok']),
                    self::stripNulls($produced),
                    $bindings,
                    $id
                );
            } else {
                $expected = $case['expect']['error'];
                self::assertNotNull($thrown, 'expected ' . $expected['kind'] . ' but the call succeeded');
                self::assertSame(
                    $expected['kind'],
                    self::KINDS[$thrown::class] ?? null,
                    'wrong error kind for ' . $thrown::class . ': ' . $thrown->getMessage()
                );
                self::assertInstanceOf(SantatiException::class, $thrown);
                $this->assertErrorAttributes($expected, $thrown, $id);
            }

            if (array_key_exists('requests', $case['expect'])) {
                $this->assertRequests($case['expect']['requests'], $this->recorded($dir), $bindings, $id);
            }
        } finally {
            $this->stopGateway();
            self::remove($dir);
        }
    }

    /**
     * @param array<string, mixed>      $options
     * @param array<string, mixed>|null $hooks    the `emit_outbox` vectors' hook behaviours
     * @param list<array<string, mixed>> $outcomes collects the `post_send` calls
     */
    private function client(array $options, string $origin, ?array $hooks, array &$outcomes, bool $outbox): Client
    {
        $pre = $hooks['pre_send'] ?? null;
        $preSend = $pre === null ? null : static function (array $event) use ($pre): ?array {
            if (($pre['raise'] ?? false) === true) {
                throw new \RuntimeException('conformance pre_send');
            }

            if (in_array($event['event'], $pre['drop_events'] ?? [], true)) {
                return null;
            }

            if (isset($pre['set_metadata'])) {
                $event['metadata'] = array_merge($event['metadata'] ?? [], $pre['set_metadata']);
            }

            return $event;
        };
        $raise = ($hooks['post_send']['raise'] ?? false) === true;
        $postSend = static function (array $event, SendOutcome $outcome) use (&$outcomes, $raise): void {
            $error = $outcome->error;
            $outcomes[] = [
                'event' => json_decode(json_encode($event, JSON_THROW_ON_ERROR), true),
                'status' => $outcome->status,
                'id' => $outcome->id,
                'error' => $error === null ? null : [
                    'kind' => self::KINDS[$error::class] ?? $error::class,
                    'status' => $error->getStatus(),
                    'code' => $error->getErrorCode(),
                    'field' => $error->getField(),
                    'retry_after' => $error->getRetryAfter(),
                ],
            ];

            if ($raise) {
                throw new \RuntimeException('conformance post_send');
            }
        };

        return new Client(
            apiKey: $options['api_key'],
            baseUrl: $origin . ($options['base_path'] ?? ''),
            trail: $options['trail'] ?? null,
            timeoutMs: $options['timeout_ms'] ?? 10000,
            maxRetries: $options['max_retries'] ?? 2,
            initialBackoffMs: $options['initial_backoff_ms'] ?? 250,
            maxBackoffMs: $options['max_backoff_ms'] ?? 8000,
            headers: $options['headers'] ?? [],
            outbox: $outbox ? new MemoryOutbox($options['max_pending'] ?? 10000) : null,
            batchSize: $options['batch_size'] ?? 100,
            preSend: $preSend,
            postSend: $postSend,
        );
    }

    /**
     * Runs the case's operation and returns the expected `ok` payload.
     *
     * @param list<array<string, mixed>> $outcomes the `post_send` calls recorded so far
     */
    private function produce(Client $client, array $case, array &$outcomes): mixed
    {
        $input = $case['input'];

        return match ($case['operation']) {
            'emit' => self::wire($this->emitPayload($client, $input['event'])),
            'emit_batch' => self::wire($this->batchPayload($client, $input['events'])),
            'list' => self::wire($this->pagePayload($client, $input['params'] ?? [])),
            'emit_outbox' => $this->emitOutboxPayload($client, $input['events'], $outcomes),
            'iterate' => self::wire(iterator_to_array($client->events->iterate($input['params'] ?? []), false)),
            default => throw new \RuntimeException('unknown operation ' . $case['operation']),
        };
    }

    /**
     * Emits every event through the outbox, closes the client (also when
     * emitting failed), then reports the results and outcomes or the
     * remembered error.
     *
     * @param list<array<string, mixed>> $events
     * @param list<array<string, mixed>> $outcomes
     *
     * @return array<string, mixed>
     */
    private function emitOutboxPayload(Client $client, array $events, array &$outcomes): array
    {
        $results = [];
        $remembered = null;

        try {
            foreach ($events as $event) {
                $results[] = self::wire($this->emitPayload($client, $event));
            }
        } catch (SantatiException $e) {
            $remembered = $e;
        } finally {
            $client->close();
        }

        if ($remembered !== null) {
            throw $remembered;
        }

        return ['results' => $results, 'outcomes' => $outcomes];
    }

    /**
     * @return array<string, mixed>
     */
    private function emitPayload(Client $client, array $event): array
    {
        $result = $client->events->emit($event);

        return [
            'event' => $result->event,
            'duplicate' => $result->duplicate,
            'idempotency_key' => $result->idempotencyKey,
            'queued' => $result->queued,
        ];
    }

    /**
     * @param list<array<string, mixed>> $events
     *
     * @return array<string, mixed>
     */
    private function batchPayload(Client $client, array $events): array
    {
        $result = $client->events->emitBatch($events);

        return [
            'accepted' => $result->accepted,
            'rejected' => $result->rejected,
            'results' => array_map(static fn ($item): array => [
                'index' => $item->index,
                'status' => $item->status,
                'id' => $item->id,
                'error' => $item->error === null ? null : [
                    'code' => $item->error->code,
                    'message' => $item->error->message,
                    'field' => $item->error->field,
                ],
            ], $result->results),
        ];
    }

    /**
     * @param array<string, mixed> $params
     *
     * @return array<string, mixed>
     */
    private function pagePayload(Client $client, array $params): array
    {
        $page = $client->events->list($params);

        return ['results' => $page->results, 'next_cursor' => $page->nextCursor];
    }

    private function assertErrorAttributes(array $expected, SantatiException $error, string $context): void
    {
        $attributes = [
            'status' => $error->getStatus(),
            'code' => $error->getErrorCode(),
            'field' => $error->getField(),
            'retry_after' => $error->getRetryAfter(),
        ];

        foreach ($attributes as $key => $actual) {
            if (array_key_exists($key, $expected)) {
                self::assertSame($expected[$key], $actual, $context . ': ' . $key);
            }
        }
    }

    /**
     * @param list<array<string, mixed>> $expected
     * @param list<array<string, mixed>> $actual
     * @param array<string, string>      $bindings
     */
    private function assertRequests(array $expected, array $actual, array &$bindings, string $context): void
    {
        self::assertCount(count($expected), $actual, $context . ': recorded requests');

        foreach ($expected as $index => $want) {
            $got = $actual[$index];
            self::assertSame($want['method'], $got['method'] ?? null, $context . " request $index: method");
            self::assertSame($want['path'], $got['path'] ?? null, $context . " request $index: path");

            foreach ($want['headers'] ?? [] as $name => $value) {
                $header = strtolower((string) $name);
                self::assertArrayHasKey($header, $got['headers'], $context . " request $index: header $header");
                self::assertSame($value, $got['headers'][$header], $context . " request $index: header $header");
            }

            if (array_key_exists('body', $want)) {
                $this->assertMatches($want['body'], $got['body'], $bindings, $context . " request $index body");
            }
        }
    }

    /**
     * Deep equality with the `{"$generated": "<label>"}` matcher.
     *
     * @param array<string, string> $bindings
     */
    private function assertMatches(mixed $expected, mixed $actual, array &$bindings, string $context): void
    {
        if (is_array($expected) && count($expected) === 1 && array_key_exists('$generated', $expected)) {
            self::assertIsString($actual, $context . ': expected a generated string');
            self::assertNotSame('', $actual, $context . ': expected a non-empty generated string');
            $label = (string) $expected['$generated'];

            if (array_key_exists($label, $bindings)) {
                self::assertSame($bindings[$label], $actual, $context . ": label $label is bound twice");
            } else {
                foreach ($bindings as $other => $value) {
                    self::assertNotSame($value, $actual, $context . ": labels $other and $label must differ");
                }

                $bindings[$label] = $actual;
            }

            return;
        }

        if (is_array($expected)) {
            self::assertIsArray($actual, $context . ': expected a list or object');

            if (array_is_list($expected) && array_is_list($actual)) {
                self::assertCount(count($expected), $actual, $context . ': list length');

                foreach ($expected as $index => $value) {
                    $this->assertMatches($value, $actual[$index], $bindings, $context . "[$index]");
                }

                return;
            }

            $want = array_keys($expected);
            $got = array_keys($actual);
            sort($want);
            sort($got);
            self::assertSame($want, $got, $context . ': members');

            foreach ($expected as $key => $value) {
                $this->assertMatches($value, $actual[$key], $bindings, $context . '.' . $key);
            }

            return;
        }

        self::assertSame($expected, $actual, $context);
    }

    /**
     * Serializes facade values with the generated models' own serializer and
     * normalizes them into plain PHP arrays.
     */
    private static function wire(mixed $value): mixed
    {
        if (is_array($value)) {
            return array_map(static fn ($item) => self::wire($item), $value);
        }

        $json = json_encode(ObjectSerializer::sanitizeForSerialization($value), JSON_THROW_ON_ERROR);

        return json_decode($json, true, 512, JSON_THROW_ON_ERROR);
    }

    /**
     * Removes every object member whose value is null, recursively.
     */
    private static function stripNulls(mixed $value): mixed
    {
        if (!is_array($value)) {
            return $value;
        }

        $list = array_is_list($value);
        $out = [];

        foreach ($value as $key => $item) {
            if (!$list && $item === null) {
                continue;
            }

            $out[$key] = self::stripNulls($item);
        }

        return $out;
    }

    /**
     * @return list<array<string, mixed>>
     */
    private function recorded(string $dir): array
    {
        $file = $dir . '/requests.jsonl';

        if (!is_file($file)) {
            return [];
        }

        $requests = [];

        foreach (file($file, FILE_IGNORE_NEW_LINES | FILE_SKIP_EMPTY_LINES) ?: [] as $line) {
            $request = json_decode($line, true);

            if (is_array($request)) {
                $requests[] = $request;
            }
        }

        return $requests;
    }

    /**
     * Starts the mock gateway for one case and returns its port.
     */
    private function startGateway(string $dir, array $gateway): int
    {
        file_put_contents($dir . '/gateway.json', json_encode($gateway, JSON_THROW_ON_ERROR));
        file_put_contents($dir . '/requests.jsonl', '');
        putenv('SANTATI_CONFORMANCE_DIR=' . $dir);

        $port = self::freePort();
        $process = proc_open(
            [PHP_BINARY, '-S', '127.0.0.1:' . $port, 'tests/Conformance/router.php'],
            [
                0 => ['pipe', 'r'],
                1 => ['file', $dir . '/gateway.log', 'a'],
                2 => ['file', $dir . '/gateway.log', 'a'],
            ],
            $pipes,
            dirname(__DIR__)
        );

        if (!is_resource($process)) {
            throw new \RuntimeException('could not start the mock gateway');
        }

        foreach ($pipes as $pipe) {
            fclose($pipe);
        }

        $this->server = [$process, $port];
        $deadline = microtime(true) + 5.0;

        while (microtime(true) < $deadline) {
            $socket = @fsockopen('127.0.0.1', $port, $errno, $error, 0.2);

            if (is_resource($socket)) {
                fclose($socket);

                return $port;
            }

            usleep(20000);
        }

        $this->stopGateway();

        throw new \RuntimeException('mock gateway did not start: ' . (string) @file_get_contents($dir . '/gateway.log'));
    }

    private function stopGateway(): void
    {
        if ($this->server === null) {
            return;
        }

        [$process] = $this->server;
        $this->server = null;

        if (is_resource($process)) {
            proc_terminate($process);
            proc_close($process);
        }
    }

    private static function freePort(): int
    {
        $socket = stream_socket_server('tcp://127.0.0.1:0', $errno, $error);

        if ($socket === false) {
            throw new \RuntimeException('could not allocate a port: ' . $error);
        }

        $name = (string) stream_socket_get_name($socket, false);
        fclose($socket);

        return (int) substr($name, strrpos($name, ':') + 1);
    }

    private static function remove(string $dir): void
    {
        foreach (glob($dir . '/*') ?: [] as $file) {
            is_dir($file) ? self::remove($file) : @unlink($file);
        }

        @rmdir($dir);
    }
}
