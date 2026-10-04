"""The outbox behind :meth:`santati.Santati.log`.

``log`` stores the resolved envelope in an :class:`OutboxStore` and returns;
a worker thread drains the store in batches through ``emit_batch``. See
``docs/sdk-surface.md`` (Outbox, Worker, Hooks) for the contract.
"""

from __future__ import annotations

import collections
import contextlib
import copy
import itertools
import threading
from collections.abc import Callable, Sequence
from dataclasses import dataclass
from typing import TYPE_CHECKING, Any, Protocol, TypeVar

from ._errors import OutboxError, RateLimitedError, SantatiError, ServerError, TransportError, ValidationError
from ._types import BatchItem, EventInput

if TYPE_CHECKING:
    from ._client import Santati

_T = TypeVar("_T")

_RETRYABLE = (TransportError, ServerError, RateLimitedError)
"""Failures whose batch goes back to the store for a later pass."""


@dataclass(frozen=True)
class OutboxEntry:
    """One stored event as a store hands it out, with the store's id for it."""

    id: str
    event: EventInput


@dataclass(frozen=True)
class SendOutcome:
    """What happened to one logged event: ``accepted``, ``duplicate``, ``rejected`` or ``failed``."""

    status: str
    id: str | None = None
    """The stored event's id, for ``accepted`` and ``duplicate``."""
    error: SantatiError | None = None
    """Why it was ``rejected`` or ``failed``."""


PreSendHook = Callable[[EventInput], "EventInput | None"]
"""Sees each stored event before its request; returns it (possibly modified) or ``None`` to drop it."""

PostSendHook = Callable[[EventInput, SendOutcome], None]
"""Receives each stored (original) event and its outcome; exceptions are ignored."""


class OutboxStore(Protocol):
    """Where :meth:`santati.Santati.log` puts events until the worker sends them.

    Implementations must be safe to call from the worker thread and from
    ``log`` concurrently. Raise :class:`santati.OutboxError` (``outbox_full``)
    when full; any other exception becomes ``OutboxError(store_unavailable)``.
    """

    def enqueue(self, event: EventInput) -> None:
        """Store ``event`` at the tail."""
        ...

    def claim(self, limit: int) -> list[OutboxEntry]:
        """Up to ``limit`` oldest unclaimed entries, oldest first."""
        ...

    def ack(self, ids: Sequence[str]) -> None:
        """Delete these claimed entries permanently."""
        ...

    def release(self, ids: Sequence[str]) -> None:
        """Make these claimed entries eligible for a later claim."""
        ...


class MemoryOutbox:
    """The default store: a bounded in-process FIFO queue.

    ``enqueue`` raises :class:`santati.OutboxError` with code ``outbox_full``
    once ``max_pending`` events are pending or claimed.
    """

    def __init__(self, max_pending: int = 10000) -> None:
        if max_pending < 1:
            raise ValidationError("max_pending must be at least 1", field="max_pending")
        self.max_pending = max_pending
        self._pending: collections.deque[OutboxEntry] = collections.deque()
        self._claimed: dict[str, OutboxEntry] = {}
        self._ids = itertools.count(1)
        self._lock = threading.Lock()

    def enqueue(self, event: EventInput) -> None:
        with self._lock:
            if len(self._pending) + len(self._claimed) >= self.max_pending:
                raise OutboxError(f"the outbox is full ({self.max_pending} events)", code="outbox_full")
            self._pending.append(OutboxEntry(id=str(next(self._ids)), event=event))

    def claim(self, limit: int) -> list[OutboxEntry]:
        with self._lock:
            entries: list[OutboxEntry] = []
            while self._pending and len(entries) < limit:
                entry = self._pending.popleft()
                self._claimed[entry.id] = entry
                entries.append(entry)
            return entries

    def ack(self, ids: Sequence[str]) -> None:
        with self._lock:
            for entry_id in ids:
                self._claimed.pop(entry_id, None)

    def release(self, ids: Sequence[str]) -> None:
        with self._lock:
            entries = [self._claimed.pop(entry_id) for entry_id in ids if entry_id in self._claimed]
            self._pending.extendleft(reversed(entries))


