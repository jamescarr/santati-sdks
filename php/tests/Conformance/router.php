<?php

declare(strict_types=1);

/**
 * Mock gateway for the conformance runner.
 *
 * Answers every request from `$SANTATI_CONFORMANCE_DIR/gateway.json` and
 * appends each request it saw to `$SANTATI_CONFORMANCE_DIR/requests.jsonl`;
 * the response taken from a `sequence` is the index of the number of requests
 * already recorded.
 */

$dir = getenv('SANTATI_CONFORMANCE_DIR');

if (!is_string($dir) || $dir === '') {
    http_response_code(500);
    echo 'SANTATI_CONFORMANCE_DIR is not set';

    return;
}

/** @var array<string, mixed> $gateway */
$gateway = json_decode((string) file_get_contents($dir . '/gateway.json'), true, 512, JSON_THROW_ON_ERROR);

$headers = [];

if (function_exists('getallheaders')) {
    foreach ((array) getallheaders() as $name => $value) {
        $headers[strtolower((string) $name)] = $value;
    }
}

foreach ($_SERVER as $name => $value) {
    if (str_starts_with((string) $name, 'HTTP_')) {
        $header = strtolower(str_replace('_', '-', substr((string) $name, 5)));

        if (!array_key_exists($header, $headers)) {
            $headers[$header] = $value;
        }
    }
}

foreach (['CONTENT_TYPE' => 'content-type', 'CONTENT_LENGTH' => 'content-length'] as $server => $header) {
    if (isset($_SERVER[$server]) && !array_key_exists($header, $headers)) {
        $headers[$header] = $_SERVER[$server];
    }
}

$raw = (string) file_get_contents('php://input');

$record = [
    'method' => (string) ($_SERVER['REQUEST_METHOD'] ?? 'GET'),
    'path' => (string) ($_SERVER['REQUEST_URI'] ?? '/'),
    'headers' => $headers,
    'body' => $raw === '' ? null : json_decode($raw, true),
];

$file = $dir . '/requests.jsonl';
$count = 0;

if (is_file($file)) {
    foreach (file($file, FILE_IGNORE_NEW_LINES | FILE_SKIP_EMPTY_LINES) ?: [] as $line) {
        ++$count;
    }
}

file_put_contents($file, json_encode($record, JSON_THROW_ON_ERROR) . "\n", FILE_APPEND | LOCK_EX);

$sequence = $gateway['sequence'] ?? null;
$response = is_array($sequence) ? $sequence[min($count, count($sequence) - 1)] : $gateway;

if (isset($response['delay_ms'])) {
    usleep(((int) $response['delay_ms']) * 1000);
}

http_response_code((int) ($response['status'] ?? 200));

$declared = [];

foreach (($response['headers'] ?? []) as $name => $value) {
    $declared[strtolower((string) $name)] = (string) $value;
    header($name . ': ' . $value);
}

$body = '';

if (isset($response['body'])) {
    if (array_key_exists('json', $response['body'])) {
        $body = json_encode($response['body']['json'], JSON_THROW_ON_ERROR);

        if (!array_key_exists('content-type', $declared)) {
            header('Content-Type: application/json');
        }
    } else {
        $body = (string) ($response['body']['text'] ?? '');

        if (!array_key_exists('content-type', $declared)) {
            header('Content-Type: text/plain; charset=utf-8');
        }
    }
}

header('Content-Length: ' . strlen($body));
echo $body;
