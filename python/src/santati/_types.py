"""Inputs and results of the event and schema operations.

``AuditEvent``, ``EventActor``, ``EventTarget`` and the schema read models
(``EventDefinition``, ``EventSchemaVersion``, …) are the generated models,
re-exported from the package root; the types here are the facade's own
(hand-written) shapes.
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import Any

from typing_extensions import NotRequired, TypedDict

from santati_core.models.audit_event import AuditEvent
from santati_core.models.event_definition import EventDefinition
from santati_core.models.event_schema_version import EventSchemaVersion


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
    schema_version: NotRequired[int]


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


@dataclass(frozen=True)
class DefinitionPage:
    """One page of event definitions and the cursor that reads the next one."""

    results: list[EventDefinition]
    next_cursor: str | None


@dataclass(frozen=True)
class SchemaVersionPage:
    """One page of an action's schema versions and the cursor that reads the next one."""

    results: list[EventSchemaVersion]
    next_cursor: str | None


@dataclass(frozen=True)
class SchemaVersionResult:
    """A schema version and the ``ETag`` header the response carried (``None`` when absent), to send back as ``if_match``."""

    schema_version: EventSchemaVersion
    etag: str | None
