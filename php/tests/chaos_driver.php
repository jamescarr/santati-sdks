<?php

/**
 * Chaos driver: emits into the outbox against the gateway in SANTATI_CHAOS and
 * prints one CHAOS_RESULT line. Run by `mise run chaos php` (chaos/run.mjs);
 * inert unless SANTATI_CHAOS is set.
 *
 * PHP has no background worker and no event loop: one plain emit loop, no
 * heartbeat, and the outbox drains only on flush() and close().
 */

declare(strict_types=1);

require __DIR__ . '/../vendor/autoload.php';

use Santati\Client;
use Santati\Exception\SantatiException;
use Santati\Outbox\MemoryOutbox;

function nowMs(): float
{
    return hrtime(true) / 1e6;
}

/**
 * @param list<float> $sorted
 */
function percentile(array $sorted, float $q): float
{
    if ($sorted === []) {
        return 0.0;
    }

    return $sorted[max((int) ceil($q * count($sorted)) - 1, 0)];
}

function errorKey(\Throwable $e): string
{
    $kind = preg_replace('/Exception$/', 'Error', (new \ReflectionClass($e))->getShortName());

    return $kind . ':' . ($e instanceof SantatiException ? $e->getErrorCode() : 'unexpected');
}

$raw = getenv('SANTATI_CHAOS');
if ($raw === false || $raw === '') {
    echo "SANTATI_CHAOS not set; skipping\n";
    exit(0);
}

$cfg = json_decode($raw, true, 512, JSON_THROW_ON_ERROR);
$opts = $cfg['client'];

$client = new Client(
    apiKey: 'sat_sk_chaos',
    baseUrl: $opts['base_url'],
    trail: 'chaos',
    timeoutMs: $opts['timeout_ms'],
    maxRetries: $opts['max_retries'],
    initialBackoffMs: $opts['initial_backoff_ms'],
    maxBackoffMs: $opts['max_backoff_ms'],
    outbox: new MemoryOutbox($opts['max_pending']),
    batchSize: $opts['batch_size'],
);

$latencies = [];
$queued = 0;
$errors = [];
$flushCalls = 0;
$flushEveryMs = (float) $cfg['flusher_interval_ms'];
$lastFlush = nowMs();

for ($emitter = 0; $emitter < $cfg['emitters']; $emitter++) {
    for ($seq = 0; $seq < $cfg['events_per_emitter']; $seq++) {
        $start = nowMs();

        try {
            $result = $client->events->emit([
                'event' => 'chaos.event',
                'metadata' => ['emitter' => (string) $emitter, 'seq' => (string) $seq],
            ]);
            if ($result->queued) {
                $queued++;
            }
        } catch (\Throwable $e) {
            $key = errorKey($e);
            $errors[$key] = ($errors[$key] ?? 0) + 1;
        }

        $latencies[] = nowMs() - $start;

        if ($flushEveryMs > 0 && nowMs() - $lastFlush >= $flushEveryMs) {
            $flushCalls++;

            try {
                $client->flush();
            } catch (SantatiException) {
                // Send failures go through postSend; the driver only counts calls.
            }

            $lastFlush = nowMs();
        }

        if ($cfg['emit_interval_ms'] > 0) {
            usleep((int) ($cfg['emit_interval_ms'] * 1000));
        }
    }
}

$closeStart = nowMs();

try {
    $client->close();
} catch (\Throwable $e) {
    $key = errorKey($e);
    $errors[$key] = ($errors[$key] ?? 0) + 1;
}

$closeMs = nowMs() - $closeStart;

sort($latencies);
echo 'CHAOS_RESULT ' . json_encode([
    'sdk' => 'php',
    'emits' => count($latencies),
    'queued' => $queued,
    'errors' => (object) $errors,
    'caller_exits' => 0,
    'emit_ms' => [
        'p50' => round(percentile($latencies, 0.50), 3),
        'p99' => round(percentile($latencies, 0.99), 3),
        'max' => round($latencies === [] ? 0.0 : $latencies[count($latencies) - 1], 3),
    ],
    'heartbeat_max_lag_ms' => null,
    'close_ms' => round($closeMs, 3),
    'flush_calls' => $flushCalls,
], JSON_THROW_ON_ERROR) . "\n";
