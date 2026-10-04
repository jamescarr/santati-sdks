"""A Redis Streams outbox store (``pip install "santati[redis]"``).

    import redis
    import santati
    from santati.outbox.redis import RedisOutbox

    client = santati.Santati("sat_sk_...", trail="billing", outbox=RedisOutbox(redis.Redis()))

Events wait in one stream read through the consumer group ``santati``; every
Santati SDK uses the same layout, so any of them can drain what another
enqueued. An entry a consumer claimed but never acknowledged is re-delivered
once it has been pending for ``visibility_ms``.
"""

from __future__ import annotations

import json
import threading
import uuid
from collections.abc import Sequence
from typing import Any, cast

import redis

from .._errors import OutboxError
from .._outbox import OutboxEntry
from .._types import EventInput

_GROUP = "santati"


class RedisOutbox:
    """An :class:`santati.OutboxStore` on a Redis stream, through the caller's own ``redis.Redis``."""

    def __init__(self, client: redis.Redis, *, key: str = "santati:outbox", visibility_ms: int = 60000) -> None:
        self.key = key
        self.visibility_ms = visibility_ms
        self._client = client
        self._consumer = str(uuid.uuid4())
        self._ready = False
        self._ready_lock = threading.Lock()

    def enqueue(self, event: EventInput) -> None:
        payload = json.dumps(event, separators=(",", ":"))
        self._command(lambda: self._client.xadd(self.key, {"event": payload}))

    def claim(self, limit: int) -> list[OutboxEntry]:
        if limit < 1:
            return []
        entries: list[OutboxEntry] = []
        reply = self._command(
            lambda: self._client.xautoclaim(
                self.key, _GROUP, self._consumer, min_idle_time=self.visibility_ms, start_id="0-0", count=limit
            )
        )
        entries.extend(self._entries(reply[1]))
        if len(entries) < limit:
            reply = self._command(
                lambda: self._client.xreadgroup(_GROUP, self._consumer, {self.key: ">"}, count=limit - len(entries))
            )
            for messages in _stream_messages(reply):
                entries.extend(self._entries(messages))
        return entries

    def ack(self, ids: Sequence[str]) -> None:
        if ids:
            self._command(lambda: self._client.xack(self.key, _GROUP, *ids))
            self._command(lambda: self._client.xdel(self.key, *ids))

    def release(self, ids: Sequence[str]) -> None:
        """Nothing to do: the entries stay pending and are re-delivered after ``visibility_ms``."""

    def _entries(self, messages: Any) -> list[OutboxEntry]:
        entries: list[OutboxEntry] = []
        poison: list[str] = []
        for message_id, fields in messages or []:
            if message_id is None:  # an entry deleted while it was pending
                continue
            entry_id = _text(message_id)
            raw = (fields or {}).get(b"event", (fields or {}).get("event"))
            try:
                event = json.loads(raw) if raw is not None else None
            except ValueError:
                event = None
            if not isinstance(event, dict):
                poison.append(entry_id)
                continue
            entries.append(OutboxEntry(id=entry_id, event=cast(EventInput, event)))
        if poison:
            self.ack(poison)
        return entries

    def _command(self, command: Any) -> Any:
        """Run one command after creating the group once, mapping client errors."""
        try:
            if not self._ready:
                with self._ready_lock:
                    if not self._ready:
                        self._create_group()
                        self._ready = True
            return command()
        except redis.RedisError as err:
            raise OutboxError(str(err), code="store_unavailable") from err

    def _create_group(self) -> None:
        try:
            self._client.xgroup_create(self.key, _GROUP, id="0", mkstream=True)
        except redis.ResponseError as err:
            if not str(err).startswith("BUSYGROUP"):
                raise


def _stream_messages(reply: Any) -> list[Any]:
    """The message lists of an ``XREADGROUP`` reply, RESP2 (list) or RESP3 (dict)."""
    if not reply:
        return []
    if isinstance(reply, dict):
        return [messages for value in reply.values() for messages in value]
    return [messages for _stream, messages in reply]


def _text(value: bytes | str) -> str:
    return value.decode() if isinstance(value, bytes) else value
