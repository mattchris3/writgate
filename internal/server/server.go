// Package server serves a set of tools as an MCP server. It enforces the
// permission decision for every tool: denied tools are left out of
// tools/list, and a call that names one anyway gets a denial result
// without any upstream request (docs/SPEC.md section 4).
package server

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mattchris3/writgate/internal/tools"
	"github.com/mattchris3/writgate/internal/upstream"
)

// Decision is a permission decision for one tool. Rule names the rule that
// decided, for the audit log, e.g. "allow readonly" or "default deny".
type Decision struct {
	Allowed bool
	Rule    string
}

// DecideFunc decides whether a tool may be used. The policy engine
// (internal/policy) provides the real one.
type DecideFunc func(*tools.Tool) Decision

// ReadOnlyOnly allows read-only tools and denies everything else: the
// spec's default when no policy file is given. serve uses it until the
// policy engine is wired in.
func ReadOnlyOnly(t *tools.Tool) Decision {
	if t.ReadOnly() {
		return Decision{Allowed: true, Rule: "allow readonly"}
	}
	return Decision{Allowed: false, Rule: "default deny"}
}

// Config is what New needs.
type Config struct {
	Name    string // server name reported to clients
	Version string
	Tools   []*tools.Tool
	Client  *upstream.Client
	Decide  DecideFunc
	Logger  *slog.Logger // nil discards logs
}

// New builds the MCP server. Decisions are made once, at startup: one
// policy per instance, so they can't change while it runs.
func New(cfg Config) (*mcp.Server, error) {
	if cfg.Client == nil || cfg.Decide == nil {
		return nil, fmt.Errorf("server: Client and Decide are required")
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}

	s := mcp.NewServer(&mcp.Implementation{Name: cfg.Name, Version: cfg.Version}, &mcp.ServerOptions{Logger: logger})
	denied := map[string]Decision{}
	for _, t := range cfg.Tools {
		d := cfg.Decide(t)
		if !d.Allowed {
			denied[t.Name()] = d
			logger.Info("tool hidden by policy", "tool", t.Name(), "rule", d.Rule)
			continue
		}
		s.AddTool(t.Def, handler(cfg.Client, t))
		logger.Info("tool served", "tool", t.Name(), "rule", d.Rule)
	}
	s.AddReceivingMiddleware(denyHidden(denied, logger))
	return s, nil
}

// DeniedMessage is the result text for a call to a tool the policy denies.
func DeniedMessage(tool string) string {
	return fmt.Sprintf("Denied by writgate policy: %s is not allowed", tool)
}

func handler(c *upstream.Client, t *tools.Tool) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return c.Call(ctx, t, req.Params.Arguments), nil
	}
}

// denyHidden answers a tools/call for a hidden tool with a denial result,
// instead of the SDK's "unknown tool" protocol error, so the agent gets a
// clear reason and the attempt can be audited.
func denyHidden(denied map[string]Decision, logger *slog.Logger) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method == "tools/call" {
				if call, ok := req.(*mcp.CallToolRequest); ok {
					if d, isDenied := denied[call.Params.Name]; isDenied {
						logger.Warn("call denied by policy", "tool", call.Params.Name, "rule", d.Rule)
						return &mcp.CallToolResult{
							IsError: true,
							Content: []mcp.Content{&mcp.TextContent{Text: DeniedMessage(call.Params.Name)}},
						}, nil
					}
				}
			}
			return next(ctx, method, req)
		}
	}
}
