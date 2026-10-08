"""Response handling shared by the ``events`` and ``schemas`` resources.

One attempt's transport mapping, the success-body decoder, the cursor reader and
the error mapping of ``docs/sdk-surface.md``. The retry loop is
:class:`santati._retry.RetryPolicy`.
"""

from __future__ import annotations

import json
import re
import urllib.parse
from collections.abc import Callable
from typing import Any, TypeVar

import urllib3
from pydantic import BaseModel
from pydantic import ValidationError as PydanticValidationError

from ._errors import (
    ApiError,
    AuthError,
    NotFoundError,
    RateLimitedError,
    SantatiError,
    ServerError,
    TransportError,
    ValidationError,
    validation_kind,
)

_MODEL = TypeVar("_MODEL", bound=BaseModel)


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
    if kind is ValidationError:
        kind = validation_kind(code)
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
