"""The ``schemas`` resource: event definitions, their schema versions and the standard packs."""

from __future__ import annotations

from collections.abc import Callable, Iterator, Mapping, Sequence
from typing import TYPE_CHECKING, Any, TypeVar

import urllib3
from pydantic import BaseModel
from pydantic import ValidationError as PydanticValidationError

from santati_core.models.event_definition import EventDefinition
from santati_core.models.event_definition_write_request import EventDefinitionWriteRequest
from santati_core.models.event_schema_document_request import EventSchemaDocumentRequest
from santati_core.models.event_schema_version import EventSchemaVersion
from santati_core.models.paginated_event_definition_list import PaginatedEventDefinitionList
from santati_core.models.paginated_event_schema_version_list import PaginatedEventSchemaVersionList
from santati_core.models.patched_event_definition_write_request import PatchedEventDefinitionWriteRequest
from santati_core.models.schema_check import SchemaCheck
from santati_core.models.standard_event_catalog import StandardEventCatalog
from santati_core.models.standard_pack_install_request import StandardPackInstallRequest
from santati_core.models.standard_pack_install_result import StandardPackInstallResult

from ._errors import ValidationError
from ._http import _attempt, _decode, _error_from_response, _next_cursor, _validation_error
from ._types import DefinitionPage, SchemaVersionPage, SchemaVersionResult

if TYPE_CHECKING:
    from ._client import Santati

_T = TypeVar("_T")
_MODEL = TypeVar("_MODEL", bound=BaseModel)


