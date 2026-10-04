<?php

declare(strict_types=1);

namespace Santati\Tests;

use PHPUnit\Framework\TestCase;
use Santati\Client;
use Santati\Exception\OutboxException;
use Santati\Outbox\MemoryOutbox;
use Santati\SendOutcome;

/**
 * The outbox behind `Client::log()`, without a server.
 */
final class OutboxTest extends TestCase
{
    public function test_unexpected_send_failure_is_reported_and_dropped(): void
    {
        $store = new MemoryOutbox();
        /** @var list<SendOutcome> $outcomes */
        $outcomes = [];
        $client = new Client(
            'sat_sk_x',
            baseUrl: 'http://127.0.0.1:1',
            outbox: $store,
            // Not an event: emitBatch fails on it with a TypeError, not a SantatiException.
            preSend: static fn (array $event): string => 'oops',
            postSend: static function (array $event, SendOutcome $outcome) use (&$outcomes): void {
                $outcomes[] = $outcome;
            },
        );

        $client->log(['event' => 'a.b', 'trail' => 't']);
        $client->flush();

        self::assertCount(1, $outcomes);
        self::assertSame('failed', $outcomes[0]->status);
        self::assertInstanceOf(OutboxException::class, $outcomes[0]->error);
        self::assertSame('hook_failed', $outcomes[0]->error->getErrorCode());
        self::assertSame([], $store->claim(10));
        $client->close();
    }
}
