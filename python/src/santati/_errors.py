"""Errors raised by the Santati SDK.

Every failure is a :class:`SantatiError`; the nine subclasses are the kinds
described in ``docs/sdk-surface.md``. ``status`` is ``None`` for local
validation failures, transport errors and outbox errors.
"""

from __future__ import annotations


class SantatiError(Exception):
    """Base class for every error this SDK raises."""

    def __init__(
        self,
        message: str,
        *,
        status: int | None = None,
        code: str | None = None,
        field: str | None = None,
        retry_after: int | None = None,
    ) -> None:
        super().__init__(message)
        self.message = message
        """Human-readable explanation of the failure."""
        self.status = status
        """HTTP status of the failing response, or ``None`` when there was none."""
        self.code = code
        """The server's machine-readable error code, when it sent one."""
        self.field = field
        """The request field at fault, when the server named one."""
        self.retry_after = retry_after
        """Seconds from the response's ``Retry-After`` header, or ``None``."""


class ValidationError(SantatiError):
    """The request was rejected locally or by the server (400, 413, 422)."""


class SchemaValidationError(ValidationError):
    """The server rejected the event against the action's JSON Schema, a disallowed target type, or an unusable ``schema_version`` pin (``schema_validation_failed``)."""


class AuthError(SantatiError):
    """The API key is missing, invalid or not scoped to the request (401, 403)."""


class NotFoundError(SantatiError):
    """The addressed resource does not exist (404)."""


class RateLimitedError(SantatiError):
    """The team is over its rate limit (429)."""


class ServerError(SantatiError):
    """Santati failed to serve the request (500-599)."""


class TransportError(SantatiError):
    """No HTTP response arrived: refused, DNS, TLS or timeout."""


class ApiError(SantatiError):
    """Any other failure: an unexpected status or an undecodable success body."""


class OutboxError(SantatiError):
    """The outbox store refused or failed, or a ``pre_send`` hook raised (status ``None``).

    ``code`` is ``outbox_full``, ``store_unavailable``, ``closed`` or ``hook_failed``.
    """


def validation_kind(code: str | None) -> type[ValidationError]:
    """The kind of a 400, 413 or 422 whose body carried ``code``."""
    return SchemaValidationError if code == "schema_validation_failed" else ValidationError
