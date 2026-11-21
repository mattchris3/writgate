package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mattchris3/writgate/internal/openapi"
	"github.com/mattchris3/writgate/internal/server"
	"github.com/mattchris3/writgate/internal/tools"
	"github.com/mattchris3/writgate/internal/upstream"
)

// apiKeyEnv holds the upstream API key. It is only ever read from the
// environment, so it never appears in argv, shell history or a config file.
const apiKeyEnv = "WRITGATE_UPSTREAM_API_KEY"

// runServe implements `writgate serve`: serve an OpenAPI spec's operations
// as MCP tools over stdio. stdout is the MCP channel, so every log line
// goes to stderr.
func runServe(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	spec := fs.String("spec", "", "OpenAPI 3.0/3.1 spec file (required)")
	up := fs.String("upstream", "", "base URL of the API, e.g. https://api.example.com (required)")
	transport := fs.String("transport", "stdio", "MCP transport: stdio")
	policyFile := fs.String("policy", "", "policy file (coming soon; until then only readonly tools are served)")
	remote := fs.Bool("allow-remote-refs", false, "fetch $refs over HTTP(S)")
	timeout := fs.Duration("timeout", 30*time.Second, "timeout for each upstream request")
	verbose := fs.Bool("v", false, "log each tool decision to stderr")
	fs.Usage = func() {
		fmt.Fprintf(stderr, "Usage: %s=... writgate serve --spec FILE --upstream URL [flags]\n\n", apiKeyEnv)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *spec == "" || *up == "" || fs.NArg() != 0 {
		fs.Usage()
		return 2
	}
	if *transport != "stdio" {
		fmt.Fprintf(stderr, "writgate: transport %q is not available yet; use stdio\n", *transport)
		return 2
	}
	if *policyFile != "" {
		fmt.Fprintln(stderr, "writgate: --policy is not available yet; without it only readonly tools are served")
		return 2
	}

	level := slog.LevelWarn
	if *verbose {
		level = slog.LevelInfo
	}
	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: level}))

	srv, err := buildServer(*spec, *up, *remote, *timeout, logger)
	if err != nil {
		fmt.Fprintf(stderr, "writgate: %v\n", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := srv.Run(ctx, &mcp.StdioTransport{}); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintf(stderr, "writgate: %v\n", err)
		return 1
	}
	return 0
}

func buildServer(spec, up string, remoteRefs bool, timeout time.Duration, logger *slog.Logger) (*mcp.Server, error) {
	key := os.Getenv(apiKeyEnv)
	if key == "" {
		logger.Warn("no upstream API key set; requests are sent without Authorization", "env", apiKeyEnv)
	}
	doc, err := openapi.LoadFile(spec, openapi.Options{AllowRemoteRefs: remoteRefs, Logger: logger})
	if err != nil {
		return nil, err
	}
	ts, warnings, err := tools.Build(doc)
	if err != nil {
		return nil, err
	}
	for _, w := range warnings {
		logger.Warn(w)
	}
	client, err := upstream.New(up, upstream.Options{
		APIKey:    key,
		Timeout:   timeout,
		UserAgent: "writgate/" + resolveVersion(),
	})
	if err != nil {
		return nil, err
	}
	return server.New(server.Config{
		Name:    "writgate",
		Version: resolveVersion(),
		Tools:   ts,
		Client:  client,
		Decide:  server.ReadOnlyOnly, // replaced by the policy engine
		Logger:  logger,
	})
}
