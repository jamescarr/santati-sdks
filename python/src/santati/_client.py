"""The Santati client and its ``events`` resource."""

from __future__ import annotations

import importlib.metadata
import uuid
from collections.abc import Iterator, Mapping, Sequence
from types import TracebackType
from typing import Any, cast

import urllib3
from pydantic import ValidationError as PydanticValidationError
from typing_extensions import Self

from santati_core import ApiClient, AuditEventsApi, Configuration, EventDefinitionsApi
from santati_core.models.audit_event import AuditEvent
from santati_core.models.event_batch_item_result import EventBatchItemResult
from santati_core.models.event_batch_request import EventBatchRequest
from santati_core.models.event_batch_result import EventBatchResult
from santati_core.models.event_envelope_request import EventEnvelopeRequest
from santati_core.models.event_ingest_request import EventIngestRequest
from santati_core.models.paginated_audit_event_list import PaginatedAuditEventList

from ._errors import ValidationError
from ._http import _attempt, _decode, _error_from_response, _next_cursor, _read, _validation_error
from ._outbox import OutboxStore, PostSendHook, PreSendHook, _Outbox
from ._retry import RetryPolicy
from ._schemas import Schemas
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

__version__: str = importlib.metadata.version("santati")
"""This SDK's version, read from the installed distribution metadata."""

DEFAULT_BASE_URL = "https://api.santati.io"
"""Where requests go when no ``base_url`` is given."""


class Santati:
    """A Santati client.

    Construction is the only place the options are read; :attr:`trail`, the
    default trail for emits, stays live. Use it as a context manager, or call
    :meth:`close` yourself, to send what a queued :meth:`Events.emit` left in the
    outbox and release the pooled connections.
    """

    def __init__(
        self,
        api_key: str,
        *,
        base_url: str = DEFAULT_BASE_URL,
        trail: str | None = None,
        timeout_ms: int = 10000,
        max_retries: int = 2,
        initial_backoff_ms: int = 250,
        max_backoff_ms: int = 8000,
        headers: Mapping[str, str] | None = None,
        outbox: OutboxStore | None = None,
        batch_size: int = 100,
        flush_interval_ms: int = 1000,
        pre_send: PreSendHook | None = None,
        post_send: PostSendHook | None = None,
    ) -> None:
        if not api_key:
            raise ValidationError("api_key must be a non-empty string", field="api_key")
        for name in headers or {}:
            if name.lower() == "authorization":
                raise ValidationError(
                    "headers must not set Authorization: the SDK sets it on every request",
                    field="headers",
                )
        if not 1 <= batch_size <= 500:
            raise ValidationError("batch_size must be between 1 and 500", field="batch_size")
        if flush_interval_ms <= 0:
            raise ValidationError("flush_interval_ms must be above zero", field="flush_interval_ms")

        self.api_key = api_key
        self.base_url = base_url.rstrip("/")
        self.trail = trail
        self.headers = dict(headers or {})

        self._timeout_seconds = timeout_ms / 1000
        self._retry = RetryPolicy(max_retries, initial_backoff_ms, max_backoff_ms)
        # The generated core applies the spec's `ApiKeyAuth` scheme itself: it
        # reads the key and the `Api-Key` prefix from the configuration.
        self._api_client = ApiClient(
            Configuration(
                host=self.base_url,
                api_key={"ApiKeyAuth": api_key},
                api_key_prefix={"ApiKeyAuth": "Api-Key"},
                retries=urllib3.util.Retry(total=0, redirect=False),
            )
        )
        self._api_client.user_agent = f"santati-python/{__version__}"
        _apply_default_headers(self._api_client, self.headers)
        self._api = AuditEventsApi(self._api_client)
        self._definitions_api = EventDefinitionsApi(self._api_client)

        self.events = Events(self)
        """The audit-event operations: ``emit``, ``emit_batch``, ``list``, ``iterate``."""

        self.schemas = Schemas(self)
        """The event-definition, schema-version and standard-pack operations."""

        self._outbox: _Outbox | None = (
            None
            if outbox is None
            else _Outbox(
                self,
                outbox,
                batch_size=batch_size,
                flush_interval_ms=flush_interval_ms,
                pre_send=pre_send,
                post_send=post_send,
            )
        )

    def flush(self) -> None:
        """Send what the outbox holds now: one pass, in batches of ``batch_size``.

        Returns at once when the client has no ``outbox``.
        """
        if self._outbox is not None:
            self._outbox.flush()

    def close(self) -> None:
        """Stop the outbox worker (if any), flush the outbox once, and release the pooled connections.

        A queued :meth:`Events.emit` after ``close`` raises :class:`OutboxError`
        (``closed``); without an ``outbox`` only the connections are released
        and ``emit`` keeps working.
        """
        if self._outbox is not None:
            self._outbox.close()
        self._api_client.rest_client.pool_manager.clear()

    def __enter__(self) -> Self:
        return self

    def __exit__(
        self,
        exc_type: type[BaseException] | None,
        exc_value: BaseException | None,
        traceback: TracebackType | None,
    ) -> None:
        self.close()


