"""Inputs and results of the event operations.

``AuditEvent``, ``EventActor`` and ``EventTarget`` are the generated read
models, re-exported from the package root; the types here are the facade's own
(hand-written) shapes.
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import Any

from typing_extensions import NotRequired, TypedDict

from santati_core.models.audit_event import AuditEvent


class ActorInput(TypedDict):
    """Who acted: ``type`` plus, unless anonymous, an ``id``."""

    type: str
    id: NotRequired[str]
    name: NotRequired[str]
    metadata: NotRequired[dict[str, str]]


class TargetInput(TypedDict):
    """One object an event acted on."""

    type: str
    id: str
    name: NotRequired[str]
    metadata: NotRequired[dict[str, str]]


class EventInput(TypedDict):
    """One emit, as accepted by :meth:`santati.Events.emit_batch`."""

    event: str
    trail: NotRequired[str]
    organization_id: NotRequired[str]
    actor: NotRequired[ActorInput]
    targets: NotRequired[list[TargetInput]]
    metadata: NotRequired[dict[str, str]]
    data: NotRequired[Any]
    context: NotRequired[dict[str, Any]]
    created_at: NotRequired[str]
    idempotency_key: NotRequired[str]


@dataclass(frozen=True)
class EmitResult:
    """The stored event (None when queued), whether it was a replay, the key that was used, and whether it went to the outbox instead of the API."""

    event: AuditEvent | None
    duplicate: bool
    idempotency_key: str
    queued: bool = False


@dataclass(frozen=True)
class BatchItemError:
    """Why one batch item was rejected."""

    code: str
    message: str
    field: str | None = None


@dataclass(frozen=True)
class BatchItem:
    """One batch result, in submission order."""

    index: int
    status: str
    id: str | None = None
    error: BatchItemError | None = None


@dataclass(frozen=True)
class BatchResult:
    """What a batch did: counts plus one item per submitted envelope."""

    accepted: int
    rejected: int
    results: list[BatchItem]


@dataclass(frozen=True)
class EventPage:
    """One page of events and the cursor that reads the next one."""

    results: list[AuditEvent]
    next_cursor: str | None
