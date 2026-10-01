<?php

declare(strict_types=1);

namespace Santati;

/**
 * Why one batch item was rejected.
 */
final readonly class BatchItemError
{
    public function __construct(
        public string $code,
        public string $message,
        public ?string $field = null,
    ) {
    }
}
