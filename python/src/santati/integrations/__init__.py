"""Framework integrations: audit events from frameworks you already run.

Each submodule is an adapter behind its own extra, and none of them is
imported here — ``import santati.integrations`` never needs a framework
installed:

- ``santati.integrations.django`` (``santati[django]``) turns Django's login
  and logout signals into events; ``instrument_auth``.
- ``santati.integrations.langchain`` (``santati[langchain]``) audits tool
  calls in LangChain and LangGraph; ``SantatiCallbackHandler``.
- ``santati.integrations.openai_agents`` (``santati[openai-agents]``) audits
  tool calls in the OpenAI Agents SDK's ``Runner``; ``SantatiRunHooks``.
- ``santati.integrations.pydantic_ai`` (``santati[pydantic-ai]``) audits tool
  calls in Pydantic AI agents; ``SantatiCapability``.
- ``santati.integrations.claude_agent_sdk`` (``santati[claude-agent-sdk]``)
  audits tool calls in Claude Agent SDK sessions; ``santati_hooks``.

Every integration is given a ``client`` to emit through directly, or a
``dispatch`` callable to hand each built envelope to (a Celery task's
``delay``, an ``rq`` queue, a thread pool). Exactly one of the two.
"""

from __future__ import annotations

from ._core import (
    TOOL_CALL_FAILED,
    TOOL_CALL_SUCCEEDED,
    USER_LOGGED_IN,
    USER_LOGGED_OUT,
    USER_LOGIN_FAILED,
    Dispatch,
)

__all__ = [
    "TOOL_CALL_FAILED",
    "TOOL_CALL_SUCCEEDED",
    "USER_LOGGED_IN",
    "USER_LOGGED_OUT",
    "USER_LOGIN_FAILED",
    "Dispatch",
]
