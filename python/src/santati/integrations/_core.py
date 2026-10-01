"""Envelope building and delivery shared by the framework integrations.

Private: nothing here is part of the package's public surface. The modules
under ``santati.integrations`` translate framework callbacks into the public
``Events.emit`` surface; this module holds the parts they share — the dispatch
indirection, the never-raise guard, and the envelope helpers.

No framework is imported here, so importing it never requires an extra.
"""

from __future__ import annotations

import json
import logging
import uuid
from collections.abc import Callable
from datetime import datetime, timezone
from typing import Any, TypeAlias

from pydantic_core import to_jsonable_python

from .._client import Santati
from .._errors import ValidationError
from .._types import ActorInput, EventInput

logger = logging.getLogger("santati.integrations")
"""The integrations' logger; every dropped event logs here at WARNING."""

Dispatch: TypeAlias = Callable[[EventInput], object]
"""How an integration delivers one built envelope.

The return type is ``object`` so a queue's ``task.delay`` fits.
"""

USER_LOGGED_IN = "user.logged_in"
USER_LOGGED_OUT = "user.logged_out"
USER_LOGIN_FAILED = "user.login_failed"
TOOL_CALL_SUCCEEDED = "agent.tool_call.succeeded"
TOOL_CALL_FAILED = "agent.tool_call.failed"

_METADATA_VALUE_LIMIT = 500


def resolve_dispatch(client: Santati | None, dispatch: Dispatch | None, trail: str | None) -> Dispatch:
    """Turn ``client`` xor ``dispatch`` into the callable that sends one event.

    A client is wrapped so it re-uses its resolved trail and its own retries; a
    dispatch is passed through. Exactly one of the two must be given, and a
    client needs a trail here or as its default.
    """
    if (client is None) == (dispatch is None):
        raise ValidationError("pass exactly one of client or dispatch", field="dispatch")
    if client is None:
        assert dispatch is not None
        return dispatch
    if not (trail or client.trail):
        raise ValidationError("trail must be set here or as the client's default trail", field="trail")

    def emit(event: EventInput) -> None:
        client.events.emit(**event)

    return emit


def deliver(dispatch: Dispatch, event_name: str, build: Callable[[], EventInput | None]) -> None:
    """Build and send one event, swallowing anything either step raises.

    ``build`` returning ``None`` skips silently (no organization, an anonymous
    logout). A failure is logged and dropped: an integration never raises into
    the host framework.
    """
    try:
        event = build()
        if event is not None:
            dispatch(event)
    except Exception:
        logger.warning("santati: dropped audit event %r", event_name, exc_info=True)


async def adeliver(dispatch: Dispatch, event_name: str, build: Callable[[], EventInput | None]) -> None:
    """``deliver`` for async callbacks: the blocking dispatch runs in a thread.

    The envelope is built in the calling task; only the dispatch moves off the
    event loop, so a synchronous client never blocks asyncio or trio.
    Cancellation is not caught.
    """
    from anyio import to_thread

    try:
        event = build()
        if event is not None:
            await to_thread.run_sync(_guarded_dispatch, dispatch, event, event_name)
    except Exception:
        logger.warning("santati: dropped audit event %r", event_name, exc_info=True)


def _guarded_dispatch(dispatch: Dispatch, event: EventInput, event_name: str) -> None:
    try:
        dispatch(event)
    except Exception:
        logger.warning("santati: dropped audit event %r", event_name, exc_info=True)


def base_event(event_name: str, *, organization_id: str, trail: str | None) -> EventInput:
    """The members every integration event carries, fixed at build time.

    The idempotency key is generated here rather than by ``emit`` so that a
    dispatcher's retry of the same envelope replays instead of double-recording.
    """
    event: EventInput = {
        "event": event_name,
        "organization_id": organization_id,
        "created_at": datetime.now(timezone.utc).isoformat(timespec="milliseconds"),
        "idempotency_key": str(uuid.uuid4()),
    }
    if trail:
        event["trail"] = trail
    return event


