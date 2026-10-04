"""Chaos driver: emits into the outbox against the gateway in SANTATI_CHAOS and prints one CHAOS_RESULT line.

Run by `mise run chaos python` (chaos/run.mjs); inert unless SANTATI_CHAOS is set.
"""

from __future__ import annotations

import json
import math
import os
import sys
import threading
import time
from collections import Counter
from typing import Any

import santati


def percentile(sorted_values: list[float], q: float) -> float:
    if not sorted_values:
        return 0.0
    return sorted_values[max(math.ceil(q * len(sorted_values)) - 1, 0)]


def main() -> int:
    raw = os.environ.get("SANTATI_CHAOS")
    if not raw:
        print("SANTATI_CHAOS not set; skipping")
        return 0
    cfg: dict[str, Any] = json.loads(raw)
    opts = cfg["client"]

    client = santati.Santati(
        "sat_sk_chaos",
        base_url=opts["base_url"],
        trail="chaos",
        timeout_ms=opts["timeout_ms"],
        max_retries=opts["max_retries"],
        initial_backoff_ms=opts["initial_backoff_ms"],
        max_backoff_ms=opts["max_backoff_ms"],
        batch_size=opts["batch_size"],
        flush_interval_ms=opts["flush_interval_ms"],
        outbox=santati.MemoryOutbox(opts["max_pending"]),
    )

    emits_done = threading.Event()
    all_done = threading.Event()
    heartbeat_s = cfg["heartbeat_ms"] / 1000
    max_lag = [0.0]

    def heartbeat() -> None:
        while not all_done.is_set():
            start = time.monotonic()
            time.sleep(heartbeat_s)
            lag = (time.monotonic() - start - heartbeat_s) * 1000
            max_lag[0] = max(max_lag[0], lag)

    flush_calls = [0]

    def flusher() -> None:
        interval_s = cfg["flusher_interval_ms"] / 1000
        while not emits_done.wait(interval_s):
            flush_calls[0] += 1
            try:
                client.flush()
            except santati.SantatiError:
                pass

    latencies: list[list[float]] = [[] for _ in range(cfg["emitters"])]
    queued = [0] * cfg["emitters"]
    errors: list[Counter[str]] = [Counter() for _ in range(cfg["emitters"])]
    crashes = [0] * cfg["emitters"]

    def emitter(index: int) -> None:
        interval_s = cfg["emit_interval_ms"] / 1000
        try:
            for seq in range(cfg["events_per_emitter"]):
                start = time.perf_counter()
                try:
                    result = client.events.emit("chaos.event", metadata={"emitter": str(index), "seq": str(seq)})
                    if result.queued:
                        queued[index] += 1
                except santati.SantatiError as error:
                    errors[index][f"{type(error).__name__}:{error.code}"] += 1
                except Exception as error:  # noqa: BLE001 - a non-SDK failure is a finding, not a crash
                    errors[index][f"{type(error).__name__}:unexpected"] += 1
                latencies[index].append((time.perf_counter() - start) * 1000)
                if interval_s > 0:
                    time.sleep(interval_s)
        except BaseException:
            crashes[index] += 1
            raise

    heartbeat_thread = threading.Thread(target=heartbeat, daemon=True)
    heartbeat_thread.start()
    flusher_thread = None
    if cfg["flusher_interval_ms"] > 0:
        flusher_thread = threading.Thread(target=flusher, daemon=True)
        flusher_thread.start()

    emitters = [threading.Thread(target=emitter, args=(i,), daemon=True) for i in range(cfg["emitters"])]
    for thread in emitters:
        thread.start()
    for thread in emitters:
        thread.join()
    emits_done.set()
    if flusher_thread is not None:
        flusher_thread.join()

    close_start = time.perf_counter()
    try:
        client.close()
    except santati.SantatiError as error:
        errors[0][f"{type(error).__name__}:{error.code}"] += 1
    close_ms = (time.perf_counter() - close_start) * 1000

    all_done.set()
    heartbeat_thread.join()

    samples = sorted(ms for per in latencies for ms in per)
    total_errors: Counter[str] = Counter()
    for per in errors:
        total_errors.update(per)
    result = {
        "sdk": "python",
        "emits": len(samples),
        "queued": sum(queued),
        "errors": dict(total_errors),
        "caller_exits": sum(crashes),
        "emit_ms": {
            "p50": round(percentile(samples, 0.50), 3),
            "p99": round(percentile(samples, 0.99), 3),
            "max": round(samples[-1] if samples else 0.0, 3),
        },
        "heartbeat_max_lag_ms": round(max_lag[0], 3),
        "close_ms": round(close_ms, 3),
        "flush_calls": flush_calls[0],
    }
    print("CHAOS_RESULT " + json.dumps(result), flush=True)
    return 0


if __name__ == "__main__":
    sys.exit(main())
