"""Run the language-neutral conformance vectors in ``conformance/`` against this SDK.

One test per vector, named by its ``id``. See ``conformance/README.md`` for the
vector format and the runner contract every SDK follows.
"""

from __future__ import annotations

import contextlib
import json
import threading
import time
from collections.abc import Callable, Iterator
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any

import pytest

import santati

CONFORMANCE_DIR = Path(__file__).resolve().parents[2] / "conformance"
CASES: list[dict[str, Any]] = [
    case for path in sorted(CONFORMANCE_DIR.glob("cases/*.json")) for case in json.loads(path.read_text())["cases"]
]
if not CASES:
    raise RuntimeError(f"no conformance cases found under {CONFORMANCE_DIR}")

ERROR_CLASSES: dict[str, type[BaseException]] = {
    name: getattr(santati, name)
    for name in (
        "ValidationError",
        "AuthError",
        "NotFoundError",
        "RateLimitedError",
        "ServerError",
        "TransportError",
        "ApiError",
        "OutboxError",
        "SchemaValidationError",
    )
}

RecordedRequest = dict[str, Any]
Runner = Callable[[dict[str, Any], str], Any]


def _body_bytes(body: dict[str, Any] | None) -> bytes:
    if body is None:
        return b""
    if "text" in body:
        return body["text"].encode()
    if "json" in body:
        return json.dumps(body["json"], separators=(",", ":")).encode()
    raise AssertionError(f"unknown Body: {body!r}")


def _content_type(body: dict[str, Any]) -> str:
    return "application/json" if "json" in body else "text/plain; charset=utf-8"


