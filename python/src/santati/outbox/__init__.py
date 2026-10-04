"""Outbox stores for a client's ``outbox`` option.

:class:`MemoryOutbox` is the in-process store; :mod:`santati.outbox.redis` holds the
Redis Streams store (``pip install "santati[redis]"``). Implement
:class:`OutboxStore` for anything else.
"""

from __future__ import annotations

from .._outbox import MemoryOutbox, OutboxEntry, OutboxStore, SendOutcome

__all__ = ["MemoryOutbox", "OutboxEntry", "OutboxStore", "SendOutcome"]
