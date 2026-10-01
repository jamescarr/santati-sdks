<?php

declare(strict_types=1);

namespace Santati;

use Santati\Core\Model\AuditEvent;

/**
 * The stored (or replayed) event of a single emit.
 */
final readonly class EmitResult
{
    public function __construct(
        public AuditEvent $event,
        public bool $duplicate,
        public string $idempotencyKey,
    ) {
    }
}
