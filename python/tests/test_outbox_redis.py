"""RedisOutbox against a real Redis: set ``SANTATI_TEST_REDIS_URL`` to run it."""

from __future__ import annotations

import os
import uuid

import pytest

redis = pytest.importorskip("redis")

from santati.outbox.redis import RedisOutbox

URL = os.environ.get("SANTATI_TEST_REDIS_URL")


@pytest.mark.skipif(not URL, reason="SANTATI_TEST_REDIS_URL not set")
def test_claims_fifo_acks_and_redelivers_stale_entries() -> None:
    client = redis.Redis.from_url(URL)
    key = f"santati:test:{uuid.uuid4()}"
    try:
        store = RedisOutbox(client, key=key)
        events = [{"event": f"e{i}", "trail": "t", "idempotency_key": f"k{i}"} for i in (1, 2, 3)]
        for event in events:
            store.enqueue(event)  # type: ignore[arg-type]

        first = store.claim(2)
        assert [entry.event for entry in first] == events[:2]
        assert all(entry.id for entry in first)
        assert first[0].id != first[1].id
        rest = store.claim(5)
        assert [entry.event for entry in rest] == events[2:]
        assert store.claim(5) == []

        store.ack([first[0].id, first[1].id])
        # A second consumer re-delivers what the first left pending: the acked
        # entries are gone, the third (never released explicitly) comes back.
        other = RedisOutbox(client, key=key, visibility_ms=0)
        stale = other.claim(10)
        assert [entry.event for entry in stale] == events[2:]

        other.ack([stale[0].id])
        assert store.claim(10) == []
        assert other.claim(10) == []
        assert client.xlen(key) == 0
    finally:
        client.delete(key)
        client.close()
