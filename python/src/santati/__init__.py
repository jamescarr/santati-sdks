"""Official Python SDK for the Santati audit-log API.

    import santati

    with santati.Santati("sat_sk_...", trail="billing") as client:
        result = client.events.emit("invoice.voided", organization_id="org_acme")
        print(result.event.id, result.duplicate)

    # With an outbox, emit returns at once and a background worker sends the event.
    with santati.Santati("sat_sk_...", trail="billing", outbox=santati.MemoryOutbox()) as client:
        client.events.emit("invoice.paid", organization_id="org_acme")

See ``docs/sdk-surface.md`` in the repository for the surface every Santati SDK
implements.
"""

from __future__ import annotations

from santati_core.models.audit_event import AuditEvent
from santati_core.models.event_actor import EventActor
from santati_core.models.event_target import EventTarget

from ._client import DEFAULT_BASE_URL, Events, Santati
from ._client import __version__ as __version__
from ._errors import (
    ApiError,
    AuthError,
    NotFoundError,
    OutboxError,
    RateLimitedError,
    SantatiError,
    ServerError,
    TransportError,
    ValidationError,
)
from ._outbox import MemoryOutbox, OutboxEntry, OutboxStore, PostSendHook, PreSendHook, SendOutcome
from ._types import (
    ActorInput,
    BatchItem,
    BatchItemError,
    BatchResult,
    EmitResult,
    EventInput,
    EventPage,
    TargetInput,
)

__all__ = [
    "DEFAULT_BASE_URL",
    "ActorInput",
    "ApiError",
    "AuditEvent",
    "AuthError",
    "BatchItem",
    "BatchItemError",
    "BatchResult",
    "EmitResult",
    "EventActor",
    "EventInput",
    "EventPage",
    "EventTarget",
    "Events",
    "MemoryOutbox",
    "NotFoundError",
    "OutboxEntry",
    "OutboxError",
    "OutboxStore",
    "PostSendHook",
    "PreSendHook",
    "RateLimitedError",
    "Santati",
    "SantatiError",
    "SendOutcome",
    "ServerError",
    "TargetInput",
    "TransportError",
    "ValidationError",
    "__version__",
]
