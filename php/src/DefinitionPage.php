<?php

declare(strict_types=1);

namespace Santati;

use Santati\Core\Model\EventDefinition;

/**
 * One page of event definitions plus the cursor of the next page, if any.
 */
final readonly class DefinitionPage
{
    /**
     * @param list<EventDefinition> $results
     */
    public function __construct(
        public array $results,
        public ?string $nextCursor = null,
    ) {
    }
}
