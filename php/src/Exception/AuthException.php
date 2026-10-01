<?php

declare(strict_types=1);

namespace Santati\Exception;

/**
 * HTTP 401 or 403: the API key is missing, invalid or under-scoped.
 */
final class AuthException extends SantatiException
{
}