@contextlib.contextmanager
def _gateway(spec: dict[str, Any], requests: list[RecordedRequest]) -> Iterator[str]:
    """A real HTTP server standing in for the API: it answers every request
    with the vector's response (or the next one in its sequence) and records
    what it saw.
    """
    if spec.get("unreachable"):
        yield "http://127.0.0.1:1"
        return

    responses = spec.get("sequence", [spec])

    class Handler(BaseHTTPRequestHandler):
        def _handle(self) -> None:
            length = int(self.headers.get("Content-Length") or 0)
            raw = self.rfile.read(length) if length else b""
            response = responses[min(len(requests), len(responses) - 1)]
            requests.append(
                {
                    "method": self.command,
                    "path": self.path,
                    "headers": {key.lower(): value for key, value in self.headers.items()},
                    "body": json.loads(raw) if raw else None,
                }
            )
            delay = response.get("delay_ms", 0) / 1000
            if delay:
                time.sleep(delay)
            body = response.get("body")
            payload = _body_bytes(body)
            headers = dict(response.get("headers") or {})
            if payload and not any(name.lower() == "content-type" for name in headers):
                headers["Content-Type"] = _content_type(body)
            try:
                self.send_response(response["status"])
                for name, value in headers.items():
                    self.send_header(name, value)
                self.send_header("Content-Length", str(len(payload)))
                self.end_headers()
                self.wfile.write(payload)
            except (BrokenPipeError, ConnectionResetError):
                # The client gave up before (or while) we answered: fine.
                self.close_connection = True

        def do_GET(self) -> None:
            self._handle()

        def do_POST(self) -> None:
            self._handle()

        def do_PUT(self) -> None:
            self._handle()

        def do_PATCH(self) -> None:
            self._handle()

        def do_DELETE(self) -> None:
            self._handle()

        def log_message(self, *args: Any) -> None:
            pass

    class Server(ThreadingHTTPServer):
        # A delayed response must not keep `shutdown()`/`server_close()` from
        # returning once the client has already given up (the timeout cases).
        daemon_threads = True
        block_on_close = False

    server = Server(("127.0.0.1", 0), Handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        yield f"http://127.0.0.1:{server.server_port}"
    finally:
        server.shutdown()
        server.server_close()
        thread.join()


def _open_client(case_input: dict[str, Any], base_url: str, *, outbox: bool = False, **extra: Any) -> santati.Santati:
    options = dict(case_input.get("client") or {})
    base_path = options.pop("base_path", "")
    max_pending = options.pop("max_pending", None)
    if outbox:
        options["outbox"] = (
            santati.MemoryOutbox(max_pending=max_pending) if max_pending is not None else santati.MemoryOutbox()
        )
    return santati.Santati(base_url=f"{base_url}{base_path}", **options, **extra)


def _emit_result(result: santati.EmitResult) -> dict[str, Any]:
    return {
        "event": result.event.to_dict() if result.event is not None else None,
        "duplicate": result.duplicate,
        "idempotency_key": result.idempotency_key,
        "queued": result.queued,
    }


def _run_emit(case: dict[str, Any], base_url: str) -> Any:
    with _open_client(case["input"], base_url) as client:
        result = client.events.emit(**case["input"]["event"])
    return _emit_result(result)


def _run_emit_batch(case: dict[str, Any], base_url: str) -> Any:
    with _open_client(case["input"], base_url) as client:
        result = client.events.emit_batch(case["input"]["events"])
    return {
        "accepted": result.accepted,
        "rejected": result.rejected,
        "results": [_batch_item(item) for item in result.results],
    }


def _batch_item(item: santati.BatchItem) -> dict[str, Any]:
    rendered: dict[str, Any] = {"index": item.index, "status": item.status}
    if item.id is not None:
        rendered["id"] = item.id
    if item.error is not None:
        rendered["error"] = {"code": item.error.code, "message": item.error.message, "field": item.error.field}
    return rendered


def _run_list(case: dict[str, Any], base_url: str) -> Any:
    with _open_client(case["input"], base_url) as client:
        page = client.events.list(**case["input"].get("params", {}))
    return {"results": [event.to_dict() for event in page.results], "next_cursor": page.next_cursor}


def _run_iterate(case: dict[str, Any], base_url: str) -> Any:
    with _open_client(case["input"], base_url) as client:
        events = list(client.events.iterate(**case["input"].get("params", {})))
    return [event.to_dict() for event in events]


def _version_result(result: santati.SchemaVersionResult) -> dict[str, Any]:
    return {"schema_version": result.schema_version.to_dict(), "etag": result.etag}


def _schema_runner(method: str, *keys: str, render: Callable[[Any], Any]) -> Runner:
    """A runner calling ``client.schemas.<method>`` with the case input's ``keys`` as positional arguments."""

    def run(case: dict[str, Any], base_url: str) -> Any:
        with _open_client(case["input"], base_url) as client:
            return render(getattr(client.schemas, method)(*(case["input"][key] for key in keys)))

    return run


def _run_with_params(method: str, *keys: str, render: Callable[[Any], Any]) -> Runner:
    """Like :func:`_schema_runner`, plus the case's ``params`` as keyword arguments."""

    def run(case: dict[str, Any], base_url: str) -> Any:
        with _open_client(case["input"], base_url) as client:
            args = (case["input"][key] for key in keys)
            return render(getattr(client.schemas, method)(*args, **case["input"].get("params", {})))

    return run


def _run_create_definition(case: dict[str, Any], base_url: str) -> Any:
    definition = dict(case["input"]["definition"])
    with _open_client(case["input"], base_url) as client:
        return client.schemas.create_definition(definition.pop("action"), **definition).to_dict()


def _run_update_definition(case: dict[str, Any], base_url: str) -> Any:
    with _open_client(case["input"], base_url) as client:
        return client.schemas.update_definition(case["input"]["action"], **case["input"]["changes"]).to_dict()


def _run_update_version(case: dict[str, Any], base_url: str) -> Any:
    spec = case["input"]
    with _open_client(spec, base_url) as client:
        result = client.schemas.update_version(
            spec["action"], spec["version"], spec["schema"], if_match=spec.get("if_match")
        )
    return _version_result(result)


def _page(page: Any) -> dict[str, Any]:
    return {"results": [item.to_dict() for item in page.results], "next_cursor": page.next_cursor}


def _wire(model: Any) -> dict[str, Any]:
    return model.to_dict()  # type: ignore[no-any-return]  # pydantic model from the generated core


def _items(items: Iterator[Any]) -> list[dict[str, Any]]:
    return [item.to_dict() for item in items]


def _nothing(_result: None) -> None:
    return None


def _run_emit_outbox(case: dict[str, Any], base_url: str) -> Any:
    """Emit every event through an outbox, close, and report the results and each post_send outcome."""
    hooks = case["input"].get("hooks") or {}
    outcomes: list[dict[str, Any]] = []

    def post_send(event: santati.EventInput, outcome: santati.SendOutcome) -> None:
        error = outcome.error
        outcomes.append(
            {
                "event": json.loads(json.dumps(event)),
                "status": outcome.status,
                "id": outcome.id,
                "error": None
                if error is None
                else {
                    "kind": type(error).__name__,
                    "status": error.status,
                    "code": error.code,
                    "field": error.field,
                    "retry_after": error.retry_after,
                },
            }
        )
        if (hooks.get("post_send") or {}).get("raise"):
            raise RuntimeError("conformance post_send")

    extra: dict[str, Any] = {"post_send": post_send}
    if "pre_send" in hooks:
        spec = hooks["pre_send"]

        def pre_send(event: santati.EventInput) -> santati.EventInput | None:
            if spec.get("raise"):
                raise RuntimeError("conformance pre_send")
            if event["event"] in spec.get("drop_events", []):
                return None
            if "set_metadata" in spec:
                return {**event, "metadata": {**event.get("metadata", {}), **spec["set_metadata"]}}
            return event

        extra["pre_send"] = pre_send

    client = _open_client(case["input"], base_url, outbox=True, **extra)
    results: list[dict[str, Any]] = []
    error: santati.SantatiError | None = None
    try:
        for event in case["input"]["events"]:
            results.append(_emit_result(client.events.emit(**event)))
    except santati.SantatiError as err:
        error = err
    finally:
        client.close()
    if error is not None:
        raise error
    return {"results": results, "outcomes": outcomes}


RUNNERS: dict[str, Runner] = {
    "emit": _run_emit,
    "emit_batch": _run_emit_batch,
    "list": _run_list,
    "iterate": _run_iterate,
    "emit_outbox": _run_emit_outbox,
    "list_definitions": _run_with_params("list_definitions", render=_page),
    "iterate_definitions": _run_with_params("iterate_definitions", render=_items),
    "get_definition": _schema_runner("get_definition", "action", render=_wire),
    "create_definition": _run_create_definition,
    "update_definition": _run_update_definition,
    "delete_definition": _schema_runner("delete_definition", "action", render=_nothing),
    "list_versions": _run_with_params("list_versions", "action", render=_page),
    "iterate_versions": _run_with_params("iterate_versions", "action", render=_items),
    "get_version": _schema_runner("get_version", "action", "version", render=_version_result),
    "create_version": _schema_runner("create_version", "action", "schema", render=_version_result),
    "update_version": _run_update_version,
    "delete_version": _schema_runner("delete_version", "action", "version", render=_nothing),
    "publish_version": _schema_runner("publish_version", "action", "version", render=_version_result),
    "deprecate_version": _schema_runner("deprecate_version", "action", "version", render=_version_result),
    "check_schema": _schema_runner("check_schema", "action", "schema", render=_wire),
    "list_standard_packs": _schema_runner("list_standard_packs", render=_wire),
    "install_standard_packs": _schema_runner("install_standard_packs", "packs", render=_wire),
}


def _without_nulls(value: Any) -> Any:
    """Deep-copy ``value`` without any object member whose value is null."""
    if isinstance(value, dict):
        return {key: _without_nulls(item) for key, item in value.items() if item is not None}
    if isinstance(value, list):
        return [_without_nulls(item) for item in value]
    return value


def _assert_match(actual: Any, expected: Any, bindings: dict[str, Any]) -> None:
    """Deep equality, with ``{"$generated": label}`` matching any non-empty string."""
    if isinstance(expected, dict) and set(expected) == {"$generated"}:
        label = expected["$generated"]
        assert isinstance(actual, str) and actual, f"expected a generated string, got {actual!r}"
        bound = bindings.setdefault(label, actual)
        assert bound == actual, f"generated {label!r}: {actual!r} != earlier {bound!r}"
        reused = [name for name, value in bindings.items() if name != label and value == actual]
        assert not reused, f"generated value {actual!r} is bound to {label!r} and {reused!r}"
        return
    if isinstance(expected, dict):
        assert isinstance(actual, dict), f"expected an object, got {actual!r}"
        assert set(actual) == set(expected), f"keys: {sorted(actual)} != {sorted(expected)}"
        for key, value in expected.items():
            _assert_match(actual[key], value, bindings)
        return
    if isinstance(expected, list):
        assert isinstance(actual, list), f"expected a list, got {actual!r}"
        assert len(actual) == len(expected), f"length: {len(actual)} != {len(expected)}: {actual!r}"
        for got, want in zip(actual, expected):
            _assert_match(got, want, bindings)
        return
    assert actual == expected, f"{actual!r} != {expected!r}"


def _assert_requests(actual: list[RecordedRequest], expected: list[dict[str, Any]], bindings: dict[str, Any]) -> None:
    assert len(actual) == len(expected), f"expected {len(expected)} requests, got {len(actual)}: {actual!r}"
    for got, want in zip(actual, expected):
        assert got["method"] == want["method"], f"method: {got['method']!r} != {want['method']!r}"
        assert got["path"] == want["path"], f"path: {got['path']!r} != {want['path']!r}"
        for name, value in (want.get("headers") or {}).items():
            assert got["headers"].get(name) == value, f"header {name!r}: {got['headers'].get(name)!r} != {value!r}"
        if "body" in want:
            _assert_match(got["body"], want["body"], bindings)


@pytest.mark.parametrize("case", CASES, ids=[case["id"] for case in CASES])
def test_conformance(case: dict[str, Any]) -> None:
    operation = case["operation"]
    runner = RUNNERS.get(operation)
    if runner is None:
        pytest.fail(f"unknown conformance operation {operation!r}")

    requests: list[RecordedRequest] = []
    bindings: dict[str, Any] = {}
    ok: Any = None
    error: BaseException | None = None
    with _gateway(case["input"]["gateway"], requests) as base_url:
        try:
            ok = runner(case, base_url)
        except Exception as err:
            name = next((name for name, cls in ERROR_CLASSES.items() if type(err) is cls), None)
            if name is None:
                raise
            error = err

    expect = case["expect"]
    if "ok" in expect:
        assert error is None, f"expected ok, got {type(error).__name__}: {error}"
        _assert_match(_without_nulls(ok), _without_nulls(expect["ok"]), bindings)
    elif "error" in expect:
        want = expect["error"]
        assert error is not None, f"expected error {want!r}, got ok {ok!r}"
        assert type(error).__name__ == want["kind"], f"expected {want['kind']}, got {error!r}"
        for key, value in want.items():
            if key == "kind":
                continue
            assert getattr(error, key) == value, f"{want['kind']}.{key}: {getattr(error, key)!r} != {value!r}"

    if "requests" in expect:
        _assert_requests(requests, expect["requests"], bindings)
