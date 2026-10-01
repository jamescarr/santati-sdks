"""The LangChain callback handler, driven through real tool invocations."""

from __future__ import annotations

from typing import Any

import pytest
from langchain_core.messages import ToolMessage
from langchain_core.tools import StructuredTool, ToolException, tool

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


def test_a_raising_dispatch_leaves_the_tool_call_alone() -> None:
    def explode_dispatch(event: dict[str, Any]) -> None:
        raise RuntimeError("queue is down")

    configured = SantatiCallbackHandler(dispatch=explode_dispatch, organization_id="org_acme", trail="agents")

    assert lookup_invoice.invoke(TOOL_CALL, config={"callbacks": [configured]}).content == "INV-1: $42.00"
