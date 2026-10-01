"""Pydantic AI: audit events from tool calls.

``SantatiCapability`` is a capability: attach it to an agent (or to a single
run) and every tool the agent executes is audited::

    agent = Agent("openai:gpt-5", capabilities=[SantatiCapability(client, organization_id="org_acme")])

``wrap_tool_execute`` is the one hook that sees every outcome, including the
failures ``on_tool_execute_error`` skips; the capability re-raises exactly what
the tool raised, so pydantic-ai's own retry and deferral behaviour is unchanged.
"""

from __future__ import annotations

from typing import TYPE_CHECKING, Any

from pydantic_ai import RunContext, ToolDefinition
from pydantic_ai.capabilities import AbstractCapability, WrapToolExecuteHandler
from pydantic_ai.exceptions import ApprovalRequired, CallDeferred, SkipToolExecution
from pydantic_ai.messages import ToolCallPart

from ._core import TOOL_CALL_FAILED, TOOL_CALL_SUCCEEDED, Dispatch, ToolCallAuditor

if TYPE_CHECKING:
    from .._client import Santati
    from .._types import ActorInput

__all__ = ["SantatiCapability"]


class SantatiCapability(AbstractCapability[Any]):
    """Audits one event per tool execution: ``agent.tool_call.succeeded`` or ``agent.tool_call.failed``.

    A call that never ran — deferred, awaiting approval, skipped — produces no
    event, and the audit event's failure never changes the tool's outcome.
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
            "pydantic_ai",
            client,
            dispatch=dispatch,
            organization_id=organization_id,
            trail=trail,
            actor=actor,
            succeeded_event=succeeded_event,
            failed_event=failed_event,
        )

    async def wrap_tool_execute(
        self,
        ctx: RunContext[Any],
        *,
        call: ToolCallPart,
        tool_def: ToolDefinition,
        args: dict[str, Any],
        handler: WrapToolExecuteHandler,
    ) -> Any:
        """Audit the tool's outcome and hand back exactly what the handler returned."""
        try:
            result = await handler(args)
        except (CallDeferred, ApprovalRequired, SkipToolExecution):
            # The tool did not run, so there is no outcome to audit.
            raise
        except Exception as error:
            await self._record(ctx, call, args, error=error)
            raise
        await self._record(ctx, call, args, error=None)
        return result

    async def _record(
        self, ctx: RunContext[Any], call: ToolCallPart, args: dict[str, Any], *, error: BaseException | None
    ) -> None:
        await self._auditor.arecord(
            tool_name=call.tool_name,
            arguments=args,
            call_id=call.tool_call_id,
            agent=ctx.agent.name if ctx.agent is not None else None,
            run_id=ctx.run_id,
            error=error,
        )