def metadata(**values: str | None) -> dict[str, str]:
    """The supplied members, without the empty ones and within the server's limit."""
    return {name: value[:_METADATA_VALUE_LIMIT] for name, value in values.items() if value}


def json_safe(value: Any) -> Any:
    """``value`` as something ``json.dumps`` accepts.

    A string that parses as JSON (frameworks hand tool arguments over as JSON
    text) becomes the parsed value; one that does not stays a string. Anything
    else the standard serializer cannot express is stringified.
    """
    if isinstance(value, str):
        try:
            value = json.loads(value)
        except ValueError:
            pass
    return to_jsonable_python(value, fallback=str)


def describe_error(error: BaseException | str) -> str:
    """A one-line description of a failure, naming its type when it has one."""
    if isinstance(error, str):
        return error
    return f"{type(error).__name__}: {error}"


class ToolCallAuditor:
    """Builds and sends one audit event per tool call.

    Shared by the agent-framework adapters: they differ only in how they
    observe a call and what they call the framework.
    """

    def __init__(
        self,
        framework: str,
        client: Santati | None,
        *,
        dispatch: Dispatch | None,
        organization_id: str,
        trail: str | None,
        actor: ActorInput | None,
        succeeded_event: str,
        failed_event: str,
    ) -> None:
        if not organization_id:
            raise ValidationError("organization_id must be a non-empty string", field="organization_id")
        self.framework = framework
        self.organization_id = organization_id
        self.trail = trail
        self.actor = actor
        self.succeeded_event = succeeded_event
        self.failed_event = failed_event
        self._dispatch = resolve_dispatch(client, dispatch, trail)

    def build(
        self,
        *,
        tool_name: str,
        arguments: Any,
        call_id: str | None,
        agent: str | None,
        run_id: str | None,
        error: BaseException | str | None,
    ) -> EventInput:
        """The envelope for one tool call that ended with ``error`` or not."""
        failed = error is not None
        event = base_event(
            self.failed_event if failed else self.succeeded_event,
            organization_id=self.organization_id,
            trail=self.trail,
        )
        event["actor"] = self.actor if self.actor is not None else {"type": "system", "id": agent or self.framework}
        event["targets"] = [{"type": "tool", "id": tool_name}]
        event["metadata"] = metadata(framework=self.framework, agent=agent, tool_call_id=call_id, run_id=run_id)
        data: dict[str, Any] = {}
        if arguments is not None:
            data["arguments"] = json_safe(arguments)
        if failed:
            assert error is not None
            data["error"] = describe_error(error)
        if data:
            event["data"] = data
        return event

    def record(
        self,
        *,
        tool_name: str,
        arguments: Any = None,
        call_id: str | None = None,
        agent: str | None = None,
        run_id: str | None = None,
        error: BaseException | str | None = None,
    ) -> None:
        """``build`` then dispatch; the outcome picks the event name."""
        deliver(
            self._dispatch,
            self._event_name(error),
            lambda: self.build(
                tool_name=tool_name, arguments=arguments, call_id=call_id, agent=agent, run_id=run_id, error=error
            ),
        )

    async def arecord(
        self,
        *,
        tool_name: str,
        arguments: Any = None,
        call_id: str | None = None,
        agent: str | None = None,
        run_id: str | None = None,
        error: BaseException | str | None = None,
    ) -> None:
        """``record`` for async hooks: the dispatch runs in a worker thread."""
        await adeliver(
            self._dispatch,
            self._event_name(error),
            lambda: self.build(
                tool_name=tool_name, arguments=arguments, call_id=call_id, agent=agent, run_id=run_id, error=error
            ),
        )

    def _event_name(self, error: BaseException | str | None) -> str:
        return self.failed_event if error is not None else self.succeeded_event
