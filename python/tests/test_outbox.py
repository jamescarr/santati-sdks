"""The outbox behind a queued ``events.emit``, without a server."""

from __future__ import annotations

from typing import Any

import santati


def _client(store: santati.MemoryOutbox, **options: Any) -> santati.Santati:
    return santati.Santati("sat_sk_x", base_url="http://127.0.0.1:1", outbox=store, flush_interval_ms=60000, **options)


def test_unexpected_send_failure_is_reported_and_dropped() -> None:
    store = santati.MemoryOutbox()
    outcomes: list[santati.SendOutcome] = []
    # Not an event: emit_batch fails on it with an AttributeError, not a SantatiError.
    client = _client(store, pre_send=lambda _e: "oops", post_send=lambda _e, o: outcomes.append(o))

    client.events.emit("a.b", trail="t")
    client.flush()

    assert len(outcomes) == 1
    assert outcomes[0].status == "failed"
    assert type(outcomes[0].error) is santati.OutboxError
    assert outcomes[0].error.code == "hook_failed"
    assert store.claim(10) == []
    client.close()


def test_queued_emit_snapshots_the_event() -> None:
    store = santati.MemoryOutbox()
    client = _client(store)
    meta = {"a": "1"}

    client.events.emit("a.b", trail="t", metadata=meta)
    meta["a"] = "2"

    assert store.claim(1)[0].event["metadata"] == {"a": "1"}
    client.close()
