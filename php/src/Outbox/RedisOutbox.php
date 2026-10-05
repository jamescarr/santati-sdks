<?php

declare(strict_types=1);

namespace Santati\Outbox;

use Predis\ClientInterface;
use Predis\PredisException;
use Santati\Exception\OutboxException;

/**
 * An outbox on one Redis Stream with the consumer group `santati`; the layout
 * is shared by every Santati SDK, so any of them can drain what another queued.
 *
 * Takes the application's own Predis client. Entries claimed but never acked
 * are re-delivered once they have been pending for `$visibilityMs`; `release()`
 * is therefore a no-op. The stream has no capacity bound.
 */
final class RedisOutbox implements OutboxStore
{
    private const GROUP = 'santati';

    private readonly string $consumer;

    private bool $ready = false;

    public function __construct(
        private readonly ClientInterface $client,
        private readonly string $key = 'santati:outbox',
        private readonly int $visibilityMs = 60000,
    ) {
        $bytes = random_bytes(16);
        $bytes[6] = chr((ord($bytes[6]) & 0x0f) | 0x40);
        $bytes[8] = chr((ord($bytes[8]) & 0x3f) | 0x80);
        $this->consumer = vsprintf('%s%s-%s-%s-%s-%s%s%s', str_split(bin2hex($bytes), 4));
    }

    public function enqueue(array $event): void
    {
        $this->guard(function () use ($event): void {
            $this->ensureGroup();
            $this->command(['XADD', $this->key, '*', 'event', json_encode(self::wire($event), JSON_THROW_ON_ERROR)]);
        });
    }

    public function claim(int $limit): array
    {
        if ($limit < 1) {
            return [];
        }

        return $this->guard(function () use ($limit): array {
            $this->ensureGroup();
            $entries = [];
            $poison = [];

            $reply = $this->command([
                'XAUTOCLAIM', $this->key, self::GROUP, $this->consumer,
                (string) $this->visibilityMs, '0-0', 'COUNT', (string) $limit,
            ]);
            $this->collect(is_array($reply) && is_array($reply[1] ?? null) ? $reply[1] : [], $entries, $poison);

            if (count($entries) + count($poison) < $limit) {
                $reply = $this->command([
                    'XREADGROUP', 'GROUP', self::GROUP, $this->consumer,
                    'COUNT', (string) ($limit - count($entries) - count($poison)), 'STREAMS', $this->key, '>',
                ]);

                foreach (is_array($reply) ? $reply : [] as $stream) {
                    if (is_array($stream) && is_array($stream[1] ?? null)) {
                        $this->collect($stream[1], $entries, $poison);
                    }
                }
            }

            if ($poison !== []) {
                $this->remove($poison);
            }

            return $entries;
        });
    }

    public function ack(array $ids): void
    {
        if ($ids === []) {
            return;
        }

        $this->guard(function () use ($ids): void {
            $this->remove(array_values($ids));
        });
    }

    public function release(array $ids): void
    {
    }

    /**
     * Empty maps must stay JSON objects on the shared wire layout, and PHP
     * cannot tell `[]` from `{}`.
     *
     * @param array<string, mixed> $event
     *
     * @return array<string, mixed>
     */
    private static function wire(array $event): array
    {
        $object = static fn (mixed $value): mixed => $value === [] ? new \stdClass() : $value;

        foreach (['metadata', 'context'] as $field) {
            if (array_key_exists($field, $event)) {
                $event[$field] = $object($event[$field]);
            }
        }

        if (is_array($event['actor'] ?? null) && array_key_exists('metadata', $event['actor'])) {
            $event['actor']['metadata'] = $object($event['actor']['metadata']);
        }

        if (is_array($event['targets'] ?? null)) {
            foreach ($event['targets'] as $i => $target) {
                if (is_array($target) && array_key_exists('metadata', $target)) {
                    $event['targets'][$i]['metadata'] = $object($target['metadata']);
                }
            }
        }

        return $event;
    }

    /**
     * @param array<mixed>       $rows
     * @param list<OutboxEntry>  $entries
     * @param list<string>       $poison
     */
    private function collect(array $rows, array &$entries, array &$poison): void
    {
        foreach ($rows as $row) {
            if (!is_array($row) || !isset($row[0]) || !is_string($row[0])) {
                continue; // nil / deleted placeholder
            }

            $id = $row[0];
            $json = null;
            $fields = is_array($row[1] ?? null) ? array_values($row[1]) : [];

            for ($i = 0; $i + 1 < count($fields); $i += 2) {
                if ($fields[$i] === 'event') {
                    $json = $fields[$i + 1];
                }
            }

            $event = is_string($json) ? json_decode($json, true) : null;

            if (!is_array($event)) {
                $poison[] = $id;

                continue;
            }

            $entries[] = new OutboxEntry($id, $event);
        }
    }

    /**
     * @param list<string> $ids
     */
    private function remove(array $ids): void
    {
        $this->command(['XACK', $this->key, self::GROUP, ...$ids]);
        $this->command(['XDEL', $this->key, ...$ids]);
    }

    private function ensureGroup(): void
    {
        if ($this->ready) {
            return;
        }

        try {
            $this->command(['XGROUP', 'CREATE', $this->key, self::GROUP, '0', 'MKSTREAM']);
        } catch (OutboxException $e) {
            if (!str_contains($e->getMessage(), 'BUSYGROUP')) {
                throw $e;
            }
        }

        $this->ready = true;
    }

    /**
     * @param list<string> $arguments
     */
    private function command(array $arguments): mixed
    {
        try {
            $reply = $this->client->executeRaw($arguments, $error);
        } catch (PredisException $e) {
            throw OutboxException::with('store_unavailable', $e->getMessage(), $e);
        }

        if ($error === true) {
            throw OutboxException::with('store_unavailable', (string) $reply);
        }

        return $reply;
    }

    /**
     * @template T
     *
     * @param callable():T $operation
     *
     * @return T
     */
    private function guard(callable $operation): mixed
    {
        try {
            return $operation();
        } catch (OutboxException $e) {
            throw $e;
        } catch (\Throwable $e) {
            throw OutboxException::with('store_unavailable', $e->getMessage(), $e);
        }
    }
}
