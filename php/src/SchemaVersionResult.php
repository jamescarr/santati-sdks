<?php

declare(strict_types=1);

namespace Santati;

use Santati\Core\Model\EventSchemaVersion;

/**
 * A schema version and the `ETag` header its response carried (null when
 * absent), to pass back as `$ifMatch` to `Schemas::updateVersion()`.
 */
final readonly class SchemaVersionResult
{
    public function __construct(
        public EventSchemaVersion $schemaVersion,
        public ?string $etag = null,
    ) {
    }
}
