<?php

declare(strict_types=1);

namespace Santati\Outbox;

use Santati\Exception\OutboxException;

/**
 * Where `Client::log()` keeps stored events until a pass sends them.
 *
 * A stored event is the emit input with `trail` and `idempotency_key` filled
 * in; JSON-encoded it is the wire envelope.
 */
interface OutboxStore
{
    /**
     * Stores the event at the tail.
     *
     * @param array<string, mixed> $event
     *
     * @throws OutboxException `outbox_full`, or any other failure
     */
    public function enqueue(array $event): void;

    /**
     * Returns up to `$limit` oldest entries, FIFO. Claimed entries are not
     * returned again until released (or, for Redis, until stale).
     *
     * @return list<OutboxEntry>
     */
    public function claim(int $limit): array;

    /**
     * Deletes the entries permanently.
     *
     * @param list<string> $ids
     */
    public function ack(array $ids): void;

    /**
     * Makes the entries eligible for a later claim.
     *
     * @param list<string> $ids
     */
    public function release(array $ids): void;
}