class Schemas:
    """Event definitions, schema versions and standard packs, reached through ``client.schemas``.

    Every operation but :meth:`create_version` retries like the events
    operations; ``create_version`` is sent once, because a repeat would create a
    second draft. An empty ``action`` raises :class:`ValidationError` before any
    request; everything else is forwarded as given and the server decides.
    """

    def __init__(self, client: Santati) -> None:
        self._client = client

    # -- definitions ------------------------------------------------------

    def list_definitions(self, *, limit: int | None = None, cursor: str | None = None) -> DefinitionPage:
        """Read one page of the team's event definitions."""
        params = _present(("cursor", cursor), ("limit", limit))
        return self._send(
            lambda: self._client._definitions_api.event_definitions_list_without_preload_content(
                **params, _request_timeout=self._client._timeout_seconds
            ),
            200,
            lambda _headers, raw, status: _definition_page(_decode(PaginatedEventDefinitionList, raw, status)),
        )

    def iterate_definitions(self, *, limit: int | None = None) -> Iterator[EventDefinition]:
        """Lazily yield every event definition, page by page."""
        cursor: str | None = None
        while True:
            page = self.list_definitions(limit=limit, cursor=cursor)
            yield from page.results
            if page.next_cursor is None:
                return
            cursor = page.next_cursor

    def get_definition(self, action: str) -> EventDefinition:
        """Read one event definition."""
        _require_action(action)
        return self._send(
            lambda: self._client._definitions_api.event_definitions_retrieve_without_preload_content(
                action, _request_timeout=self._client._timeout_seconds
            ),
            200,
            _body(EventDefinition),
        )

    def create_definition(
        self,
        action: str,
        *,
        description: str | None = None,
        allowed_target_types: Sequence[str] | None = None,
        is_active: bool | None = None,
    ) -> EventDefinition:
        """Define an action; only the members you supply are sent."""
        _require_action(action)
        body = _model(
            EventDefinitionWriteRequest,
            {
                "action": action,
                **_present(
                    ("description", description),
                    ("allowed_target_types", list(allowed_target_types) if allowed_target_types is not None else None),
                    ("is_active", is_active),
                ),
            },
        )
        return self._send(
            lambda: self._client._definitions_api.event_definitions_create_without_preload_content(
                event_definition_write_request=body, _request_timeout=self._client._timeout_seconds
            ),
            201,
            _body(EventDefinition),
        )

    def update_definition(
        self,
        action: str,
        *,
        new_action: str | None = None,
        description: str | None = None,
        allowed_target_types: Sequence[str] | None = None,
        is_active: bool | None = None,
    ) -> EventDefinition:
        """Change a definition; only the members you supply are sent, ``new_action`` renames it."""
        _require_action(action)
        body = _model(
            PatchedEventDefinitionWriteRequest,
            _present(
                ("action", new_action),
                ("description", description),
                ("allowed_target_types", list(allowed_target_types) if allowed_target_types is not None else None),
                ("is_active", is_active),
            ),
        )
        return self._send(
            lambda: self._client._definitions_api.event_definitions_update_without_preload_content(
                action, patched_event_definition_write_request=body, _request_timeout=self._client._timeout_seconds
            ),
            200,
            _body(EventDefinition),
        )

    def delete_definition(self, action: str) -> None:
        """Delete an event definition."""
        _require_action(action)
        self._send(
            lambda: self._client._definitions_api.event_definitions_destroy_without_preload_content(
                action, _request_timeout=self._client._timeout_seconds
            ),
            204,
            _nothing,
        )

    # -- schema versions --------------------------------------------------

    def list_versions(self, action: str, *, limit: int | None = None, cursor: str | None = None) -> SchemaVersionPage:
        """Read one page of an action's schema versions, newest first."""
        _require_action(action)
        params = _present(("cursor", cursor), ("limit", limit))
        return self._send(
            lambda: self._client._definitions_api.schema_versions_list_without_preload_content(
                action, **params, _request_timeout=self._client._timeout_seconds
            ),
            200,
            lambda _headers, raw, status: _version_page(_decode(PaginatedEventSchemaVersionList, raw, status)),
        )

    def iterate_versions(self, action: str, *, limit: int | None = None) -> Iterator[EventSchemaVersion]:
        """Lazily yield every schema version of an action, page by page."""
        cursor: str | None = None
        while True:
            page = self.list_versions(action, limit=limit, cursor=cursor)
            yield from page.results
            if page.next_cursor is None:
                return
            cursor = page.next_cursor

    def get_version(self, action: str, version: int) -> SchemaVersionResult:
        """Read one schema version with its ``ETag``."""
        _require_action(action)
        return self._send(
            lambda: self._client._definitions_api.schema_versions_retrieve_without_preload_content(
                action, version, _request_timeout=self._client._timeout_seconds
            ),
            200,
            _versioned,
        )

    def create_version(self, action: str, schema: Mapping[str, Any]) -> SchemaVersionResult:
        """Create a draft from a JSON Schema document. Sent once: it is never retried."""
        _require_action(action)
        body = _document(schema)
        return self._send(
            lambda: self._client._definitions_api.schema_versions_create_without_preload_content(
                action, event_schema_document_request=body, _request_timeout=self._client._timeout_seconds
            ),
            201,
            _versioned,
            retries=False,
        )

    def update_version(
        self, action: str, version: int, schema: Mapping[str, Any], *, if_match: str | None = None
    ) -> SchemaVersionResult:
        """Replace a draft's document; ``if_match`` (an ``ETag`` you read) makes a concurrent edit answer ``ApiError`` 412."""
        _require_action(action)
        body = _document(schema)
        return self._send(
            lambda: self._client._definitions_api.schema_versions_update_without_preload_content(
                action,
                version,
                event_schema_document_request=body,
                if_match=if_match,
                _request_timeout=self._client._timeout_seconds,
            ),
            200,
            _versioned,
        )

    def delete_version(self, action: str, version: int) -> None:
        """Delete a draft; a published version answers ``ApiError`` 409."""
        _require_action(action)
        self._send(
            lambda: self._client._definitions_api.schema_versions_destroy_without_preload_content(
                action, version, _request_timeout=self._client._timeout_seconds
            ),
            204,
            _nothing,
        )

    def publish_version(self, action: str, version: int) -> SchemaVersionResult:
        """Publish a draft: it becomes immutable and validates ingest."""
        _require_action(action)
        return self._send(
            lambda: self._client._definitions_api.schema_versions_publish_without_preload_content(
                action, version, _request_timeout=self._client._timeout_seconds
            ),
            200,
            _versioned,
        )

    def deprecate_version(self, action: str, version: int) -> SchemaVersionResult:
        """Start the migration window of a superseded version."""
        _require_action(action)
        return self._send(
            lambda: self._client._definitions_api.schema_versions_deprecate_without_preload_content(
                action, version, _request_timeout=self._client._timeout_seconds
            ),
            200,
            _versioned,
        )

    def check_schema(self, action: str, schema: Mapping[str, Any]) -> SchemaCheck:
        """Dry-run a document against the action's newest stored events; nothing is stored."""
        _require_action(action)
        body = _document(schema)
        return self._send(
            lambda: self._client._definitions_api.schema_versions_check_without_preload_content(
                action, event_schema_document_request=body, _request_timeout=self._client._timeout_seconds
            ),
            200,
            _body(SchemaCheck),
        )

    # -- standard packs ---------------------------------------------------

    def list_standard_packs(self) -> StandardEventCatalog:
        """Read the standard catalog: every pack and its actions."""
        return self._send(
            lambda: self._client._definitions_api.standard_events_list_without_preload_content(
                _request_timeout=self._client._timeout_seconds
            ),
            200,
            _body(StandardEventCatalog),
        )

    def install_standard_packs(self, packs: Sequence[str]) -> StandardPackInstallResult:
        """Install packs by slug; the slugs are forwarded unchanged and the server judges them."""
        body = _model(StandardPackInstallRequest, {"packs": list(packs)})
        return self._send(
            lambda: self._client._definitions_api.standard_events_install_without_preload_content(
                standard_pack_install_request=body, _request_timeout=self._client._timeout_seconds
            ),
            200,
            _body(StandardPackInstallResult),
        )

    # -- plumbing ---------------------------------------------------------

    def _send(
        self,
        request: Callable[[], urllib3.HTTPResponse],
        expected: int,
        result: Callable[[Any, bytes, int], _T],
        *,
        retries: bool = True,
    ) -> _T:
        """One operation: attempt, require the ``expected`` status, build the result; retried unless ``retries=False``."""

        def attempt() -> _T:
            try:
                status, headers, raw = _attempt(request)
            except PydanticValidationError as err:
                raise _validation_error(err) from err
            if status != expected:
                raise _error_from_response(status, headers, raw)
            return result(headers, raw, status)

        return self._client._retry.run(attempt) if retries else attempt()