class _Outbox:
    """One client's outbox: the store, the hooks and the worker thread."""

    def __init__(
        self,
        client: Santati,
        store: OutboxStore,
        *,
        batch_size: int,
        flush_interval_ms: int,
        pre_send: PreSendHook | None,
        post_send: PostSendHook | None,
    ) -> None:
        self._client = client
        self._store = store
        self._batch_size = batch_size
        self._interval = flush_interval_ms / 1000
        self._pre_send = pre_send
        self._post_send = post_send
        self._pass_lock = threading.Lock()
        # Guards the lifecycle below; close() waits on it for in-flight logs.
        self._state = threading.Condition()
        self._logging = 0
        self._wake = threading.Event()
        self._thread: threading.Thread | None = None
        self._closed = False

    def log(self, event: EventInput) -> str:
        # A snapshot, so later changes to the caller's objects do not reach the stored event.
        event = copy.deepcopy(event)
        with self._state:
            if self._closed:
                raise OutboxError("the client is closed", code="closed")
            self._logging += 1
        try:
            self._call(self._store.enqueue, event)
        finally:
            with self._state:
                self._logging -= 1
                self._state.notify_all()
                if self._thread is None and not self._closed:
                    self._thread = threading.Thread(target=self._run, name="santati-outbox", daemon=True)
                    self._thread.start()
        return event["idempotency_key"]

    def flush(self) -> None:
        with self._pass_lock:
            self._pass()

    def close(self) -> None:
        with self._state:
            if self._closed:
                return
            self._closed = True
            # Every log() that got past the closed check is in the store
            # before the final flush.
            self._state.wait_for(lambda: self._logging == 0)
            thread = self._thread
        self._wake.set()
        if thread is not None:
            thread.join()
        self.flush()

    def _run(self) -> None:
        while True:
            self._wake.wait(self._interval)
            self._wake.clear()
            if self._closed:
                return
            # A failing store or anything unexpected must not kill the worker:
            # whatever was claimed stays in the store for the next tick.
            with self._pass_lock, contextlib.suppress(Exception):
                self._pass()

    def _pass(self) -> None:
        while True:
            entries = self._call(self._store.claim, self._batch_size)
            if not entries:
                return
            released = False
            to_send: list[tuple[OutboxEntry, EventInput]] = []
            for entry in entries:
                out: EventInput | None = entry.event
                if self._pre_send is not None:
                    try:
                        # A copy, so post_send still sees the stored event.
                        out = self._pre_send(copy.deepcopy(entry.event))
                    except Exception as err:  # noqa: BLE001 - any hook failure is reported as hook_failed
                        self._call(self._store.release, [entry.id])
                        released = True
                        self._notify(
                            entry.event, SendOutcome("failed", error=OutboxError(str(err), code="hook_failed"))
                        )
                        continue
                if out is None:
                    self._call(self._store.ack, [entry.id])
                    continue
                to_send.append((entry, out))
            if to_send:
                released = self._send(to_send) or released
            if released or len(entries) < self._batch_size:
                return

    def _send(self, to_send: list[tuple[OutboxEntry, EventInput]]) -> bool:
        """Send one batch and settle its entries; ``True`` when they were released."""
        ids = [entry.id for entry, _ in to_send]
        try:
            status, result = self._client.events._emit_batch([out for _, out in to_send])
        except SantatiError as err:
            for entry, _ in to_send:
                self._notify(entry.event, SendOutcome("failed", error=err))
            if isinstance(err, _RETRYABLE):
                self._call(self._store.release, ids)
                return True
            self._call(self._store.ack, ids)
            return False
        except Exception as err:  # noqa: BLE001 - e.g. a malformed pre_send result; reported, never raised
            failure = OutboxError(str(err), code="hook_failed")
            for entry, _ in to_send:
                self._notify(entry.event, SendOutcome("failed", error=failure))
            self._call(self._store.ack, ids)
            return False
        for item in result.results:
            if 0 <= item.index < len(to_send):
                self._notify(to_send[item.index][0].event, _outcome(item, status))
        self._call(self._store.ack, ids)
        return False

    def _notify(self, event: EventInput, outcome: SendOutcome) -> None:
        if self._post_send is not None:
            with contextlib.suppress(Exception):
                self._post_send(event, outcome)

    def _call(self, method: Callable[..., _T], *args: Any) -> _T:
        """Call the store, surfacing a foreign failure as ``store_unavailable``."""
        try:
            return method(*args)
        except SantatiError:
            raise
        except Exception as err:
            raise OutboxError(str(err), code="store_unavailable") from err


def _outcome(item: BatchItem, status: int) -> SendOutcome:
    if item.status != "rejected":
        return SendOutcome(item.status, id=item.id)
    error = item.error
    return SendOutcome(
        "rejected",
        error=ValidationError(
            error.message if error else "rejected",
            status=status,
            code=error.code if error else None,
            field=error.field if error else None,
        ),
    )
