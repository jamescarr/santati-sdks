<?php

declare(strict_types=1);

namespace Santati;

use Santati\Exception\SantatiException;

/**
 * What happened to one queued event, as handed to the `post_send` hook.
 *
 * `status` is `accepted`, `duplicate`, `rejected` or `failed`.
 */
final readonly class SendOutcome
{
    public function __construct(
        public string $status,
        public ?string $id = null,
        public ?SantatiException $error = null,
    ) {
    }
}
