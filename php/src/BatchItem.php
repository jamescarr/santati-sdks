<?php

declare(strict_types=1);

namespace Santati;

/**
 * One entry of a batch response: accepted, duplicate or rejected.
 */
final readonly class BatchItem
{
    public function __construct(
        public int $index,
        public string $status,
        public ?string $id = null,
        public ?BatchItemError $error = null,
    ) {
    }
}
