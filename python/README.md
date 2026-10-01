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
