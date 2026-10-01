<?php

declare(strict_types=1);

namespace Santati;

/**
 * The per-item outcome of a batch emit (HTTP 202 or 207).
 */
final readonly class BatchResult
{
    /**
     * @param list<BatchItem> $results
     */
    public function __construct(
        public int $accepted,
        public int $rejected,
        public array $results,
    ) {
    }
}
