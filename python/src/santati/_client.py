"""The Santati client and its ``events`` resource."""

from __future__ import annotations

import importlib.metadata
import json
import re
import urllib.parse
import uuid
from collections.abc import Callable, Iterator, Mapping, Sequence
from types import TracebackType
from typing import Any, TypeVar

import urllib3
from pydantic import BaseModel
from pydantic import ValidationError as PydanticValidationError
from typing_extensions import Self

from santati_core import ApiClient, AuditEventsApi, Configuration
from santati_core.models.audit_event import AuditEvent
from santati_core.models.event_batch_item_result import EventBatchItemResult
from santati_core.models.event_batch_request import EventBatchRequest
from santati_core.models.event_batch_result import EventBatchResult
from santati_core.models.event_envelope_request import EventEnvelopeRequest
from santati_core.models.event_ingest_request import EventIngestRequest
from santati_core.models.paginated_audit_event_list import PaginatedAuditEventList

from ._errors import (
    ApiError,
    AuthError,
    NotFoundError,
    RateLimitedError,
    SantatiError,
    ServerError,
    TransportError,
    ValidationError,
)
from ._retry import RetryPolicy
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

_MODEL = TypeVar("_MODEL", bound=BaseModel)


class Santati:
    """A Santati client.

    Construction is the only place the options are read; :attr:`trail`, the
    default trail for emits, stays live. Use it as a context manager, or call
    :meth:`close` yourself, to release the pooled connections.
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
    ) -> None:
        if not api_key:
            raise ValidationError("api_key must be a non-empty string", field="api_key")
        for name in headers or {}:
            if name.lower() == "authorization":
                raise ValidationError(
                    "headers must not set Authorization: the SDK sets it on every request",
                    field="headers",
                )

        self.api_key = api_key
        self.base_url = base_url.rstrip("/")
        self.trail = trail
        self.headers = dict(headers or {})

        self._timeout_seconds = timeout_ms / 1000
        self._retry = RetryPolicy(max_retries, initial_backoff_ms, max_backoff_ms)
        self._api_client = ApiClient(
            Configuration(
                host=self.base_url,
                retries=urllib3.util.Retry(total=0, redirect=False),
            )
        )
        self._api_client.user_agent = f"santati-python/{__version__}"
        # The generated core carries no security scheme, so it has no auth
        # plumbing of its own: the key goes on as a default header, which wins
        # over any per-call header the generated serializers set.
        _apply_default_headers(self._api_client, {"Authorization": f"Api-Key {api_key}", **self.headers})
        self._api = AuditEventsApi(self._api_client)

        self.events = Events(self)
        """The audit-event operations: ``emit``, ``emit_batch``, ``list``, ``iterate``."""

    def close(self) -> None:
        """Release the pooled HTTP connections."""
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
    ) -> EmitResult:
        """Index one event; a repeated ``idempotency_key`` returns the stored one."""
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
            default_trail=self._client.trail,
            field_prefix="",
        )
        body = _envelope_model(envelope)
        key = envelope["idempotency_key"]

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
                default_trail=self._client.trail,
                field_prefix=f"events[{index}].",
            )
            for index, item in enumerate(events)
        ]
        batch = EventBatchRequest.model_validate({"events": envelopes})

        def attempt() -> BatchResult:
            status, headers, raw = _attempt(
                lambda: self._client._api.events_create_without_preload_content(
                    event_ingest_request=EventIngestRequest(actual_instance=batch),
                    _request_timeout=self._client._timeout_seconds,
                )
            )
            if status in (202, 207):
                result = _decode(EventBatchResult, raw, status)
                return BatchResult(
                    accepted=result.accepted,
                    rejected=result.rejected,
                    results=[_batch_item(item) for item in result.results],
                )
            raise _error_from_response(status, headers, raw)

        return self._client._retry.run(attempt)

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


def _validation_error(err: PydanticValidationError) -> ValidationError:
    location = err.errors()[0]["loc"]
    return ValidationError(str(err), field=".".join(str(part) for part in location))


def _attempt(request: Callable[[], urllib3.HTTPResponse]) -> tuple[int, Any, bytes]:
    """Make one request and read its body, mapping transport failures."""
    try:
        response = request()
        try:
            return response.status, response.headers, response.data
        finally:
            response.release_conn()
    except urllib3.exceptions.HTTPError as err:
        raise TransportError(f"{type(err).__name__}: {err}") from err


def _read(response: urllib3.HTTPResponse) -> tuple[int, Any, bytes]:
    """Read a response that was already made (see :func:`_attempt`)."""
    return _attempt(lambda: response)


def _decode(model: type[_MODEL], body: bytes, status: int) -> _MODEL:
    """Parse a success body with the generated read model."""
    try:
        parsed = json.loads(body)
    except ValueError as err:
        raise ApiError(f"HTTP {status}: response body is not JSON", status=status) from err
    try:
        return model.model_validate(parsed)
    except PydanticValidationError as err:
        raise ApiError(f"HTTP {status}: response body does not match the schema", status=status) from err


def _batch_item(item: EventBatchItemResult) -> BatchItem:
    error = None
    if item.error is not None:
        error = BatchItemError(code=item.error.code, message=item.error.message, field=item.error.var_field)
    return BatchItem(index=item.index, status=item.status, id=item.id, error=error)


def _next_cursor(next_url: str | None) -> str | None:
    """The decoded ``cursor`` of a page's ``next`` URL, or ``None``."""
    if not next_url:
        return None
    values = urllib.parse.parse_qs(urllib.parse.urlsplit(next_url).query).get("cursor")
    return values[0] if values else None


def _error_from_response(status: int, headers: Any, body: bytes) -> SantatiError:
    kind = _error_kind(status)
    code: str | None = None
    field: str | None = None
    message = f"HTTP {status}"
    try:
        parsed = json.loads(body)
    except ValueError:
        parsed = None
    if isinstance(parsed, dict):
        error = parsed.get("error")
        if isinstance(error, dict) and isinstance(error.get("code"), str):
            code = error["code"]
            if isinstance(error.get("field"), str):
                field = error["field"]
            if isinstance(error.get("message"), str):
                message = error["message"]
        elif isinstance(parsed.get("detail"), str):
            message = parsed["detail"]
    return kind(message, status=status, code=code, field=field, retry_after=_retry_after(headers))


def _error_kind(status: int) -> type[SantatiError]:
    if status in (400, 413, 422):
        return ValidationError
    if status in (401, 403):
        return AuthError
    if status == 404:
        return NotFoundError
    if status == 429:
        return RateLimitedError
    if 500 <= status <= 599:
        return ServerError
    return ApiError


def _retry_after(headers: Any) -> int | None:
    value = headers.get("Retry-After")
    if isinstance(value, str) and re.fullmatch(r"\d+", value):
        return int(value)
    return None