def _require_action(action: str) -> None:
    if not action:
        raise ValidationError("action must be a non-empty string", field="action")


def _present(*members: tuple[str, Any]) -> dict[str, Any]:
    """The members whose value is not ``None``, in the order given."""
    return {name: value for name, value in members if value is not None}


def _model(model: type[_MODEL], members: Mapping[str, Any]) -> _MODEL:
    """Validate a request body with the generated request model."""
    try:
        return model.model_validate(dict(members))
    except PydanticValidationError as err:
        raise _validation_error(err) from err


def _document(schema: Mapping[str, Any]) -> EventSchemaDocumentRequest:
    return _model(EventSchemaDocumentRequest, {"schema": schema})


def _body(model: type[_MODEL]) -> Callable[[Any, bytes, int], _MODEL]:
    return lambda _headers, raw, status: _decode(model, raw, status)


def _versioned(headers: Any, raw: bytes, status: int) -> SchemaVersionResult:
    return SchemaVersionResult(schema_version=_decode(EventSchemaVersion, raw, status), etag=headers.get("ETag"))


def _nothing(_headers: Any, _raw: bytes, _status: int) -> None:
    return None


def _definition_page(page: PaginatedEventDefinitionList) -> DefinitionPage:
    return DefinitionPage(results=page.results, next_cursor=_next_cursor(page.next))


def _version_page(page: PaginatedEventSchemaVersionList) -> SchemaVersionPage:
    return SchemaVersionPage(results=page.results, next_cursor=_next_cursor(page.next))
