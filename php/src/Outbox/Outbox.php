<?php

declare(strict_types=1);

namespace Santati\Outbox;

use Santati\Events;
use Santati\Exception\OutboxException;
use Santati\Exception\RateLimitedException;
use Santati\Exception\SantatiException;
use Santati\Exception\ServerException;
use Santati\Exception\TransportException;
use Santati\Exception\ValidationException;
use Santati\SendOutcome;

/**
 * The outbox behind `Client::log()`: stores events, and drains the store in
 * batches through `Events::emitBatch()` on `flush()`.
 *
 * PHP has no background worker; passes run on `flush()`, `close()` and, for
 * what is still pending, at shutdown.
 *
 * @internal
 */
final class Outbox
{
    private bool $closed = false;

    private bool $shutdownRegistered = false;

    private bool $passing = false;

    /**
     * @param (callable(array<string, mixed>): (array<string, mixed>|null))|null $preSend
     * @param (callable(array<string, mixed>, SendOutcome): mixed)|null          $postSend
     */
    public function __construct(
        private readonly Events $events,
        private readonly OutboxStore $store,
        private readonly int $batchSize,
        private readonly mixed $preSend,
        private readonly mixed $postSend,
    ) {
    }

    /**
     * @param array<string, mixed> $stored
     *
     * @throws OutboxException
     */
    public function log(array $stored): string
    {
        if ($this->closed) {
            throw OutboxException::with('closed', 'client is closed');
        }

        try {
            $this->store->enqueue($stored);
        } catch (SantatiException $e) {
            throw $e;
        } catch (\Throwable $e) {
            throw OutboxException::with('store_unavailable', $e->getMessage(), $e);
        }

        if (!$this->shutdownRegistered) {
            $this->shutdownRegistered = true;
            register_shutdown_function(function (): void {
                if ($this->closed) {
                    return;
                }

                try {
                    $this->flush();
                } catch (\Throwable) {
                    // Nothing can handle it at shutdown; the entries stay in the store.
                }
            });
        }

        return (string) $stored['idempotency_key'];
    }

    /**
     * One pass: claim, hook, send, ack or release, until the store is drained
     * or a batch was released.
     *
     * @throws OutboxException when the store fails
     */
    public function flush(): void
    {
        if ($this->passing) {
            return; // called from inside a hook
        }

        $this->passing = true;

        try {
            $this->pass();
        } finally {
            $this->passing = false;
        }
    }

    public function close(): void
    {
        if ($this->closed) {
            return;
        }

        $this->flush();
        $this->closed = true;
    }

    private function pass(): void
    {
        while (true) {
            $entries = $this->guard(fn (): array => $this->store->claim($this->batchSize));

            if ($entries === []) {
                return;
            }

            $released = false;
            $toSend = [];

            foreach ($entries as $entry) {
                $out = $entry->event;

                if ($this->preSend !== null) {
                    try {
                        $out = ($this->preSend)($entry->event);
                    } catch (\Throwable $e) {
                        $this->guard(fn () => $this->store->release([$entry->id]));
                        $released = true;
                        $this->notify($entry->event, new SendOutcome(
                            'failed',
                            null,
                            OutboxException::with('hook_failed', $e->getMessage(), $e)
                        ));

                        continue;
                    }
                }

                if ($out === null) {
                    $this->guard(fn () => $this->store->ack([$entry->id]));

                    continue;
                }

                $toSend[] = [$entry, $out];
            }

            if ($toSend !== []) {
                $ids = array_map(static fn (array $pair): string => $pair[0]->id, $toSend);

                try {
                    [$status, $result] = $this->events->emitBatchWithStatus(array_map(static fn (array $pair): array => $pair[1], $toSend));
                } catch (SantatiException $e) {
                    foreach ($toSend as [$entry]) {
                        $this->notify($entry->event, new SendOutcome('failed', null, $e));
                    }

                    if ($e instanceof TransportException || $e instanceof ServerException || $e instanceof RateLimitedException) {
                        $this->guard(fn () => $this->store->release($ids));
                        $released = true;
                    } else {
                        $this->guard(fn () => $this->store->ack($ids));
                    }

                    $result = null;
                    $status = 0;
                } catch (\Throwable $e) {
                    // E.g. a malformed preSend result: reported and dropped, never thrown from flush()/close().
                    $failure = OutboxException::with('hook_failed', $e->getMessage(), $e);

                    foreach ($toSend as [$entry]) {
                        $this->notify($entry->event, new SendOutcome('failed', null, $failure));
                    }

                    $this->guard(fn () => $this->store->ack($ids));
                    $result = null;
                    $status = 0;
                }

                if ($result !== null) {
                    foreach ($result->results as $item) {
                        $entry = $toSend[$item->index][0] ?? null;

                        if ($entry === null) {
                            continue;
                        }

                        $this->notify($entry->event, self::outcome($item, $status));
                    }

                    $this->guard(fn () => $this->store->ack($ids));
                }
            }

            if ($released || count($entries) < $this->batchSize) {
                return;
            }
        }
    }

    private static function outcome(\Santati\BatchItem $item, int $status): SendOutcome
    {
        if ($item->status === 'rejected') {
            $error = $item->error;

            return new SendOutcome('rejected', null, new ValidationException(
                $error?->message ?? 'rejected',
                $status,
                $error?->code,
                $error?->field
            ));
        }

        return new SendOutcome($item->status, $item->id);
    }

    /**
     * @param array<string, mixed> $event
     */
    private function notify(array $event, SendOutcome $outcome): void
    {
        if ($this->postSend === null) {
            return;
        }

        try {
            ($this->postSend)($event, $outcome);
        } catch (\Throwable) {
            // post_send exceptions are ignored.
        }
    }

    /**
     * @template T
     *
     * @param callable():T $operation
     *
     * @return T
     */
    private function guard(callable $operation): mixed
    {
        try {
            return $operation();
        } catch (SantatiException $e) {
            throw $e;
        } catch (\Throwable $e) {
            throw OutboxException::with('store_unavailable', $e->getMessage(), $e);
        }
    }
}