class Events:
    """Audit-event operations, reached through ``client.events``."""

    def __init__(self, client: Santati) -> None:
        self._client = client

    def emit(
        self,
        event: str,
        *,
        trail: str | None = None,
        organization_id: str | None = None,
        actor: ActorInput | None = None,
        targets: Sequence[TargetInput] | None = None,
        metadata: Mapping[str, str] | None = None,
        data: Any = None,
        context: Mapping[str, Any] | None = None,
        created_at: str | None = None,
        idempotency_key: str | None = None,
        schema_version: int | None = None,
    ) -> EmitResult:
        """Index one event, or with an ``outbox`` store it for the background worker and return at once.

        A repeated ``idempotency_key`` returns the stored event. ``schema_version``
        pins the emit to one published schema version of the action (an integer,
        forwarded unchanged and never validated here; the server answers an
        unusable pin with :class:`SchemaValidationError`). A queued emit never
        makes a request: the result has ``event=None`` and ``queued=True``.
        Raises :class:`OutboxError` when the store refuses the event or the
        client is closed.
        """
        envelope = _build_envelope(
            event=event,
            trail=trail,
            organization_id=organization_id,
            actor=actor,
            targets=targets,
            metadata=metadata,
            data=data,
            context=context,
            created_at=created_at,
            idempotency_key=idempotency_key,
            schema_version=schema_version,
            default_trail=self._client.trail,
            field_prefix="",
        )
        body = _envelope_model(envelope)
        key = envelope["idempotency_key"]
        outbox = self._client._outbox
        if outbox is not None:
            outbox.enqueue(cast(EventInput, envelope))
            return EmitResult(event=None, duplicate=False, idempotency_key=key, queued=True)

        def attempt() -> EmitResult:
            status, headers, raw = _attempt(
                lambda: self._client._api.events_create_without_preload_content(
                    event_ingest_request=EventIngestRequest(actual_instance=body),
                    _request_timeout=self._client._timeout_seconds,
                )
            )
            if status in (200, 201):
                return EmitResult(
                    event=_decode(AuditEvent, raw, status),
                    duplicate=status == 200,
                    idempotency_key=key,
                )
            raise _error_from_response(status, headers, raw)

        return self._client._retry.run(attempt)

    def emit_batch(self, events: Sequence[EventInput]) -> BatchResult:
        """Index up to 500 events in one request, one generated key per item."""
        return self._emit_batch(events)[1]

    def _emit_batch(self, events: Sequence[EventInput], *, retries: bool = True) -> tuple[int, BatchResult]:
        """:meth:`emit_batch`, plus the response status (202 or 207) the outbox reports; ``retries=False`` sends once."""
        if not events:
            raise ValidationError("events must not be empty", field="events")
        envelopes = [
            _build_envelope(
                event=item.get("event"),
                trail=item.get("trail"),
                organization_id=item.get("organization_id"),
                actor=item.get("actor"),
                targets=item.get("targets"),
                metadata=item.get("metadata"),
                data=item.get("data"),
                context=item.get("context"),
                created_at=item.get("created_at"),
                idempotency_key=item.get("idempotency_key"),
                schema_version=item.get("schema_version"),
                default_trail=self._client.trail,
                field_prefix=f"events[{index}].",
            )
            for index, item in enumerate(events)
        ]
        try:
            batch = EventBatchRequest.model_validate({"events": envelopes})
        except PydanticValidationError as err:
            raise _validation_error(err) from err

        def attempt() -> tuple[int, BatchResult]:
            status, headers, raw = _attempt(
                lambda: self._client._api.events_create_without_preload_content(
                    event_ingest_request=EventIngestRequest(actual_instance=batch),
                    _request_timeout=self._client._timeout_seconds,
                )
            )
            if status in (202, 207):
                result = _decode(EventBatchResult, raw, status)
                return status, BatchResult(
                    accepted=result.accepted,
                    rejected=result.rejected,
                    results=[_batch_item(item) for item in result.results],
                )
            raise _error_from_response(status, headers, raw)

        return self._client._retry.run(attempt) if retries else attempt()

    def list(
        self,
        *,
        trail: str | None = None,
        event: str | None = None,
        event_prefix: str | None = None,
        organization_id: str | None = None,
        actor_id: str | None = None,
        actor_type: str | None = None,
        target_type: str | None = None,
        target_id: str | None = None,
        created_after: str | None = None,
        created_before: str | None = None,
        q: str | None = None,
        sort: str | None = None,
        limit: int | None = None,
        cursor: str | None = None,
    ) -> EventPage:
        """Read one page of events; the client's default trail does not apply."""
        params: dict[str, Any] = {}
        for name, value in (
            ("actor_id", actor_id),
            ("actor_type", actor_type),
            ("created_after", created_after),
            ("created_before", created_before),
            ("cursor", cursor),
            ("event", event),
            ("event_prefix", event_prefix),
            ("limit", limit),
            ("organization_id", organization_id),
            ("q", q),
            ("sort", sort),
            ("target_id", target_id),
            ("target_type", target_type),
            ("trail", trail),
        ):
            if value is not None:
                params[name] = value

        def attempt() -> EventPage:
            try:
                response = self._client._api.events_list_without_preload_content(
                    **params, _request_timeout=self._client._timeout_seconds
                )
            except PydanticValidationError as err:
                raise _validation_error(err) from err
            status, headers, raw = _read(response)
            if status != 200:
                raise _error_from_response(status, headers, raw)
            page = _decode(PaginatedAuditEventList, raw, status)
            return EventPage(results=page.results, next_cursor=_next_cursor(page.next))

        return self._client._retry.run(attempt)

    def iterate(
        self,
        *,
        trail: str | None = None,
        event: str | None = None,
        event_prefix: str | None = None,
        organization_id: str | None = None,
        actor_id: str | None = None,
        actor_type: str | None = None,
        target_type: str | None = None,
        target_id: str | None = None,
        created_after: str | None = None,
        created_before: str | None = None,
        q: str | None = None,
        sort: str | None = None,
        limit: int | None = None,
    ) -> Iterator[AuditEvent]:
        """Lazily yield every matching event, page by page."""
        cursor: str | None = None
        while True:
            page = self.list(
                trail=trail,
                event=event,
                event_prefix=event_prefix,
                organization_id=organization_id,
                actor_id=actor_id,
                actor_type=actor_type,
                target_type=target_type,
                target_id=target_id,
                created_after=created_after,
                created_before=created_before,
                q=q,
                sort=sort,
                limit=limit,
                cursor=cursor,
            )
            yield from page.results
            if page.next_cursor is None:
                return
            cursor = page.next_cursor


