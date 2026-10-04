<?php

declare(strict_types=1);

namespace Santati\Outbox;

use Santati\Exception\OutboxException;
use Santati\Exception\ValidationException;

/**
 * A bounded in-memory outbox: a FIFO queue plus the claimed entries.
 */
final class MemoryOutbox implements OutboxStore
{
    /**
     * @var list<OutboxEntry>
     */
    private array $pending = [];

    /**
     * @var array<string, OutboxEntry>
     */
    private array $claimed = [];

    private int $counter = 0;

    /**
     * @throws ValidationException when `$maxPending` is below 1
     */
    public function __construct(private readonly int $maxPending = 10000)
    {
        if ($maxPending < 1) {
            throw new ValidationException('max_pending must be at least 1', null, null, 'max_pending');
        }
    }

    public function enqueue(array $event): void
    {
        if (count($this->pending) + count($this->claimed) >= $this->maxPending) {
            throw OutboxException::with('outbox_full', 'the outbox is full (' . $this->maxPending . ' events)');
        }

        $this->pending[] = new OutboxEntry((string) ++$this->counter, $event);
    }

    public function claim(int $limit): array
    {
        if ($limit < 1) {
            return [];
        }

        $entries = array_splice($this->pending, 0, $limit);

        foreach ($entries as $entry) {
            $this->claimed[$entry->id] = $entry;
        }

        return $entries;
    }

    public function ack(array $ids): void
    {
        foreach ($ids as $id) {
            unset($this->claimed[$id]);
        }
    }

    public function release(array $ids): void
    {
        $entries = [];

        foreach ($ids as $id) {
            if (isset($this->claimed[$id])) {
                $entries[] = $this->claimed[$id];
                unset($this->claimed[$id]);
            }
        }

        array_unshift($this->pending, ...$entries);
    }
}
