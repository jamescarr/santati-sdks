"""Django: audit events from the authentication signals.

:func:`instrument_auth` connects receivers to Django's ``user_logged_in``,
``user_logged_out`` and ``user_login_failed`` signals, so every login and
logout becomes an audit event without any change to the views::

    from santati.integrations.django import instrument_auth

    class BillingConfig(AppConfig):
        def ready(self) -> None:
            instrument_auth(client, organization_id="org_acme")

Nothing here ever raises into Django: Django turns a receiver's exception into
an HTTP 500, and a failed audit event must never fail a login. A repeated call
replaces the previous instrumentation — the last one wins — so ``ready()``
running twice cannot double-emit.
"""

from __future__ import annotations

import threading
from collections.abc import Callable, Mapping
from typing import TYPE_CHECKING, Any

from django.contrib.auth.signals import user_logged_in, user_logged_out, user_login_failed

from .._errors import ValidationError
from ._core import (
    USER_LOGGED_IN,
    USER_LOGGED_OUT,
    USER_LOGIN_FAILED,
    Dispatch,
    base_event,
    deliver,
    logger,
    metadata,
    resolve_dispatch,
)

if TYPE_CHECKING:
    from django.contrib.auth.base_user import AbstractBaseUser
    from django.http import HttpRequest

    from .._client import Santati
    from .._types import ActorInput, EventInput

__all__ = ["AuthInstrumentation", "instrument_auth"]

_LOGGED_IN_UID = "santati.integrations.django.user_logged_in"
_LOGGED_OUT_UID = "santati.integrations.django.user_logged_out"
_LOGIN_FAILED_UID = "santati.integrations.django.user_login_failed"

_active: AuthInstrumentation | None = None
_lock = threading.RLock()


class AuthInstrumentation:
    """The auth-signal receivers one instrumentation installed.

    Constructing it changes nothing; :meth:`connect` connects the receivers
    (replacing any instrumentation already installed) and :meth:`disconnect`
    unhooks them.
    """

    def __init__(
        self,
        client: Santati | None = None,
        *,
        dispatch: Dispatch | None = None,
        organization_id: str | Callable[[HttpRequest | None, AbstractBaseUser | None], str | None],
        trail: str | None = None,
        actor: Callable[[HttpRequest, AbstractBaseUser], ActorInput] | None = None,
        context: Callable[[HttpRequest], dict[str, Any]] | None = None,
        login_event: str | None = USER_LOGGED_IN,
        logout_event: str | None = USER_LOGGED_OUT,
        login_failed_event: str | None = USER_LOGIN_FAILED,
    ) -> None:
        if isinstance(organization_id, str) and not organization_id:
            raise ValidationError("organization_id must be a non-empty string", field="organization_id")
        self._dispatch = resolve_dispatch(client, dispatch, trail)
        self._organization_id = organization_id
        self._trail = trail
        self._actor = actor
        self._context = context
        # A signal whose event name is None is not connected at all; the names
        # the receivers log are therefore always set.
        self._login_event = login_event or USER_LOGGED_IN
        self._logout_event = logout_event or USER_LOGGED_OUT
        self._login_failed_event = login_failed_event or USER_LOGIN_FAILED
        self._login_enabled = login_event is not None
        self._logout_enabled = logout_event is not None
        self._login_failed_enabled = login_failed_event is not None

    def connect(self) -> None:
        """Connect the enabled receivers, replacing whatever instrumentation was installed."""
        global _active
        with _lock:
            if _active is not None:
                _active.disconnect()
            if self._login_enabled:
                user_logged_in.connect(self._on_logged_in, weak=False, dispatch_uid=_LOGGED_IN_UID)
            if self._logout_enabled:
                user_logged_out.connect(self._on_logged_out, weak=False, dispatch_uid=_LOGGED_OUT_UID)
            if self._login_failed_enabled:
                user_login_failed.connect(self._on_login_failed, weak=False, dispatch_uid=_LOGIN_FAILED_UID)
            _active = self

    def disconnect(self) -> None:
        """Unhook the receivers; does nothing once a later instrumentation replaced this one."""
        global _active
        with _lock:
            if _active is not self:
                return
            if self._login_enabled:
                user_logged_in.disconnect(dispatch_uid=_LOGGED_IN_UID)
            if self._logout_enabled:
                user_logged_out.disconnect(dispatch_uid=_LOGGED_OUT_UID)
            if self._login_failed_enabled:
                user_login_failed.disconnect(dispatch_uid=_LOGIN_FAILED_UID)
            _active = None

    def _on_logged_in(self, sender: object, **kwargs: Any) -> None:
        user = kwargs.get("user")
        if user is None:
            return
        name = self._login_event
        deliver(self._dispatch, name, lambda: self._build_user_event(name, kwargs.get("request"), user))

    def _on_logged_out(self, sender: object, **kwargs: Any) -> None:
        # An anonymous logout carries no user, so there is nobody to name.
        user = kwargs.get("user")
        if user is None:
            return
        name = self._logout_event
        deliver(self._dispatch, name, lambda: self._build_user_event(name, kwargs.get("request"), user))

    def _on_login_failed(self, sender: object, **kwargs: Any) -> None:
        name = self._login_failed_event
        deliver(
            self._dispatch,
            name,
            lambda: self._build_login_failed_event(kwargs.get("request"), kwargs.get("credentials")),
        )

    def _build_user_event(self, name: str, request: HttpRequest | None, user: AbstractBaseUser) -> EventInput | None:
        organization_id = self._resolve_organization(request, user)
        if not organization_id:
            logger.debug("santati: skipped audit event %r: no organization_id", name)
            return None
        event = base_event(name, organization_id=organization_id, trail=self._trail)
        if self._actor is not None and request is not None:
            event["actor"] = self._actor(request, user)
        else:
            event["actor"] = _user_actor(user)
        event["metadata"] = metadata(username=user.get_username())
        resolved_context = self._resolve_context(request)
        if resolved_context:
            event["context"] = resolved_context
        return event

    def _build_login_failed_event(self, request: HttpRequest | None, credentials: Any) -> EventInput | None:
        name = self._login_failed_event
        organization_id = self._resolve_organization(request, None)
        if not organization_id:
            logger.debug("santati: skipped audit event %r: no organization_id", name)
            return None
        event = base_event(name, organization_id=organization_id, trail=self._trail)
        event["actor"] = {"type": "anonymous"}
        # Only the attempted identifier: Django masks passwords but not, say,
        # a one-time code, so no other credential value is ever sent.
        attempted = metadata(username=_attempted_username(credentials))
        if attempted:
            event["metadata"] = attempted
        resolved_context = self._resolve_context(request)
        if resolved_context:
            event["context"] = resolved_context
        return event

    def _resolve_organization(self, request: HttpRequest | None, user: AbstractBaseUser | None) -> str | None:
        resolver = self._organization_id
        if isinstance(resolver, str):
            return resolver or None
        return resolver(request, user) or None

    def _resolve_context(self, request: HttpRequest | None) -> dict[str, Any]:
        if request is None:
            return {}
        if self._context is not None:
            return self._context(request)
        return _request_context(request)


