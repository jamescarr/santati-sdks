<?php

declare(strict_types=1);

namespace Santati\Exception;

/**
 * The server rejected the event against the action's JSON Schema, a disallowed
 * target type, or an unusable `schema_version` pin: an HTTP 400, 413 or 422
 * whose code is `schema_validation_failed`. A {@see ValidationException}, so
 * `catch (ValidationException)` still catches it.
 */
final class SchemaValidationException extends ValidationException
{
}
