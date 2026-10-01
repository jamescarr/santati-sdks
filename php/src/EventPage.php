<?php

declare(strict_types=1);

namespace Santati;

use Santati\Core\Model\AuditEvent;

/**
 * One page of audit events plus the cursor of the next page, if any.
 */
final readonly class EventPage
{
    /**
     * @param list<AuditEvent> $results
     */
    public function __construct(
        public array $results,
        public ?string $nextCursor = null,
    ) {
    }
}
