<?php

declare(strict_types=1);

namespace Santati\Exception;

/**
 * No HTTP response came back: refused connection, DNS or TLS failure, timeout.
 */
final class TransportException extends SantatiException
{
}
