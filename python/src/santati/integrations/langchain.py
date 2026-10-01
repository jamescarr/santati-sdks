"""LangChain and LangGraph: audit events from tool calls.

``SantatiCallbackHandler`` is a callback handler: attach it to a run and every
tool call that run makes is audited::

    handler = SantatiCallbackHandler(client, organization_id="org_acme")
    agent.invoke({"messages": [...]}, config={"callbacks": [handler]})

It covers ``create_agent``, a raw LangGraph ``ToolNode``, ``create_react_agent``
and a bare ``tool.invoke``, because they all fire tool callbacks from
``BaseTool.run``/``arun``.

The handler is synchronous and its inherited ``run_inline = False`` makes
LangChain run it in an executor thread during async runs, so the blocking emit
never stalls the event loop.
"""

from __future__ import annotations

import threading
from dataclasses import dataclass
from typing import TYPE_CHECKING, Any
from uuid import UUID

from langchain_core.callbacks import BaseCallbackHandler
from langchain_core.messages import ToolMessage

from ._core import TOOL_CALL_FAILED, TOOL_CALL_SUCCEEDED, Dispatch, ToolCallAuditor

if TYPE_CHECKING:
    from .._client import Santati
    from .._types import ActorInput

__all__ = ["SantatiCallbackHandler"]


@dataclass
class _PendingCall:
    """What ``on_tool_start`` saw, kept until the call ends."""

    tool_name: str
    arguments: Any
    call_id: str | None


class SantatiCallbackHandler(BaseCallbackHandler):
    """Audits one audit event per tool call.

    A tool that returns sends ``agent.tool_call.succeeded``; one that raises
    sends ``agent.tool_call.failed``. A tool whose error ``handle_tool_error``
    turned into a result also counts as failed — LangChain reports it as an
    error ``ToolMessage`` and never calls ``on_tool_error``. A call LangGraph
    pauses with ``interrupt()`` produces no event: the pause is control flow,
    and the resumed run audits the call that actually executes.

    Tool callbacks carry no agent name, so the actor is the configured one or
    ``{"type": "system", "id": "langchain"}``.
    """

    def __init__(
        self,
        client: Santati | None = None,
        *,
        dispatch: Dispatch | None = None,
        organization_id: str,
        trail: str | None = None,
        actor: ActorInput | None = None,
        succeeded_event: str = TOOL_CALL_SUCCEEDED,
        failed_event: str = TOOL_CALL_FAILED,
    ) -> None:
        super().__init__()
        self._auditor = ToolCallAuditor(
            "langchain",
            client,
            dispatch=dispatch,
            organization_id=organization_id,
            trail=trail,
            actor=actor,
            succeeded_event=succeeded_event,
            failed_event=failed_event,
        )
        # ToolNode invokes tools from a thread pool, so starts and ends race.
        self._pending: dict[UUID, _PendingCall] = {}
        self._lock = threading.Lock()

    def on_tool_start(
        self,
        serialized: dict[str, Any],
        input_str: str,
        *,
        run_id: UUID,
        parent_run_id: UUID | None = None,
        tags: list[str] | None = None,
        metadata: dict[str, Any] | None = None,
        inputs: dict[str, Any] | None = None,
        **kwargs: Any,
    ) -> None:
        """Remember the call: ``on_tool_end`` and ``on_tool_error`` carry no tool name or args."""
        pending = _PendingCall(
            tool_name=serialized.get("name") or "unknown",
            arguments=inputs if inputs is not None else input_str,
            call_id=_text(kwargs.get("tool_call_id")),
        )
        with self._lock:
            self._pending[run_id] = pending

    def on_tool_end(self, output: Any, *, run_id: UUID, parent_run_id: UUID | None = None, **kwargs: Any) -> None:
        """Audit the finished call, including the errors ``handle_tool_error`` turned into results."""
        pending = self._pop(run_id)
        if pending is None:
            return
        error = str(output.content) if isinstance(output, ToolMessage) and output.status == "error" else None
        self._record(pending, error=error)

    def on_tool_error(
        self, error: BaseException, *, run_id: UUID, parent_run_id: UUID | None = None, **kwargs: Any
    ) -> None:
        """Audit a call whose exception escaped the tool, except LangGraph's control flow."""
        pending = self._pop(run_id)
        if pending is None:
            return
        if isinstance(error, _bubble_up_types()):
            # An interruption signal, not a failure: `interrupt()` pauses the run
            # for approval, and the resumed run executes the tool again.
            return
        self._record(pending, error=error)

    def _pop(self, run_id: UUID) -> _PendingCall | None:
        with self._lock:
            return self._pending.pop(run_id, None)

    def _record(self, pending: _PendingCall, *, error: BaseException | str | None) -> None:
        self._auditor.record(
            tool_name=pending.tool_name,
            arguments=pending.arguments,
            call_id=pending.call_id,
            agent=None,
            run_id=None,
            error=error,
        )


def _text(value: Any) -> str | None:
    """``value`` when it is a non-empty string, else ``None``."""
    return value if isinstance(value, str) and value else None


_BUBBLE_UP: tuple[type[BaseException], ...] | None = None


def _bubble_up_types() -> tuple[type[BaseException], ...]:
    """LangGraph's control-flow exceptions, resolved on first use.

    ``langgraph`` is optional even alongside ``langchain-core``, and importing
    it costs about 0.2 s, so it is only looked up when a tool actually raises.
    """
    global _BUBBLE_UP
    if _BUBBLE_UP is None:
        try:
            from langgraph.errors import GraphBubbleUp
        except ImportError:
            _BUBBLE_UP = ()
        else:
            _BUBBLE_UP = (GraphBubbleUp,)
    return _BUBBLE_UP
