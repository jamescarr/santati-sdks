<?php

declare(strict_types=1);

namespace Santati\Tests;

use PHPUnit\Framework\TestCase;
use Predis\Client as Redis;
use Santati\Outbox\OutboxEntry;
use Santati\Outbox\RedisOutbox;

/**
 * Drives `RedisOutbox` against the Redis at `SANTATI_TEST_REDIS_URL`.
 */
final class RedisOutboxTest extends TestCase
{
    public function test_stream_layout_claim_ack_and_redelivery(): void
    {
        $url = getenv('SANTATI_TEST_REDIS_URL');

        if ($url === false || $url === '') {
            self::markTestSkipped('SANTATI_TEST_REDIS_URL not set');
        }

        $redis = new Redis($url);
        $key = 'santati:test:' . bin2hex(random_bytes(8));

        try {
            $store = new RedisOutbox($redis, $key);
            $events = [];

            foreach ([1, 2, 3] as $i) {
                $events[$i] = ['event' => "e$i", 'trail' => 't', 'idempotency_key' => "k$i"];
                $store->enqueue($events[$i]);
            }

            $first = $store->claim(2);
            self::assertSame([$events[1], $events[2]], self::events($first));
            self::assertCount(2, array_unique(array_map(static fn (OutboxEntry $e): string => $e->id, $first)));
            self::assertNotSame('', $first[0]->id);

            $second = $store->claim(5);
            self::assertSame([$events[3]], self::events($second));
            self::assertSame([], $store->claim(5));

            $store->ack([$first[0]->id, $first[1]->id]);

            $other = new RedisOutbox($redis, $key, 0);
            $again = $other->claim(10);
            self::assertSame([$events[3]], self::events($again));

            $store->ack([$second[0]->id]);
            self::assertSame([], $store->claim(5));
            self::assertSame([], $other->claim(5));
            self::assertSame(0, $redis->xlen($key));
        } finally {
            $redis->del([$key]);
        }
    }

    public function test_empty_maps_are_objects_on_the_wire(): void
    {
        $url = getenv('SANTATI_TEST_REDIS_URL');

        if ($url === false || $url === '') {
            self::markTestSkipped('SANTATI_TEST_REDIS_URL not set');
        }

        $redis = new Redis($url);
        $key = 'santati:test:' . bin2hex(random_bytes(8));

        try {
            $client = new \Santati\Client(apiKey: 'sat_sk_x', trail: 't', outbox: new RedisOutbox($redis, $key));
            $client->log([
                'event' => 'a.b',
                'organization_id' => null,
                'unknown' => 1,
                'metadata' => [],
                'actor' => ['type' => 'user', 'id' => 'u', 'name' => null, 'metadata' => []],
                'targets' => [['type' => 'x', 'id' => 'y', 'metadata' => []]],
            ]);

            $entries = $redis->xrange($key, '-', '+');
            $json = array_values($entries)[0]['event'];
            $wire = json_decode($json);

            self::assertEquals(new \stdClass(), $wire->metadata);
            self::assertEquals(new \stdClass(), $wire->actor->metadata);
            self::assertEquals(new \stdClass(), $wire->targets[0]->metadata);
            self::assertSame(
                ['event', 'trail', 'idempotency_key', 'actor', 'targets', 'metadata'],
                array_keys(get_object_vars($wire))
            );
            self::assertSame(['type', 'id', 'metadata'], array_keys(get_object_vars($wire->actor)));
        } finally {
            $redis->del([$key]);
        }
    }

    /**
     * @param list<OutboxEntry> $entries
     *
     * @return list<array<string, mixed>>
     */
    private static function events(array $entries): array
    {
        return array_map(static fn (OutboxEntry $e): array => $e->event, $entries);
    }
}
