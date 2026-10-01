<?php

declare(strict_types=1);

namespace Santati\Exception;

/**
 * A locally rejected input (status null) or an HTTP 400/413/422 response.
 */
final class ValidationException extends SantatiException
{
}
