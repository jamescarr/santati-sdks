"""The shared integration core: dispatch resolution, the drop guard, and envelope helpers.

No framework is involved; the tests drive the private core directly, using a
real HTTP server for the default-dispatch path.
"""

from __future__ import annotations

import asyncio
import contextlib
import json
import logging
import threading
from collections.abc import Iterator
from datetime import datetime, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any

import pytest

import santati
from santati.integrations import _core

CONFORMANCE_DIR = Path(__file__).resolve().parents[2] / "conformance"
CASES: dict[str, dict[str, Any]] = {
    case["id"]: case
    for path in sorted(CONFORMANCE_DIR.glob("cases/*.json"))
    for case in json.loads(path.read_text())["cases"]
}
GENERATED_KEY_BODY: dict[str, Any] = CASES["emit.generated_key"]["input"]["gateway"]["body"]["json"]

RecordedRequest = dict[str, Any]


@contextlib.contextmanager
def _gateway(requests: list[RecordedRequest]) -> Iterator[str]:
    """A real HTTP server answering every request the way the API answers ``emit``."""

    class Handler(BaseHTTPRequestHandler):
        def do_POST(self) -> None:
            length = int(self.headers.get("Content-Length") or 0)
            raw = self.rfile.read(length) if length else b""
            requests.append({"path": self.path, "body": json.loads(raw)})
            body = json.dumps(GENERATED_KEY_BODY).encode()
            self.send_response(201)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, *args: Any) -> None:
            pass

    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        yield f"http://127.0.0.1:{server.server_port}"
    finally:
        server.shutdown()
        server.server_close()
        thread.join()


def test_default_dispatch_sends_the_built_envelope() -> None:
    requests: list[RecordedRequest] = []
    with (
        _gateway(requests) as base_url,
        santati.Santati("sat_sk_test", base_url=base_url, trail="auth", max_retries=0) as client,
    ):
        dispatch = _core.resolve_dispatch(client, None, None)
        event = _core.base_event("user.logged_in", organization_id="org_acme", trail="auth")
        dispatch(event)

    assert len(requests) == 1
    assert requests[0]["path"] == "/api/v0/events/"
    body = requests[0]["body"]
    for key in ("idempotency_key", "created_at", "organization_id", "trail"):
        assert body[key] == event[key], key
    assert body["event"] == "user.logged_in"
    assert body["trail"] == "auth"


def test_unreachable_client_is_swallowed_and_logged(caplog: pytest.LogCaptureFixture) -> None:
    with santati.Santati("sat_sk_test", base_url="http://127.0.0.1:1", trail="auth", max_retries=0) as client:
        dispatch = _core.resolve_dispatch(client, None, None)
        with caplog.at_level(logging.WARNING, logger="santati.integrations"):
            result = _core.deliver(
                dispatch,
                "user.logged_in",
                lambda: _core.base_event("user.logged_in", organization_id="org_acme", trail=None),
            )

    assert result is None
    dropped = [record for record in caplog.records if record.name == "santati.integrations"]
    assert [record.levelno for record in dropped] == [logging.WARNING]
    assert "user.logged_in" in dropped[0].getMessage()


def test_construction_requires_exactly_one_of_client_and_dispatch() -> None:
    with santati.Santati("sat_sk_test", trail="auth") as client:
        with pytest.raises(santati.ValidationError) as both:
            _core.resolve_dispatch(client, lambda event: None, None)
        assert both.value.field == "dispatch"

    with pytest.raises(santati.ValidationError) as neither:
        _core.resolve_dispatch(None, None, None)
    assert neither.value.field == "dispatch"


def test_client_dispatch_requires_a_trail() -> None:
    with santati.Santati("sat_sk_test") as client, pytest.raises(santati.ValidationError) as error:
        _core.resolve_dispatch(client, None, None)
    assert error.value.field == "trail"


def test_tool_call_auditor_requires_an_organization_id() -> None:
    with pytest.raises(santati.ValidationError) as error:
        _core.ToolCallAuditor(
            "langchain",
            None,
            dispatch=lambda event: None,
            organization_id="",
            trail=None,
            actor=None,
            succeeded_event=_core.TOOL_CALL_SUCCEEDED,
            failed_event=_core.TOOL_CALL_FAILED,
        )
    assert error.value.field == "organization_id"


def test_json_safe_parses_json_text_and_stringifies_the_rest() -> None:
    assert _core.json_safe('{"a": 1}') == {"a": 1}
    assert _core.json_safe("not json") == "not json"
    assert _core.json_safe(None) is None

    rendered = _core.json_safe({"at": datetime(2026, 1, 2, 3, 4, 5, tzinfo=timezone.utc), "opaque": object()})
    assert json.loads(json.dumps(rendered))["at"] == "2026-01-02T03:04:05Z"
    assert isinstance(rendered["opaque"], str)


def test_metadata_drops_empty_values_and_truncates() -> None:
    long = "x" * 600
    assert _core.metadata(framework="langchain", agent=None, run_id="", note=long) == {
        "framework": "langchain",
        "note": "x" * 500,
    }


def test_adeliver_dispatches_off_the_event_loop() -> None:
    """A synchronous client must never run on the loop, or one emit stalls every task."""
    threads: list[str] = []

    async def run() -> str:
        await _core.adeliver(
            lambda event: threads.append(threading.current_thread().name),
            "user.logged_in",
            lambda: _core.base_event("user.logged_in", organization_id="org_acme", trail=None),
        )
        return threading.current_thread().name

    loop_thread = asyncio.run(run())

    assert len(threads) == 1
    assert threads[0] != loop_thread
