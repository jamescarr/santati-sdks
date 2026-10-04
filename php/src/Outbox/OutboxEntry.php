<?php

declare(strict_types=1);

namespace Santati\Outbox;

/**
 * One claimed outbox entry: the store's id and the stored event.
 */
final readonly class OutboxEntry
{
    /**
     * @param array<string, mixed> $event
     */
    public function __construct(
        public string $id,
        public array $event,
    ) {
    }
}
