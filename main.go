// Command writgate serves an OpenAPI-described API as safe MCP tools,
// enforcing a permission policy and writing a tamper-evident audit log.
package main

import (
	"fmt"
	"io"
	"os"
	"runtime/debug"

	"github.com/mattchris3/writgate/internal/version"
)

const usage = `writgate: no agent action without a writ.

Usage:
  writgate <command> [flags]

Commands:
  serve          Serve an OpenAPI spec as MCP tools over stdio
  tools <spec>   List the MCP tools an OpenAPI spec turns into
  version        Print the writgate version
  help           Show this help

Planned for v0.1 (see docs/SPEC.md):
  serve --policy / --transport http
  audit verify   Check the audit log's hash chain and checkpoints
  audit recover  Resolve calls whose result is unknown
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "version", "--version", "-v":
		fmt.Fprintln(stdout, "writgate", resolveVersion())
		return 0
	case "serve":
		return runServe(args[1:], stderr)
	case "tools":
		return runTools(args[1:], stdout, stderr)
	case "help", "--help", "-h":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "writgate: unknown command %q\n\n%s", args[0], usage)
		return 2
	}
}

// resolveVersion prefers a version set with -ldflags, then the module
// version recorded by `go install module@version`.
func resolveVersion() string {
	if version.Version != "dev" {
		return version.Version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return version.Version
}