def _apply_default_headers(client: ApiClient, headers: Mapping[str, str]) -> None:
    """Set headers every request carries; defaults beat the generated per-call ones."""
    for name, value in headers.items():
        client.set_default_header(name, value)  # type: ignore[no-untyped-call]  # generated


def _build_envelope(
    *,
    event: str | None,
    trail: str | None,
    organization_id: str | None,
    actor: ActorInput | None,
    targets: Sequence[TargetInput] | None,
    metadata: Mapping[str, str] | None,
    data: Any,
    context: Mapping[str, Any] | None,
    created_at: str | None,
    idempotency_key: str | None,
    schema_version: int | None,
    default_trail: str | None,
    field_prefix: str,
) -> dict[str, Any]:
    """The wire envelope: resolved trail, a key, and only the supplied members."""
    if not event:
        raise ValidationError("event must be a non-empty string", field=f"{field_prefix}event")
    resolved_trail = trail or default_trail
    if not resolved_trail:
        raise ValidationError(
            "trail must be resolved: supply one on the event or set a client default",
            field=f"{field_prefix}trail",
        )
    envelope: dict[str, Any] = {
        "event": event,
        "trail": resolved_trail,
        "idempotency_key": idempotency_key or str(uuid.uuid4()),
    }
    for name, value in (
        ("organization_id", organization_id),
        ("actor", actor),
        ("targets", list(targets) if targets is not None else None),
        ("metadata", metadata),
        ("data", data),
        ("context", context),
        ("created_at", created_at),
        ("schema_version", schema_version),
    ):
        if value is not None:
            envelope[name] = value
    return envelope


def _envelope_model(envelope: Mapping[str, Any]) -> EventEnvelopeRequest:
    """Validate the envelope with the generated request model."""
    try:
        return EventEnvelopeRequest.model_validate(dict(envelope))
    except PydanticValidationError as err:
        raise _validation_error(err) from err


def _batch_item(item: EventBatchItemResult) -> BatchItem:
    error = None
    if item.error is not None:
        error = BatchItemError(code=item.error.code, message=item.error.message, field=item.error.var_field)
    return BatchItem(index=item.index, status=item.status, id=item.id, error=error)
