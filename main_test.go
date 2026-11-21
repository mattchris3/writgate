package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{name: "no args prints usage", args: nil, wantCode: 2, wantStderr: "Usage:"},
		{name: "version", args: []string{"version"}, wantCode: 0, wantStdout: "writgate "},
		{name: "help", args: []string{"help"}, wantCode: 0, wantStdout: "Commands:"},
		{name: "tools lists the payments spec", args: []string{"tools", "internal/tools/testdata/payments.openapi.json"}, wantCode: 0, wantStdout: "6 tools"},
		{name: "tools marks groups", args: []string{"tools", "internal/tools/testdata/payments.openapi.json"}, wantCode: 0, wantStdout: "readonly"},
		{name: "tools json", args: []string{"tools", "--json", "internal/tools/testdata/payments.openapi.json"}, wantCode: 0, wantStdout: `"name": "create_refund"`},
		{name: "tools without a spec", args: []string{"tools"}, wantCode: 2, wantStderr: "Usage: writgate tools"},
		{name: "tools with a missing spec", args: []string{"tools", "nope.yaml"}, wantCode: 1, wantStderr: "read spec"},
		{name: "unknown command", args: []string{"nope"}, wantCode: 2, wantStderr: `unknown command "nope"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(tt.args, &stdout, &stderr)
			if code != tt.wantCode {
				t.Errorf("exit code = %d, want %d", code, tt.wantCode)
			}
			if !strings.Contains(stdout.String(), tt.wantStdout) {
				t.Errorf("stdout = %q, want it to contain %q", stdout.String(), tt.wantStdout)
			}
			if !strings.Contains(stderr.String(), tt.wantStderr) {
				t.Errorf("stderr = %q, want it to contain %q", stderr.String(), tt.wantStderr)
			}
		})
	}
}
