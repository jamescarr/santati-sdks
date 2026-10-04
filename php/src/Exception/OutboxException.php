<?php

declare(strict_types=1);

namespace Santati\Exception;

/**
 * The outbox store refused or failed (`outbox_full`, `store_unavailable`,
 * `closed`), or a `pre_send` hook raised (`hook_failed`). The status is null.
 */
final class OutboxException extends SantatiException
{
    public static function with(string $code, string $message, ?\Throwable $previous = null): self
    {
        return new self($message, null, $code, null, null, $previous);
    }
}
