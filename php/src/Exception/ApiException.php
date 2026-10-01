<?php

declare(strict_types=1);

namespace Santati\Exception;

/**
 * Any other non-2xx response, an unexpected 2xx status or an undecodable 2xx body.
 */
final class ApiException extends SantatiException
{
}
