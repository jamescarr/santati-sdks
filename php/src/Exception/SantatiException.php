<?php

declare(strict_types=1);

namespace Santati\Exception;

/**
 * Base class for every error the Santati SDK raises.
 *
 * `status` is null when no HTTP response was involved (local validation, a
 * transport failure). `errorCode`/`field` mirror the server's error body and
 * `retryAfter` is the `Retry-After` header in seconds, when it is an integer.
 */
class SantatiException extends \RuntimeException
{
    public function __construct(
        string $message,
        private readonly ?int $status = null,
        private readonly ?string $errorCode = null,
        private readonly ?string $field = null,
        private readonly ?int $retryAfter = null,
        ?\Throwable $previous = null,
    ) {
        parent::__construct($message, $status ?? 0, $previous);
    }

    public function getStatus(): ?int
    {
        return $this->status;
    }

    public function getErrorCode(): ?string
    {
        return $this->errorCode;
    }

    public function getField(): ?string
    {
        return $this->field;
    }

    public function getRetryAfter(): ?int
    {
        return $this->retryAfter;
    }
}
