<?php

declare(strict_types=1);

namespace Santati\Exception;

/**
 * A locally rejected input (status null) or an HTTP 400/413/422 response; an
 * HTTP rejection with the code `schema_validation_failed` is the subclass
 * {@see SchemaValidationException}.
 */
class ValidationException extends SantatiException
{
}
