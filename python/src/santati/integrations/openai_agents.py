"""OpenAI Agents SDK: audit events from tool calls.

Attach :class:`SantatiRunHooks` to a run and every local tool call it makes is
audited::

    hooks = SantatiRunHooks(client, organization_id="org_acme")
    result = Runner.run_sync(agent, "go", hooks=hooks)

Because a raising tool hook fails the whole run, this module never raises: a
failed emit is logged and dropped.

Failures are opt-in. The SDK's default formatter turns a tool exception into a
result string and no hook ever sees the exception, so a failing tool is
reported as succeeded unless the tool is given
:func:`record_tool_failure` as its ``failure_error_function``::

    @function_tool(failure_error_function=record_tool_failure)
    def refund(invoice_id: str) -> str: ...

An existing tool can be retrofitted with
``agents.tool.set_function_tool_failure_error_function(tool, record_tool_failure)``.

Hosted tools (web search, file search, computer use) and handoffs never reach
the tool hooks, so they produce no events.
"""

from __future__ import annotations

from typing import TYPE_CHECKING, Any

from agents import Agent, RunContextWrapper, RunHooks, Tool, default_tool_error_function
from agents.tool_context import ToolContext

from ._core import TOOL_CALL_FAILED, TOOL_CALL_SUCCEEDED, Dispatch, ToolCallAuditor, describe_error, logger

if TYPE_CHECKING:
    from .._client import Santati
    from .._types import ActorInput

__all__ = ["SantatiRunHooks", "record_tool_failure"]

_FAILURE_ATTR = "_santati_tool_error"


class SantatiRunHooks(RunHooks[Any]):
    """Audits one event per local tool call:  ``agent.tool_call.succeeded`` or ``agent.tool_call.failed``."""

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
            "openai_agents",
            client,
            dispatch=dispatch,
            organization_id=organization_id,
            trail=trail,
            actor=actor,
            succeeded_event=succeeded_event,
            failed_event=failed_event,
        )

    async def on_tool_end(
        self,
        context: RunContextWrapper[Any],
        agent: Agent[Any],
        tool: Tool,
        result: object,
    ) -> None:
        """Audit the finished call; a failure ``record_tool_failure`` saw is reported as one."""
        if isinstance(context, ToolContext):
            tool_name = context.tool_name
            # The raw JSON string the model sent; the envelope parses it.
            arguments: Any = context.tool_arguments
            call_id: str | None = context.tool_call_id
        else:
            tool_name = tool.name
            arguments = None
            call_id = None
        await self._auditor.arecord(
            tool_name=tool_name,
            arguments=arguments,
            call_id=call_id,
            agent=agent.name,
            run_id=None,
            error=getattr(context, _FAILURE_ATTR, None),
        )


def record_tool_failure(ctx: RunContextWrapper[Any], error: Exception) -> str:
    """A tool's ``failure_error_function`` that records the failure for :class:`SantatiRunHooks`.

    Returns the same message the SDK's default would, so the model sees no
    change. Never raises: a failure here would fail the run.
    """
    try:
        setattr(ctx, _FAILURE_ATTR, describe_error(error))
    except Exception:  # noqa: BLE001 - this runs on the SDK's error path and must not raise
        # A second failure here would replace the tool's own error with this one.
        logger.debug("santati: could not record the tool failure", exc_info=True)
    return default_tool_error_function(ctx, error)
