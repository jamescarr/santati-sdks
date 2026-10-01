# santati (Python)

Official Python SDK for the [Santati](https://santati.io) audit-log API: emit
audit events (one or a batch) and read them back with cursor pagination.

## Install

```sh
pip install santati
```

## Quickstart

```python
import santati

with santati.Santati("sat_sk_...", trail="billing") as client:
    # Emit one event. A missing idempotency key is generated for you, and the
    # request is retried on transport failures, 500/502/503/504 and 429.
    result = client.events.emit(
        "invoice.voided",
        organization_id="org_acme",
        actor={"type": "user", "id": "usr_123", "name": "Dana Ortiz"},
        targets=[{"type": "invoice", "id": "inv_555"}],
        data={"amount_cents": 4200, "currency": "usd"},
    )
    print(result.event.id, result.duplicate)

    # Emit a batch: one generated key per item, per-item results on 202/207.
    batch = client.events.emit_batch([{"event": "invoice.paid"}, {"event": "invoice.voided"}])
    print(batch.accepted, batch.rejected)

    # List a page, then iterate across every page.
    page = client.events.list(trail="billing", limit=50)
    for event in page.results:
        print(event.id, event.event)

    for event in client.events.iterate(trail="billing", limit=100):
        print(event.id)
```

## Client options

`Santati(api_key, *, base_url="https://api.santati.io", trail=None,
timeout_ms=10000, max_retries=2, initial_backoff_ms=250, max_backoff_ms=8000,
headers=None)`. `trail` is the default trail for emits and is never applied to
reads.

## Errors

Every failure raises a subclass of `santati.SantatiError`: `ValidationError`,
`AuthError`, `NotFoundError`, `RateLimitedError`, `ServerError`,
`TransportError` or `ApiError`. Each carries `status`, `code`, `field`,
`retry_after` and `message`; `status` is `None` for local validation failures
and transport errors.

`AuditEvent`, `EventActor` and `EventTarget` are the generated read models,
re-exported from the package root. See `docs/sdk-surface.md` in the repository
for the surface every Santati SDK implements.

## Framework integrations

`santati.integrations` turns the callbacks of the frameworks you already run
into audit events. Each integration lives behind its own extra:

| module | extra | records |
|---|---|---|
| `santati.integrations.django` | `santati[django]` | `user.logged_in`, `user.logged_out`, `user.login_failed` |
| `santati.integrations.langchain` | `santati[langchain]` | `agent.tool_call.succeeded` / `.failed` |
| `santati.integrations.openai_agents` | `santati[openai-agents]` | `agent.tool_call.succeeded` / `.failed` |
| `santati.integrations.pydantic_ai` | `santati[pydantic-ai]` | `agent.tool_call.succeeded` / `.failed` |
| `santati.integrations.claude_agent_sdk` | `santati[claude-agent-sdk]` | `agent.tool_call.succeeded` / `.failed` |

```python
from santati.integrations.django import instrument_auth


class BillingConfig(AppConfig):
    def ready(self) -> None:
        instrument_auth(client, organization_id="org_acme")
```

Every integration takes exactly one of `client` — sent inline through the
synchronous client — or `dispatch`, a callable that receives each built
envelope (a Celery task's `delay`, an RQ queue, a thread pool):

```python
handler = SantatiCallbackHandler(dispatch=record_event.delay, organization_id="org_acme")
agent.invoke({"messages": [...]}, config={"callbacks": [handler]})
```

`organization_id` and `actor` are fixed per integration instance, so a
multi-tenant app builds one per request — or passes it per run, which all four
agent frameworks support. The default agent actor is
`{"type": "system", "id": <agent name or framework slug>}`; the agent name is
also kept in `metadata.agent`. Event names are overridable per instance, which
teams with a declared action catalog need.

Nothing an integration does can raise into the framework that called it: a
failed emit is logged to the `santati.integrations` logger at WARNING and
dropped. Events are built with a fixed idempotency key, so a dispatcher's
retry replays instead of double-recording.

Per framework:

- **Django** — `instrument_auth` connects to `user_logged_in`,
  `user_logged_out` and `user_login_failed`; call it from `AppConfig.ready`.
  A failed login records the attempted identifier only
  (`metadata.username`) and never any other credential value. Pass `actor` to
  record the real end user, `context` to replace the default IP/user-agent
  context, and set an `*_event` name to `None` to leave that signal alone. A
  repeated call replaces the previous instrumentation.
- **LangChain / LangGraph** — `SantatiCallbackHandler` audits `ToolNode`,
  `create_agent` and bare `tool.invoke` calls. It runs in an executor thread
  during async runs, so emits never stall the event loop. A call LangGraph
  pauses with `interrupt()` produces no event; the resumed run audits the call
  that actually executes.
- **OpenAI Agents SDK** — `SantatiRunHooks` audits local tool calls.
  Failures are opt-in: the SDK's default formatter turns a tool exception into
  a result nothing can observe, so a failing tool is recorded as succeeded
  unless the tool is given `failure_error_function=record_tool_failure` (or
  retrofitted with `set_function_tool_failure_error_function`). Hosted tools
  and handoffs produce no events, and a nested `agent.as_tool(...)` run is
  audited only when it is given the hooks too (`as_tool(..., hooks=hooks)`).
- **Pydantic AI** — `SantatiCapability` audits every tool execution. A call
  that never ran (deferred, awaiting approval, skipped) produces no event.
- **Claude Agent SDK** — `santati_hooks(...)` returns the hook configuration for
  `ClaudeAgentOptions(hooks=...)`: it audits `PostToolUse` and
  `PostToolUseFailure`. Tools denied by a permission hook produce no events.

