"""Claude Agent SDK: audit events from tool calls.

``santati_hooks`` returns the hook configuration to hand to
``ClaudeAgentOptions``::

    from claude_agent_sdk import ClaudeAgentOptions
    from santati.integrations.claude_agent_sdk import santati_hooks

    options = ClaudeAgentOptions(hooks=santati_hooks(client, organization_id="org_acme"))

Concatenate the lists with your own if you already configure those events.

The callbacks always return ``{}``, so Claude's own behaviour is unchanged. A
call that never ran — denied by a permission hook, or rejected before
execution — fires neither hook and produces no event.
"""

from __future__ import annotations

from collections.abc import Mapping
from typing import TYPE_CHECKING, Any

from claude_agent_sdk.types import HookContext, HookEvent, HookInput, HookJSONOutput, HookMatcher

from ._core import TOOL_CALL_FAILED, TOOL_CALL_SUCCEEDED, Dispatch, ToolCallAuditor

if TYPE_CHECKING:
    from .._client import Santati
    from .._types import ActorInput

__all__ = ["santati_hooks"]

_UNKNOWN_FAILURE = "the tool call failed"


def santati_hooks(
    client: Santati | None = None,
    *,
    dispatch: Dispatch | None = None,
    organization_id: str,
    trail: str | None = None,
    actor: ActorInput | None = None,
    succeeded_event: str = TOOL_CALL_SUCCEEDED,
    failed_event: str = TOOL_CALL_FAILED,
) -> dict[HookEvent, list[HookMatcher]]:
    """Hook matchers that audit every tool call as ``agent.tool_call.succeeded`` or ``...failed``.

    Covers ``PostToolUse`` (the tool returned) and ``PostToolUseFailure`` (the
    tool errored). Both hooks share one callback: the failing one reports the
    error the SDK gives it.
    """
    auditor = ToolCallAuditor(
        "claude_agent_sdk",
        client,
        dispatch=dispatch,
        organization_id=organization_id,
        trail=trail,
        actor=actor,
        succeeded_event=succeeded_event,
        failed_event=failed_event,
    )

    async def callback(input_data: HookInput, tool_use_id: str | None, context: HookContext) -> HookJSONOutput:
        data: Mapping[str, Any] = input_data
        hook_event = data.get("hook_event_name")
        if hook_event == "PostToolUse":
            error: BaseException | str | None = None
        elif hook_event == "PostToolUseFailure":
            error = data.get("error") or _UNKNOWN_FAILURE
        else:
            return {}
        await auditor.arecord(
            tool_name=data["tool_name"],
            arguments=data["tool_input"],
            call_id=tool_use_id or data.get("tool_use_id"),
            agent=data.get("agent_type"),
            run_id=data["session_id"],
            error=error,
        )
        return {}

    return {"PostToolUse": [HookMatcher(hooks=[callback])], "PostToolUseFailure": [HookMatcher(hooks=[callback])]}
