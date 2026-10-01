<?php

declare(strict_types=1);

namespace Santati\Exception;

/**
 * HTTP 429. Retried unless the error code is `quota_exceeded`.
 */
final class RateLimitedException extends SantatiException
{
}
