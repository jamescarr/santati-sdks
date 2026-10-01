"""The LangChain callback handler, driven through real tool invocations."""

from __future__ import annotations

import sys
from typing import Any

import pytest
from langchain_core.messages import AIMessage, ToolMessage
from langchain_core.tools import StructuredTool, ToolException, tool
from langgraph.checkpoint.memory import InMemorySaver
from langgraph.errors import GraphInterrupt
from langgraph.graph import END, START, MessagesState, StateGraph
from langgraph.prebuilt import ToolNode
from langgraph.types import Command, interrupt

import santati.integrations.langchain as langchain_module
from santati.integrations.langchain import SantatiCallbackHandler

TOOL_CALL = {"name": "lookup_invoice", "args": {"invoice_id": "inv_1"}, "id": "call_1", "type": "tool_call"}


def handler(captured: list[dict[str, Any]], **overrides: Any) -> SantatiCallbackHandler:
    return SantatiCallbackHandler(dispatch=captured.append, organization_id="org_acme", trail="agents", **overrides)


@tool
def lookup_invoice(invoice_id: str) -> str:
    """Look up one invoice by its id."""
    return "INV-1: $42.00"


def test_a_successful_tool_call_is_audited() -> None:
    captured: list[dict[str, Any]] = []

    assert lookup_invoice.invoke(TOOL_CALL, config={"callbacks": [handler(captured)]}).content == "INV-1: $42.00"

    assert len(captured) == 1
    event = captured[0]
    assert event["event"] == "agent.tool_call.succeeded"
    assert event["organization_id"] == "org_acme"
    assert event["trail"] == "agents"
    assert event["targets"] == [{"type": "tool", "id": "lookup_invoice"}]
    assert event["data"] == {"arguments": {"invoice_id": "inv_1"}}
    assert event["metadata"] == {"framework": "langchain", "tool_call_id": "call_1"}
    assert event["actor"] == {"type": "system", "id": "langchain"}


@tool
def explode(invoice_id: str) -> str:
    """Fail loudly."""
    raise RuntimeError("boom")


def test_a_raising_tool_call_is_audited() -> None:
    captured: list[dict[str, Any]] = []

    with pytest.raises(RuntimeError, match="boom"):
        explode.invoke(TOOL_CALL, config={"callbacks": [handler(captured)]})

    assert len(captured) == 1
    assert captured[0]["event"] == "agent.tool_call.failed"
    assert captured[0]["data"] == {"arguments": {"invoice_id": "inv_1"}, "error": "RuntimeError: boom"}


def void_invoice(invoice_id: str) -> str:
    """Void one invoice."""
    raise ToolException("invoice locked")


def test_a_handled_tool_error_is_audited() -> None:
    captured: list[dict[str, Any]] = []
    voiding = StructuredTool.from_function(void_invoice, handle_tool_error=True)

    result = voiding.invoke(TOOL_CALL, config={"callbacks": [handler(captured)]})

    assert isinstance(result, ToolMessage) and result.content == "invoice locked"
    assert len(captured) == 1
    assert captured[0]["event"] == "agent.tool_call.failed"
    assert captured[0]["data"] == {"arguments": {"invoice_id": "inv_1"}, "error": "invoice locked"}


def test_an_actor_and_custom_event_names_replace_the_defaults() -> None:
    captured: list[dict[str, Any]] = []
    configured = handler(
        captured,
        actor={"type": "user", "id": "usr_123", "name": "Dana Ortiz"},
        succeeded_event="tool.used",
        failed_event="tool.broke",
    )

    lookup_invoice.invoke(TOOL_CALL, config={"callbacks": [configured]})

    assert captured[0]["event"] == "tool.used"
    assert captured[0]["actor"] == {"type": "user", "id": "usr_123", "name": "Dana Ortiz"}


@tool
def pause_for_approval(invoice_id: str) -> str:
    """Ask a human before voiding; LangGraph raises this to pause the run."""
    raise GraphInterrupt(())


def test_an_interrupted_tool_call_is_not_audited() -> None:
    captured: list[dict[str, Any]] = []

    with pytest.raises(GraphInterrupt):
        pause_for_approval.invoke(TOOL_CALL, config={"callbacks": [handler(captured)]})

    assert captured == []


def test_a_failure_is_still_audited_without_langgraph(monkeypatch: pytest.MonkeyPatch) -> None:
    """The extra installs langchain-core only, so langgraph may be absent."""
    # Both entries: a cached submodule alone would satisfy the import.
    monkeypatch.delitem(sys.modules, "langgraph.errors")
    monkeypatch.setitem(sys.modules, "langgraph", None)
    monkeypatch.setattr(langchain_module, "_BUBBLE_UP", None)
    captured: list[dict[str, Any]] = []

    with pytest.raises(RuntimeError, match="boom"):
        explode.invoke(TOOL_CALL, config={"callbacks": [handler(captured)]})

    assert langchain_module._bubble_up_types() == ()
    assert [event["event"] for event in captured] == ["agent.tool_call.failed"]
    assert captured[0]["data"]["error"] == "RuntimeError: boom"


@tool
def void_invoice_with_approval(invoice_id: str) -> str:
    """Void one invoice, once a human approves it."""
    approved = interrupt({"action": "void", "invoice_id": invoice_id})
    return f"voided {invoice_id}: {approved}"


def test_a_paused_approval_records_nothing_and_its_resume_records_once() -> None:
    """The real flow: a human-in-the-loop pause, then the resumed run executes the tool."""
    captured: list[dict[str, Any]] = []
    builder = StateGraph(MessagesState)
    builder.add_node("tools", ToolNode([void_invoice_with_approval]))
    builder.add_edge(START, "tools")
    builder.add_edge("tools", END)
    graph = builder.compile(checkpointer=InMemorySaver())
    config = {"configurable": {"thread_id": "t1"}, "callbacks": [handler(captured)]}
    call = AIMessage(
        content="",
        tool_calls=[
            {"name": "void_invoice_with_approval", "args": {"invoice_id": "inv_1"}, "id": "call_1", "type": "tool_call"}
        ],
    )

    paused = graph.invoke({"messages": [call]}, config)
    assert paused["__interrupt__"], "the graph should have paused for approval"
    assert captured == []

    graph.invoke(Command(resume=True), config)
    assert [event["event"] for event in captured] == ["agent.tool_call.succeeded"]
    assert captured[0]["data"] == {"arguments": {"invoice_id": "inv_1"}}


def test_a_raising_dispatch_leaves_the_tool_call_alone() -> None:
    def explode_dispatch(event: dict[str, Any]) -> None:
        raise RuntimeError("queue is down")

    configured = SantatiCallbackHandler(dispatch=explode_dispatch, organization_id="org_acme", trail="agents")

    assert lookup_invoice.invoke(TOOL_CALL, config={"callbacks": [configured]}).content == "INV-1: $42.00"