def instrument_auth(
    client: Santati | None = None,
    *,
    dispatch: Dispatch | None = None,
    organization_id: str | Callable[[HttpRequest | None, AbstractBaseUser | None], str | None],
    trail: str | None = None,
    actor: Callable[[HttpRequest, AbstractBaseUser], ActorInput] | None = None,
    context: Callable[[HttpRequest], dict[str, Any]] | None = None,
    login_event: str | None = USER_LOGGED_IN,
    logout_event: str | None = USER_LOGGED_OUT,
    login_failed_event: str | None = USER_LOGIN_FAILED,
) -> AuthInstrumentation:
    """Audit Django logins, logouts and failed logins; returns the instrumentation.

    Exactly one of ``client`` and ``dispatch`` is required. ``organization_id``
    is a fixed string or a callable receiving ``(request, user)`` — the user is
    ``None`` for a failed login — and returning the organization to record, or
    ``None``/``""`` to skip the event. Pass ``actor`` (given ``(request, user)``)
    to record the real end user instead of the Django user, and ``context``
    (given the request) to replace the default IP/user-agent context.

    Set an ``*_event`` name to ``None`` to leave that signal alone.
    """
    instrumentation = AuthInstrumentation(
        client,
        dispatch=dispatch,
        organization_id=organization_id,
        trail=trail,
        actor=actor,
        context=context,
        login_event=login_event,
        logout_event=logout_event,
        login_failed_event=login_failed_event,
    )
    instrumentation.connect()
    return instrumentation


def _user_actor(user: AbstractBaseUser) -> ActorInput:
    actor: ActorInput = {"type": "user", "id": str(user.pk)}
    name = user.get_username()
    if name:
        actor["name"] = name
    return actor


def _request_context(request: HttpRequest) -> dict[str, Any]:
    """The request's provenance, in the convention the API documents."""
    values = (("location", request.META.get("REMOTE_ADDR")), ("user_agent", request.META.get("HTTP_USER_AGENT")))
    return {name: value for name, value in values if value}


def _attempted_username(credentials: Mapping[str, Any] | None) -> str:
    """The identifier the failed login tried, and nothing else from the credentials."""
    if not credentials:
        return ""
    from django.contrib.auth import get_user_model

    # django-stubs types get_user_model() with a TypeVar that also covers the
    # manager, so its attribute is a union; at runtime it is the model class.
    username_field: str = getattr(get_user_model(), "USERNAME_FIELD", "username")
    for field in (username_field, "username", "email"):
        value = credentials.get(field)
        if isinstance(value, str) and value:
            return value
    return ""
