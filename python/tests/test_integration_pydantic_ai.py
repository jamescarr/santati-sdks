"""The Pydantic AI capability, driven through ``TestModel`` runs."""

from __future__ import annotations

from typing import Any

import pytest
from pydantic_ai import Agent, models
from pydantic_ai.models.test import TestModel

from santati.integrations.pydantic_ai import SantatiCapability

models.ALLOW_MODEL_REQUESTS = False


def capability(captured: list[dict[str, Any]], **overrides: Any) -> SantatiCapability:
    return SantatiCapability(dispatch=captured.append, organization_id="org_acme", trail="agents", **overrides)


def test_a_successful_tool_call_is_audited() -> None:
    captured: list[dict[str, Any]] = []
    agent = Agent(TestModel(), name="billing-bot", capabilities=[capability(captured)])

    @agent.tool_plain
    def lookup_invoice(invoice_id: str) -> str:
        """Look up one invoice by its id."""
        return "INV-1: $42.00"

    agent.run_sync("look up inv_1")

    assert len(captured) == 1
    event = captured[0]
    assert event["event"] == "agent.tool_call.succeeded"
    assert event["trail"] == "agents"
    assert event["targets"] == [{"type": "tool", "id": "lookup_invoice"}]
    assert list(event["data"]["arguments"]) == ["invoice_id"]
    assert event["metadata"]["framework"] == "pydantic_ai"
    assert event["metadata"]["agent"] == "billing-bot"
    assert event["metadata"]["run_id"]
    assert event["metadata"]["tool_call_id"]
    assert event["actor"] == {"type": "system", "id": "billing-bot"}


def test_a_raising_tool_call_is_audited() -> None:
    captured: list[dict[str, Any]] = []
    agent = Agent(TestModel(), name="billing-bot", capabilities=[capability(captured)])

    @agent.tool_plain
    def void_invoice(invoice_id: str) -> str:
        """Void one invoice."""
        raise RuntimeError("invoice locked")

    with pytest.raises(RuntimeError, match="invoice locked"):
        agent.run_sync("void inv_1")

    assert len(captured) == 1
    assert captured[0]["event"] == "agent.tool_call.failed"
    assert captured[0]["data"]["error"] == "RuntimeError: invoice locked"


def test_a_capability_can_be_attached_to_a_single_run() -> None:
    captured: list[dict[str, Any]] = []
    agent = Agent(TestModel(), name="billing-bot")

    @agent.tool_plain
    def lookup_invoice(invoice_id: str) -> str:
        """Look up one invoice by its id."""
        return "INV-1: $42.00"

    agent.run_sync("look up inv_1", capabilities=[capability(captured)])

    assert [event["event"] for event in captured] == ["agent.tool_call.succeeded"]


def test_a_raising_dispatch_leaves_the_run_alone() -> None:
    def explode(event: dict[str, Any]) -> None:
        raise RuntimeError("queue is down")

    configured = SantatiCapability(dispatch=explode, organization_id="org_acme", trail="agents")
    agent = Agent(TestModel(), name="billing-bot", capabilities=[configured])

    @agent.tool_plain
    def lookup_invoice(invoice_id: str) -> str:
        """Look up one invoice by its id."""
        return "INV-1: $42.00"

    assert agent.run_sync("look up inv_1").output


def test_an_actor_replaces_the_agent_actor() -> None:
    captured: list[dict[str, Any]] = []
    agent = Agent(
        TestModel(), name="billing-bot", capabilities=[capability(captured, actor={"type": "user", "id": "usr_123"})]
    )

    @agent.tool_plain
    def lookup_invoice(invoice_id: str) -> str:
        """Look up one invoice by its id."""
        return "INV-1: $42.00"

    agent.run_sync("look up inv_1")

    assert captured[0]["actor"] == {"type": "user", "id": "usr_123"}
    assert captured[0]["metadata"]["agent"] == "billing-bot"
