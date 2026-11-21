# Mock payments API

A small FastAPI service with charges and refunds. It's the API writgate's demo puts behind an agent, so it behaves like a real payments API where it matters for safety: money in minor units, required idempotency keys, refunds that stay pending for a while, and a lookup by idempotency key for writgate's audit recovery.

It is a mock. Nothing here moves money.

## Run it

Requires Python 3.13 and [uv](https://docs.astral.sh/uv/).

```sh
cd examples/payments-api
uv sync
PAYMENTS_API_KEY=sk_test_local uv run uvicorn --factory payments_api.app:app_from_env --reload
```

Then open http://localhost:8000/docs.

```sh
curl -s localhost:8000/charges \
  -H 'Authorization: Bearer sk_test_local' \
  -H 'Idempotency-Key: demo-1' \
  -H 'Content-Type: application/json' \
  -d '{"amount": 1250, "customer_id": "cus_42"}'
```

## Endpoints

| Tool name (operationId) | Call | Notes |
|---|---|---|
| `create_charge` | `POST /charges` | Succeeds at once. `cus_test_declined` and `cus_test_insufficient_funds` get a 402. |
| `list_charges` | `GET /charges` | Filter by `customer_id`, `status` or `idempotency_key`. |
| `get_charge` | `GET /charges/{charge_id}` | Includes `amount_refunded`. |
| `create_refund` | `POST /charges/{charge_id}/refunds` | Omit `amount` to refund what's left. Over-refunding is a 409. |
| `list_refunds` | `GET /charges/{charge_id}/refunds` | Filter by `idempotency_key`. |
| `cancel_refund` | `DELETE /refunds/{refund_id}` | Only while pending. Safe to retry. |

## Rules

- **Money** is an integer in minor units (1250 = £12.50), with a 3-letter currency that defaults to GBP.
- **Auth** is one static key, sent as `Authorization: Bearer <key>`, set with `PAYMENTS_API_KEY`.
- **Idempotency-Key** is required on every POST.
  - The same key and body replay the first response, with `Idempotent-Replayed: true`. A declined charge replays as a decline.
  - The same key with a different body, or on another endpoint, is a 409.
  - Replays last 24 hours. The key is also stored on the object it created, so `?idempotency_key=` lookups keep working after that.
- **Refunds** are `pending` for 30 seconds, then `succeeded`. A cancelled refund frees its amount again.
- **IDs** are prefixed: `ch_`, `re_`, `cus_`.
- **Lists** are newest first, with `limit` (1–100, default 20), a `starting_after` cursor and `has_more`.

## Storage

Week 1 keeps everything in memory, so a restart clears it. The routes only talk to two small interfaces in `payments_api/store.py`. Week 2 moves charges and refunds to PostgreSQL and replay records to Redis, with Docker Compose for local runs.

## The OpenAPI spec

[`openapi.json`](openapi.json) is generated from the code and committed, so writgate can read it without running the app. After changing a route or model, regenerate it:

```sh
uv run python scripts/export_openapi.py
```

CI fails if the committed file is out of date.

## Checks

```sh
uv run pytest
uv run ruff check . && uv run ruff format --check .
```
