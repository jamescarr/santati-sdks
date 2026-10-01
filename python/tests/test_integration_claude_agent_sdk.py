"""The Claude Agent SDK hooks, driven the way the SDK drives them.

The CLI is not involved: the hooks are called directly with the inputs the SDK
builds, which is the same code path a real ``query()`` uses.
"""

from __future__ import annotations

import asyncio
from typing import Any

from claude_agent_sdk.types import HookContext, HookJSONOutput

from santati.integrations.claude_agent_sdk import santati_hooks

CONTEXT: HookContext = {"signal": None}
POST_TOOL_USE = {
    "hook_event_name": "PostToolUse",
    "session_id": "s1",
    "transcript_path": "/t",
    "cwd": "/w",
    "tool_name": "Bash",
    "tool_input": {"command": "ls"},
    "tool_response": {"stdout": "x"},
    "tool_use_id": "toolu_1",
}


def callback(captured: list[dict[str, Any]], **overrides: Any) -> Any:
    hooks = santati_hooks(dispatch=captured.append, organization_id="org_acme", trail="agents", **overrides)
    (matcher,) = hooks["PostToolUse"]
    return matcher.hooks[0]


def run(hook: Any, input_data: dict[str, Any], tool_use_id: str | None = "toolu_1") -> HookJSONOutput:
    return asyncio.run(hook(input_data, tool_use_id, CONTEXT))


def test_a_successful_tool_call_is_audited() -> None:
    captured: list[dict[str, Any]] = []

    assert run(callback(captured), POST_TOOL_USE) == {}

    assert len(captured) == 1
    event = captured[0]
    assert event["event"] == "agent.tool_call.succeeded"
    assert event["trail"] == "agents"
    assert event["targets"] == [{"type": "tool", "id": "Bash"}]
    assert event["data"] == {"arguments": {"command": "ls"}}
    assert event["metadata"] == {"framework": "claude_agent_sdk", "tool_call_id": "toolu_1", "run_id": "s1"}
    assert event["actor"] == {"type": "system", "id": "claude_agent_sdk"}


def test_the_failure_hook_shares_the_callback() -> None:
    captured: list[dict[str, Any]] = []
    hooks = santati_hooks(dispatch=captured.append, organization_id="org_acme", trail="agents")
    assert hooks["PostToolUseFailure"][0].hooks == hooks["PostToolUse"][0].hooks

    failure = {**POST_TOOL_USE, "hook_event_name": "PostToolUseFailure", "error": "Exit code 1"}
    assert run(hooks["PostToolUseFailure"][0].hooks[0], failure) == {}

    assert len(captured) == 1
    assert captured[0]["event"] == "agent.tool_call.failed"
    assert captured[0]["data"]["error"] == "Exit code 1"


def test_an_unhandled_hook_event_is_ignored() -> None:
    captured: list[dict[str, Any]] = []

    assert run(callback(captured), {**POST_TOOL_USE, "hook_event_name": "PreCompact"}) == {}

    assert captured == []


def test_an_agent_type_is_recorded_when_present() -> None:
    captured: list[dict[str, Any]] = []

    run(callback(captured), {**POST_TOOL_USE, "agent_type": "Explore"})

    assert captured[0]["metadata"]["agent"] == "Explore"
    assert captured[0]["actor"] == {"type": "system", "id": "Explore"}


def test_the_second_hook_argument_supplies_the_call_id() -> None:
    captured: list[dict[str, Any]] = []
    without_key = {key: value for key, value in POST_TOOL_USE.items() if key != "tool_use_id"}

    run(callback(captured), without_key, tool_use_id="toolu_2")

    assert captured[0]["metadata"]["tool_call_id"] == "toolu_2"


def test_a_raising_dispatch_leaves_the_hook_alone() -> None:
    def explode(event: dict[str, Any]) -> None:
        raise RuntimeError("queue is down")

    hooks = santati_hooks(dispatch=explode, organization_id="org_acme", trail="agents")

    assert run(hooks["PostToolUse"][0].hooks[0], POST_TOOL_USE) == {}
