"""The OpenAI Agents SDK run hooks, driven through ``Runner`` and a scripted model."""

from __future__ import annotations

from typing import Any

from agents import Agent, Runner, function_tool
from agents.testing import ScriptedModel, assistant_message, function_call
from agents.tracing import set_tracing_disabled

from santati.integrations.openai_agents import SantatiRunHooks, record_tool_failure

set_tracing_disabled(True)


@function_tool
def lookup_invoice(invoice_id: str) -> str:
    """Look up one invoice by its id."""
    return "INV-1: $42.00"


@function_tool(failure_error_function=record_tool_failure)
def void_invoice(invoice_id: str) -> str:
    """Void one invoice."""
    raise RuntimeError("invoice locked")


def hooks(captured: list[dict[str, Any]], **overrides: Any) -> SantatiRunHooks:
    return SantatiRunHooks(dispatch=captured.append, organization_id="org_acme", trail="agents", **overrides)


def scripted(tool_name: str, call_id: str = "call_1", args: dict[str, Any] | None = None) -> ScriptedModel:
    return ScriptedModel(
        [[function_call(tool_name, args or {"invoice_id": "inv_1"}, call_id=call_id)], [assistant_message("done")]]
    )


def test_a_successful_tool_call_is_audited() -> None:
    captured: list[dict[str, Any]] = []
    agent = Agent(name="Billing bot", model=scripted("lookup_invoice"), tools=[lookup_invoice])

    Runner.run_sync(agent, "go", hooks=hooks(captured))

    assert len(captured) == 1
    event = captured[0]
    assert event["event"] == "agent.tool_call.succeeded"
    assert event["trail"] == "agents"
    assert event["targets"] == [{"type": "tool", "id": "lookup_invoice"}]
    assert event["data"] == {"arguments": {"invoice_id": "inv_1"}}
    assert event["metadata"] == {
        "framework": "openai_agents",
        "agent": "Billing bot",
        "tool_call_id": "call_1",
    }
    assert event["actor"] == {"type": "system", "id": "Billing bot"}


def test_a_recorded_failure_is_audited_as_one() -> None:
    captured: list[dict[str, Any]] = []
    agent = Agent(name="Billing bot", model=scripted("void_invoice"), tools=[void_invoice])

    Runner.run_sync(agent, "go", hooks=hooks(captured))

    assert len(captured) == 1
    assert captured[0]["event"] == "agent.tool_call.failed"
    assert captured[0]["data"] == {
        "arguments": {"invoice_id": "inv_1"},
        "error": "RuntimeError: invoice locked",
    }


def test_a_tool_without_record_tool_failure_reports_success() -> None:
    """The SDK's default formatter swallows the exception; nothing else can see it."""
    captured: list[dict[str, Any]] = []

    @function_tool
    def unrecorded(invoice_id: str) -> str:
        """Fail without recording."""
        raise RuntimeError("boom")

    agent = Agent(name="Billing bot", model=scripted("unrecorded"), tools=[unrecorded])

    Runner.run_sync(agent, "go", hooks=hooks(captured))

    assert [event["event"] for event in captured] == ["agent.tool_call.succeeded"]
    assert "error" not in captured[0]["data"]


def test_an_actor_replaces_the_agent_actor() -> None:
    captured: list[dict[str, Any]] = []
    agent = Agent(name="Billing bot", model=scripted("lookup_invoice"), tools=[lookup_invoice])

    Runner.run_sync(agent, "go", hooks=hooks(captured, actor={"type": "user", "id": "usr_123"}))

    assert captured[0]["actor"] == {"type": "user", "id": "usr_123"}
    assert captured[0]["metadata"]["agent"] == "Billing bot"


def test_a_raising_dispatch_leaves_the_run_alone() -> None:
    def explode(event: dict[str, Any]) -> None:
        raise RuntimeError("queue is down")

    agent = Agent(name="Billing bot", model=scripted("lookup_invoice"), tools=[lookup_invoice])
    configured = SantatiRunHooks(dispatch=explode, organization_id="org_acme", trail="agents")

    result = Runner.run_sync(agent, "go", hooks=configured)

    assert result.final_output == "done"
