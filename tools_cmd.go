package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mattchris3/writgate/internal/openapi"
	"github.com/mattchris3/writgate/internal/tools"
)

// runTools implements `writgate tools <spec>`: it prints the MCP tools a
// spec turns into, without serving them. Use it to check a spec before
// writing a policy.
func runTools(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("tools", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "print the full tools/list result as JSON")
	remote := fs.Bool("allow-remote-refs", false, "fetch $refs over HTTP(S)")
	fs.Usage = func() {
		fmt.Fprint(stderr, "Usage: writgate tools [--json] [--allow-remote-refs] <openapi-spec>\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return 2
	}

	doc, err := openapi.LoadFile(fs.Arg(0), openapi.Options{AllowRemoteRefs: *remote})
	if err != nil {
		fmt.Fprintf(stderr, "writgate: %v\n", err)
		return 1
	}
	ts, warnings, err := tools.Build(doc)
	if err != nil {
		fmt.Fprintf(stderr, "writgate: %v\n", err)
		return 1
	}
	for _, w := range warnings {
		fmt.Fprintf(stderr, "warning: %s\n", w)
	}

	if *asJSON {
		list := &mcp.ListToolsResult{Tools: make([]*mcp.Tool, 0, len(ts))}
		for _, t := range ts {
			list.Tools = append(list.Tools, t.Def)
		}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(list); err != nil {
			fmt.Fprintf(stderr, "writgate: %v\n", err)
			return 1
		}
		return 0
	}

	for _, t := range ts {
		var groups []string
		if t.ReadOnly() {
			groups = append(groups, "readonly")
		}
		if t.Destructive() {
			groups = append(groups, "destructive")
		}
		fmt.Fprintf(stdout, "%-24s %-7s %-34s %s\n", t.Name(), t.Method, t.Path, strings.Join(groups, ","))
	}
	fmt.Fprintf(stdout, "\n%d tools\n", len(ts))
	return 0
}
