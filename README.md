# writgate

*No agent action without a writ.*

writgate turns an OpenAPI spec into safe [MCP](https://modelcontextprotocol.io) tools for AI agents. Every call is checked against a permission policy and written to a tamper-evident, hash-chained audit log before it happens.

It's built for teams that own an API, such as a fintech exposing a payments API, and want agents like Claude to use it without handing over the keys to everything.

> **Status:** pre-release. v0.1 is being built. See the [v0.1 spec](docs/SPEC.md).

## How it works

| | |
|---|---|
| **01 · Point at your OpenAPI spec** | Each operation becomes one MCP tool, with a typed input schema. Writes are marked destructive. |
| **02 · Set a policy** | Allow or deny tools by name. Deny by default, and deny always wins. Denied tools are hidden from the agent. |
| **03 · Every call is written down** | An *intent* record before the request, an *outcome* record after. No record, no action. Secrets are masked. |
| **04 · Verify & recover** | `writgate audit verify` checks the hash chain and checkpoints. `writgate audit recover` resolves interrupted calls by Idempotency-Key. |

## Quickstart

Coming with v0.1:

```sh
go install github.com/mattchris3/writgate@latest

export WRITGATE_UPSTREAM_API_KEY=sk_test_...
writgate serve --spec ./openapi.json --upstream https://api.example.com --policy writgate.yaml
```

```yaml
# writgate.yaml
allow: [readonly, create_refund]
deny:  [cancel_refund]
```

### See what a spec turns into

This works today:

```sh
$ writgate tools ./openapi.json
create_charge            POST    /charges                           destructive
list_charges             GET     /charges                           readonly
get_charge               GET     /charges/{charge_id}               readonly
create_refund            POST    /charges/{charge_id}/refunds       destructive
list_refunds             GET     /charges/{charge_id}/refunds       readonly
cancel_refund            DELETE  /refunds/{refund_id}               destructive

6 tools
```

The last column shows the policy groups each tool falls in. Add `--json` for the full MCP `tools/list` result, with input and output schemas.

### Serve it to Claude Code

This also works today, over stdio. Until the policy engine lands, writgate serves only the read-only tools, and every other tool is hidden and denied.

```sh
go install github.com/mattchris3/writgate@latest

claude mcp add payments \
  -e WRITGATE_UPSTREAM_API_KEY=sk_test_local \
  -- writgate serve --spec ./examples/payments-api/openapi.json --upstream http://localhost:8000
```

The upstream API key is read only from `WRITGATE_UPSTREAM_API_KEY`. It is never a flag and never comes from the agent. Logs go to stderr, because stdout is the MCP channel. Add `-v` to log each tool's policy decision.

## Development

Requires Go 1.26 or later.

```sh
make test   # go test -race ./...
make lint   # golangci-lint run
make build  # ./writgate
```

## Licence

[Apache-2.0](LICENSE)
