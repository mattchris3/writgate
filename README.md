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

## Development

Requires Go 1.26 or later.

```sh
make test   # go test -race ./...
make lint   # golangci-lint run
make build  # ./writgate
```

## Licence

[Apache-2.0](LICENSE)
