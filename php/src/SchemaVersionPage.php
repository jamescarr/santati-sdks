<?php

declare(strict_types=1);

namespace Santati;

use Santati\Core\Model\EventSchemaVersion;

/**
 * One page of an action's schema versions (newest first) plus the cursor of the
 * next page, if any.
 */
final readonly class SchemaVersionPage
{
    /**
     * @param list<EventSchemaVersion> $results
     */
    public function __construct(
        public array $results,
        public ?string $nextCursor = null,
    ) {
    }
}
