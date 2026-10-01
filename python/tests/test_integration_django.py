"""The Django auth instrumentation, driven through Django's own signals.

Django is configured directly (no settings module, no database): the tests
send the signals a browser login would, and inspect the envelopes the
instrumentation builds.
"""

from __future__ import annotations

import logging
from collections.abc import Iterator
from typing import Any

import pytest
from django.conf import settings

if not settings.configured:
    settings.configure(INSTALLED_APPS=["django.contrib.contenttypes", "django.contrib.auth"], SECRET_KEY="test")

import django

django.setup()

from django.contrib.auth.models import User
from django.contrib.auth.signals import user_logged_in, user_logged_out, user_login_failed
from django.test import RequestFactory

import santati
from santati.integrations.django import AuthInstrumentation, instrument_auth

# Django's own receiver writes `last_login` to the database; there is no database here.
user_logged_in.disconnect(dispatch_uid="update_last_login")

REQUEST = RequestFactory().get("/", REMOTE_ADDR="203.0.113.7", HTTP_USER_AGENT="pytest")


@pytest.fixture
def captured() -> list[dict[str, Any]]:
    return []


@pytest.fixture
def instrumentation(captured: list[dict[str, Any]]) -> Iterator[AuthInstrumentation]:
    auth = instrument_auth(dispatch=captured.append, organization_id="org_acme", trail="auth")
    yield auth
    auth.disconnect()


def user() -> User:
    return User(pk=42, username="dana")


def test_login_is_audited(captured: list[dict[str, Any]], instrumentation: AuthInstrumentation) -> None:
    user_logged_in.send(sender=User, request=REQUEST, user=user())

    assert len(captured) == 1
    event = captured[0]
    assert event["event"] == "user.logged_in"
    assert event["trail"] == "auth"
    assert event["organization_id"] == "org_acme"
    assert event["actor"] == {"type": "user", "id": "42", "name": "dana"}
    assert event["metadata"] == {"username": "dana"}
    assert event["context"] == {"location": "203.0.113.7", "user_agent": "pytest"}
    assert "targets" not in event and "data" not in event
    assert event["idempotency_key"] and event["created_at"]


def test_logout_is_audited(captured: list[dict[str, Any]], instrumentation: AuthInstrumentation) -> None:
    user_logged_out.send(sender=User, request=REQUEST, user=user())

    assert [event["event"] for event in captured] == ["user.logged_out"]
    assert captured[0]["actor"] == {"type": "user", "id": "42", "name": "dana"}
    assert captured[0]["metadata"] == {"username": "dana"}


def test_anonymous_logout_is_skipped(captured: list[dict[str, Any]], instrumentation: AuthInstrumentation) -> None:
    user_logged_out.send(sender=User, request=REQUEST, user=None)

    assert captured == []


def test_failed_login_records_only_the_attempted_username(
    captured: list[dict[str, Any]], instrumentation: AuthInstrumentation
) -> None:
    credentials = {"username": "dana", "password": "********************", "code": "123456"}
    user_login_failed.send(sender=User, credentials=credentials, request=REQUEST)

    assert len(captured) == 1
    event = captured[0]
    assert event["event"] == "user.login_failed"
    assert event["actor"] == {"type": "anonymous"}
    assert event["metadata"] == {"username": "dana"}
    assert event["context"] == {"location": "203.0.113.7", "user_agent": "pytest"}
    rendered = repr(event)
    assert "123456" not in rendered and "****" not in rendered


def test_failed_login_without_a_request_has_no_context(captured: list[dict[str, Any]]) -> None:
    auth = instrument_auth(dispatch=captured.append, organization_id="org_acme", trail="auth")
    try:
        user_login_failed.send(sender=User, credentials={"email": "dana@example.com"}, request=None)
    finally:
        auth.disconnect()

    assert captured[0]["metadata"] == {"username": "dana@example.com"}
    assert "context" not in captured[0]


def test_a_resolver_returning_none_skips_the_event(captured: list[dict[str, Any]]) -> None:
    auth = instrument_auth(dispatch=captured.append, organization_id=lambda request, user: None, trail="auth")
    try:
        user_logged_in.send(sender=User, request=REQUEST, user=user())
    finally:
        auth.disconnect()

    assert captured == []


def test_an_empty_organization_id_is_rejected(captured: list[dict[str, Any]]) -> None:
    with pytest.raises(santati.ValidationError) as error:
        instrument_auth(dispatch=captured.append, organization_id="", trail="auth")

    assert error.value.field == "organization_id"


def test_a_raising_dispatch_never_escapes(caplog: pytest.LogCaptureFixture) -> None:
    def explode(event: dict[str, Any]) -> None:
        raise RuntimeError("queue is down")

    auth = instrument_auth(dispatch=explode, organization_id="org_acme", trail="auth")
    try:
        with caplog.at_level(logging.WARNING, logger="santati.integrations"):
            user_logged_in.send(sender=User, request=REQUEST, user=user())
    finally:
        auth.disconnect()

    warnings = [record for record in caplog.records if record.name == "santati.integrations"]
    assert [record.levelno for record in warnings] == [logging.WARNING]
    assert "user.logged_in" in warnings[0].getMessage()


def test_a_second_instrumentation_replaces_the_first(captured: list[dict[str, Any]]) -> None:
    first: list[dict[str, Any]] = []
    second: list[dict[str, Any]] = []

    instrument_auth(dispatch=first.append, organization_id="org_acme", trail="auth")
    replacement = instrument_auth(dispatch=second.append, organization_id="org_acme", trail="auth")
    user_logged_in.send(sender=User, request=REQUEST, user=user())
    replacement.disconnect()
    user_logged_in.send(sender=User, request=REQUEST, user=user())

    assert first == []
    assert [event["event"] for event in second] == ["user.logged_in"]


def test_actor_and_context_overrides(captured: list[dict[str, Any]]) -> None:
    auth = instrument_auth(
        dispatch=captured.append,
        organization_id="org_acme",
        trail="auth",
        actor=lambda request, user: {"type": "user", "id": "usr_7", "name": "Dana Ortiz"},
        context=lambda request: {"location": "198.51.100.9"},
    )
    try:
        user_logged_in.send(sender=User, request=REQUEST, user=user())
    finally:
        auth.disconnect()

    assert captured[0]["actor"] == {"type": "user", "id": "usr_7", "name": "Dana Ortiz"}
    assert captured[0]["context"] == {"location": "198.51.100.9"}


def test_disabling_an_event_leaves_its_signal_alone(captured: list[dict[str, Any]]) -> None:
    auth = instrument_auth(dispatch=captured.append, organization_id="org_acme", trail="auth", login_failed_event=None)
    try:
        user_login_failed.send(sender=User, credentials={"username": "dana"}, request=REQUEST)
        user_logged_in.send(sender=User, request=REQUEST, user=user())
    finally:
        auth.disconnect()

    assert [event["event"] for event in captured] == ["user.logged_in"]
