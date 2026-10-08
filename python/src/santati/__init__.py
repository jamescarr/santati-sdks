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
from santati_core.models.event_definition import EventDefinition
from santati_core.models.event_schema_version import EventSchemaVersion
from santati_core.models.event_target import EventTarget
from santati_core.models.ocsf_mapping import OcsfMapping
from santati_core.models.schema_check import SchemaCheck
from santati_core.models.schema_check_failure import SchemaCheckFailure
from santati_core.models.standard_event import StandardEvent
from santati_core.models.standard_event_catalog import StandardEventCatalog
from santati_core.models.standard_pack import StandardPack
from santati_core.models.standard_pack_install_result import StandardPackInstallResult

from ._client import DEFAULT_BASE_URL, Events, Santati
from ._client import __version__ as __version__
from ._errors import (
    ApiError,
    AuthError,
    NotFoundError,
    OutboxError,
    RateLimitedError,
    SantatiError,
    SchemaValidationError,
    ServerError,
    TransportError,
    ValidationError,
)
from ._outbox import MemoryOutbox, OutboxEntry, OutboxStore, PostSendHook, PreSendHook, SendOutcome
from ._schemas import Schemas
from ._types import (
    ActorInput,
    BatchItem,
    BatchItemError,
    BatchResult,
    DefinitionPage,
    EmitResult,
    EventInput,
    EventPage,
    SchemaVersionPage,
    SchemaVersionResult,
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
    "DefinitionPage",
    "EmitResult",
    "EventActor",
    "EventDefinition",
    "EventInput",
    "EventPage",
    "EventSchemaVersion",
    "EventTarget",
    "Events",
    "MemoryOutbox",
    "NotFoundError",
    "OcsfMapping",
    "OutboxEntry",
    "OutboxError",
    "OutboxStore",
    "PostSendHook",
    "PreSendHook",
    "RateLimitedError",
    "Santati",
    "SantatiError",
    "SchemaCheck",
    "SchemaCheckFailure",
    "SchemaValidationError",
    "SchemaVersionPage",
    "SchemaVersionResult",
    "Schemas",
    "SendOutcome",
    "ServerError",
    "StandardEvent",
    "StandardEventCatalog",
    "StandardPack",
    "StandardPackInstallResult",
    "TargetInput",
    "TransportError",
    "ValidationError",
    "__version__",
]
