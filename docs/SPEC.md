# writgate v0.1 spec

*No agent action without a writ.*

writgate reads an OpenAPI spec and serves each operation as an MCP tool. It enforces a permission policy and writes a tamper-evident, hash-chained audit log of every call. It's for teams that own an API (e.g. a fintech payments API) and want AI agents to use it safely.

This spec was decided through a set of planning tickets (numbered below). Agreed with Matthew, 17–21 Nov 2026.

---

## 1. Identity (ticket 01)

- Name **writgate**. Repo `github.com/mattchris3/writgate`, Go module of the same path, binary `writgate`.

## 2. Foundations (02, 03)

- MCP: official `modelcontextprotocol/go-sdk`, **pinned to v1.8.x** (v1.9 changes stateless session behaviour). Permissions and audit live in our own small middleware, behind our own interface.
- OpenAPI: `pb33f/libopenapi` (pinned) plus `libopenapi-validator`. Accept **OpenAPI 3.0.x and 3.1.x**. Reject Swagger 2.0 with "convert to 3.x first". 3.2 is out of scope. Remote `$ref`s are off by default and enabled with a flag.
- Go 1.26 (required by libopenapi).

## 3. Operation → tool (04)

- One operation becomes one tool.
- **Name:** `x-writgate-name`, or else the operationId (with FastAPI's `_<path>_<method>` suffix stripped), or else `<method>_<path>`. Allowed characters `[A-Za-z0-9_-]`, up to 64.
- **Description:** summary, then description, then the call, e.g. "(POST /charges)". `annotations.title` is the summary.
- **Annotations:** GET/HEAD get `readOnlyHint`; PUT/DELETE get `idempotentHint`. Every write is `destructiveHint: true` unless `x-writgate-destructive: false`.
- **Input schema:** flat. Path, query and header params plus the body's top-level fields go in one object; the body is nested under `body` only on a clash or when it isn't an object. Required fields are marked, `additionalProperties: false`, all `$ref`s are inlined, FastAPI's auto-titles are dropped, and `anyOf [X, null]` collapses to X. Cookie params aren't supported. Auth/security headers are never exposed.
- `Idempotency-Key` is an ordinary argument the agent supplies.
- Arguments are validated before any request is sent; a failure is a tool error, with plain-language messages.
- **2xx:** the JSON body as text. If there's a response schema, it becomes the `outputSchema` and the body is also returned as `structuredContent`. 204 has a fixed message.
- **4xx/5xx and unreachable upstream:** `isError: true`, "HTTP <code> <text> from <METHOD> <path>: <body>". FastAPI 422 is flattened to `loc: msg; …`.

## 4. Permissions (05)

- A YAML file passed with `--policy writgate.yaml`, containing `allow:` and `deny:` lists.
- Rules match **tool names** with `*` wildcards, plus the groups `readonly` and `destructive`.
- **Deny by default.** With no policy file, only `readonly` tools are allowed. **Deny wins** over allow, whatever the order.
- Denied tools are left out of `tools/list`. A forced call returns "Denied by writgate policy: <tool> is not allowed" and is audited.
- One policy per instance. The upstream API key comes only from an env var.

## 5. Transport (07)

- **stdio** by default, for Claude Code and Claude Desktop.
- **Streamable HTTP**, behind `--transport http`: `Stateless: true`, `JSONResponse: true`, `PropagateRequestCancellation: true`, plus the Origin check. The legacy SSE transport is not built.
- HTTP auth is a static bearer key, checked with `auth.RequireBearerToken` (`AllowMissingExpiration: true`). There's no inbound auth on stdio.
- The upstream key comes from an env var (Secret Manager on Cloud Run) and is never passed through from the client.

## 6. Audit log (06)

- **Format and destination:** JSON Lines through a **sink interface**. v0.1 ships a file sink (`--audit-log path`) and a stdout sink. On Cloud Run, stdout goes to Cloud Logging. A Postgres sink (`--audit-postgres`) comes in week 3.
- **Record fields:** time, call ID, type (`intent` / `outcome` / `denied` / `recovered`), caller (verified identity over HTTP, `local` over stdio), tool, arguments, policy decision and matched rule, upstream method and URL, Idempotency-Key, HTTP status, duration, and the response body (capped at 16 KB).
- **Redaction:** the API key and auth headers are never written. Values of fields marked `format: password` or `x-writgate-sensitive` become `"***"` everywhere: in arguments, the URL and the response body. A test proves a marked value never appears anywhere in a record.
- **Two records per allowed call:** an intent record before the upstream request, and an outcome record after, linked by call ID. A denied call gets one record.
- **Fail closed:**
  - If the intent record can't be written, the call is refused.
  - If the outcome record can't be written, writgate retries, then returns an error to the agent and raises an operator-visible error. The orphaned intent means "result unknown".
- **Recovery:**
  - On startup and with `writgate audit recover`, writgate finds orphaned intents.
  - It looks each one up upstream by Idempotency-Key (read-only) and writes a `recovered` outcome.
  - While a call is unresolved, a new attempt at the same action under a different key is refused.
  - The upstream record is the source of truth; the key is the shared reference used to find it.
- **Tamper evidence:**
  - **Hash chain:** each record carries the hash of the previous one.
  - **Head-hash checkpoints:** record number + hash + time, appended to a separate append-only destination (stderr / Cloud Logging with a retention lock; Postgres later). By default one is written every 100 records or 60 s, plus one at shutdown; all configurable.
  - **Verify:** `writgate audit verify` checks the chain and every checkpoint, names the tampered range, and lists "result unknown" calls.

## 7. Demo target: payments API (08)

A FastAPI (async) app with PostgreSQL and Redis, built separately (P1.12).

- **Endpoints**, with explicit operationIds, summaries and response models:
  - `create_charge`: POST /charges
  - `list_charges`: GET /charges
  - `get_charge`: GET /charges/{charge_id}
  - `create_refund`: POST /charges/{charge_id}/refunds
  - `list_refunds`: GET /charges/{charge_id}/refunds
  - `cancel_refund`: DELETE /refunds/{refund_id}
- **Behaviour:** charges succeed immediately, and a few test customer IDs fail them. Refunds are `pending` for about 30 s and then settle. Over-refunding is rejected.
- **Money:** integers in minor units, a 3-letter currency, GBP by default.
- **Idempotency:** `Idempotency-Key` is **required** on every POST. The same key with the same body replays the response; with a different body it returns 409. Keys are kept in Redis for 24 h and also stored on the created object.
- **Lookup by key:** `GET /charges?idempotency_key=…` and the refunds equivalent, for writgate's recovery.
- **The rest:** a static bearer key; prefixed IDs (`ch_`, `re_`, `cus_`); `limit` + `starting_after` pagination, newest first.
- Ledger endpoints come in week 3.

## 8. Definition of done (09)

- **Demo:** Claude Code with writgate over stdio. HTTP mode is tested locally. The Cloud Run deploy is week 3.
- **Demo story:** "A support agent fixes a duplicate charge."
  1. With `allow: [readonly, create_refund]`, the agent finds the duplicate and refunds it.
  2. `create_charge` and `cancel_refund` are hidden, and a forced call is denied and logged.
  3. `audit verify` passes; a hand-edited line is then caught and its range named.
  4. writgate is killed mid-refund, and the refund is recovered on restart.
- **Tests:**
  - Unit tests for mapping, policy, redaction (the "never appears" test), the chain with checkpoints and verify, and recovery.
  - One end-to-end test against the real payments API (Docker Compose: API + Postgres + Redis), in GitHub Actions with a linter.
  - No coverage-percentage target.
- **Docs** (in the repo: README + `docs/`):
  - A 60-second quickstart and the demo GIF.
  - An **architecture hero in the meuze.ai style**: numbered 01–04 blocks (Point at your OpenAPI spec → Set a policy → Every call is written down → Verify & recover), dark teal (~#163847), code-style panels, and status chips such as "policy: deny wins" and "chain verified". Mermaid detail diagrams go lower down.
  - A short threat model. Say "tamper-evident, hash-chained audit log", never "blockchain".
- **Release:** a `v0.1.0` tag and `go install github.com/mattchris3/writgate@v0.1.0`. Binaries and a Docker image come in week 3.
- **Target:** 1 Dec 2026, a target rather than a cutoff. It ships when all of the above is true.

## Later (not v0.1)

- **Week 3:** undo / compensating actions, the "needs approval" state, argument-value limits (e.g. refund caps), the Postgres audit sink, the ledger module and endpoints, the Cloud Run deploy, binaries and a Docker image.
- **Later still:**
  - The retail POS demo and a GitHub Pages docs site.
  - Extra README diagrams beyond the hero.
  - OAuth (needed for remote Desktop/claude.ai use with real auth).
  - Auto-filling the Idempotency-Key.
  - Matching rules by path, method or tag.
