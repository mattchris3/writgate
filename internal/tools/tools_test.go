package tools

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mattchris3/writgate/internal/openapi"
)

func load(t *testing.T, file string) []*Tool {
	t.Helper()
	doc, err := openapi.LoadFile("testdata/"+file, openapi.Options{})
	if err != nil {
		t.Fatal(err)
	}
	tools, _, err := Build(doc)
	if err != nil {
		t.Fatal(err)
	}
	return tools
}

func byName(t *testing.T, tools []*Tool) map[string]*Tool {
	t.Helper()
	m := map[string]*Tool{}
	for _, tl := range tools {
		m[tl.Name()] = tl
	}
	return m
}

// schemaOf round-trips a schema through JSON so tests compare plain values.
func schemaOf(t *testing.T, v any) map[string]any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func propNames(s map[string]any) []string {
	var names []string
	for k := range s["properties"].(map[string]any) {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

func requiredOf(s map[string]any) []string {
	var out []string
	if r, ok := s["required"].([]any); ok {
		for _, x := range r {
			out = append(out, x.(string))
		}
	}
	return out
}

func TestPaymentsSpec(t *testing.T) {
	tools := load(t, "payments.openapi.json")
	got := byName(t, tools)

	tests := []struct {
		name        string
		method      string
		path        string
		readOnly    bool
		destructive bool
		idempotent  bool
		props       []string
		required    []string
		hasOutput   bool
	}{
		{"create_charge", "POST", "/charges", false, true, false,
			[]string{"Idempotency-Key", "amount", "currency", "customer_id", "description"},
			[]string{"amount", "customer_id"}, true},
		{"list_charges", "GET", "/charges", true, false, false,
			[]string{"customer_id", "limit", "status"}, nil, true},
		{"get_charge", "GET", "/charges/{charge_id}", true, false, false,
			[]string{"charge_id"}, []string{"charge_id"}, true},
		{"create_refund", "POST", "/charges/{charge_id}/refunds", false, true, false,
			[]string{"Idempotency-Key", "amount", "charge_id", "reason"}, []string{"charge_id"}, true},
		{"list_refunds", "GET", "/charges/{charge_id}/refunds", true, false, false,
			[]string{"charge_id"}, []string{"charge_id"}, true},
		{"cancel_refund", "DELETE", "/refunds/{refund_id}", false, true, true,
			[]string{"refund_id"}, []string{"refund_id"}, false},
	}
	if len(tools) != len(tests) {
		t.Fatalf("got %d tools, want %d", len(tools), len(tests))
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tl, ok := got[tt.name]
			if !ok {
				t.Fatalf("no tool named %q", tt.name)
			}
			if tl.Method != tt.method || tl.Path != tt.path {
				t.Errorf("call = %s %s, want %s %s", tl.Method, tl.Path, tt.method, tt.path)
			}
			if tl.ReadOnly() != tt.readOnly || tl.Destructive() != tt.destructive {
				t.Errorf("readOnly=%v destructive=%v, want %v %v", tl.ReadOnly(), tl.Destructive(), tt.readOnly, tt.destructive)
			}
			if tl.Def.Annotations.IdempotentHint != tt.idempotent {
				t.Errorf("idempotent = %v, want %v", tl.Def.Annotations.IdempotentHint, tt.idempotent)
			}
			in := schemaOf(t, tl.Def.InputSchema)
			if p := propNames(in); !reflect.DeepEqual(p, tt.props) {
				t.Errorf("properties = %v, want %v", p, tt.props)
			}
			if r := requiredOf(in); !reflect.DeepEqual(r, tt.required) {
				t.Errorf("required = %v, want %v", r, tt.required)
			}
			if in["additionalProperties"] != false {
				t.Error("additionalProperties should be false")
			}
			if (tl.Def.OutputSchema != nil) != tt.hasOutput {
				t.Errorf("has output schema = %v, want %v", tl.Def.OutputSchema != nil, tt.hasOutput)
			}
			if !strings.HasSuffix(tl.Def.Description, "("+tt.method+" "+tt.path+")") {
				t.Errorf("description %q should end with the call", tl.Def.Description)
			}
		})
	}

	// The FastAPI auto-titles are gone everywhere, including on a collapsed
	// anyOf [ChargeStatus, null].
	b, _ := json.Marshal(got["list_charges"].Def.InputSchema)
	if strings.Contains(string(b), `"title"`) {
		t.Errorf("input schema still has a title: %s", b)
	}
	if got["create_charge"].Def.Annotations.Title != "Create Charge" {
		t.Errorf("title = %q", got["create_charge"].Def.Annotations.Title)
	}
	if d := got["create_charge"].Def.Description; d != "Create Charge. Charge a customer's payment method. Amounts are in minor units (pence). (POST /charges)" {
		t.Errorf("description = %q", d)
	}
	if a := got["create_charge"].Args["amount"]; a != (Arg{In: InBodyField, Name: "amount"}) {
		t.Errorf("amount binding = %+v", a)
	}
	if a := got["create_charge"].Args["Idempotency-Key"]; a != (Arg{In: InHeader, Name: "Idempotency-Key"}) {
		t.Errorf("Idempotency-Key binding = %+v", a)
	}
}

func TestEdgeCases(t *testing.T) {
	doc, err := openapi.LoadFile("testdata/edge-cases.openapi.yaml", openapi.Options{})
	if err != nil {
		t.Fatal(err)
	}
	tools, warnings, err := Build(doc)
	if err != nil {
		t.Fatal(err)
	}
	got := byName(t, tools)

	wantWarnings := []string{
		"skipped POST /upload: request body has no JSON media type",
		`skipped GET /session: required cookie parameter "sid" (cookie parameters are not supported)`,
	}
	if !reflect.DeepEqual(warnings, wantWarnings) {
		t.Errorf("warnings =\n%q\nwant\n%q", warnings, wantWarnings)
	}

	t.Run("credentials and cookies are never exposed", func(t *testing.T) {
		in := schemaOf(t, got["fetchNote"].Def.InputSchema)
		if p := propNames(in); !reflect.DeepEqual(p, []string{"note_id"}) {
			t.Errorf("properties = %v, want only note_id", p)
		}
	})

	t.Run("operation parameter overrides path-level one", func(t *testing.T) {
		in := schemaOf(t, got["fetchNote"].Def.InputSchema)
		id := in["properties"].(map[string]any)["note_id"].(map[string]any)
		if id["type"] != "string" {
			t.Errorf("note_id type = %v, want string", id["type"])
		}
	})

	t.Run("OpenAPI 3.0 forms become JSON Schema", func(t *testing.T) {
		out := schemaOf(t, got["fetchNote"].Def.OutputSchema)
		props := out["properties"].(map[string]any)
		if _, ok := props["title"]; !ok {
			t.Error("a field named title must survive title-stripping")
		}
		if _, ok := props["text"].(map[string]any)["title"]; ok {
			t.Error("title keyword should be dropped")
		}
		if _, ok := props["title"].(map[string]any)["nullable"]; ok {
			t.Error("nullable should be dropped")
		}
		score := props["score"].(map[string]any)
		if score["exclusiveMinimum"] != float64(0) || score["minimum"] != nil {
			t.Errorf("score = %v, want exclusiveMinimum 0 and no minimum", score)
		}
	})

	t.Run("x-writgate-name and x-writgate-destructive", func(t *testing.T) {
		tl, ok := got["replace-note"]
		if !ok {
			t.Fatal("no tool named replace-note")
		}
		if tl.Destructive() || !tl.Def.Annotations.IdempotentHint {
			t.Errorf("destructive=%v idempotent=%v, want false true", tl.Destructive(), tl.Def.Annotations.IdempotentHint)
		}
		in := schemaOf(t, tl.Def.InputSchema)
		if r := requiredOf(in); !reflect.DeepEqual(r, []string{"note_id", "text"}) {
			t.Errorf("required = %v", r)
		}
	})

	t.Run("body nests on a name clash", func(t *testing.T) {
		tl := got["patch_notes_note_id"]
		if tl == nil {
			t.Fatal("no tool patch_notes_note_id")
		}
		in := schemaOf(t, tl.Def.InputSchema)
		if p := propNames(in); !reflect.DeepEqual(p, []string{"body", "note_id"}) {
			t.Errorf("properties = %v", p)
		}
		if tl.Args["body"].In != InBody {
			t.Errorf("body binding = %+v", tl.Args["body"])
		}
		if r := requiredOf(in); !reflect.DeepEqual(r, []string{"note_id"}) {
			t.Errorf("required = %v (an optional body is not required)", r)
		}
	})

	t.Run("a non-object body nests under body", func(t *testing.T) {
		tl := got["post_notes"]
		if tl == nil {
			t.Fatal("no tool post_notes")
		}
		in := schemaOf(t, tl.Def.InputSchema)
		if r := requiredOf(in); !reflect.DeepEqual(r, []string{"body"}) {
			t.Errorf("required = %v", r)
		}
	})
}

func TestToolNames(t *testing.T) {
	tests := []struct {
		spec string
		want string
	}{
		{`operationId: get_charge_charges__charge_id__get`, "get_charge"},
		{`operationId: getCharge`, "getCharge"},
		{`operationId: "billing.get charge"`, "billing_get_charge"},
		{`x-writgate-name: refund`, "refund"},
		{``, "get_charges_charge_id"},
		{`operationId: ` + strings.Repeat("a", 70), strings.Repeat("a", 64)},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			tools, _, err := buildYAML(t, "/charges/{charge_id}", "get", tt.spec)
			if err != nil {
				t.Fatal(err)
			}
			if tools[0].Name() != tt.want {
				t.Errorf("name = %q, want %q", tools[0].Name(), tt.want)
			}
		})
	}
}

func TestBuildErrors(t *testing.T) {
	t.Run("invalid x-writgate-name", func(t *testing.T) {
		_, _, err := buildYAML(t, "/a", "get", `x-writgate-name: "has space"`)
		if err == nil || !strings.Contains(err.Error(), "x-writgate-name") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("duplicate tool names", func(t *testing.T) {
		doc, err := openapi.Load([]byte(`openapi: 3.1.0
info: {title: t, version: "1"}
paths:
  /a:
    get: {operationId: same, responses: {"200": {description: ok}}}
  /b:
    get: {operationId: same, responses: {"200": {description: ok}}}
`), openapi.Options{})
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = Build(doc)
		if err == nil || !strings.Contains(err.Error(), `"same" is used by both GET /a and GET /b`) {
			t.Errorf("err = %v", err)
		}
	})
}

func TestArgumentValidation(t *testing.T) {
	tl := byName(t, load(t, "payments.openapi.json"))["create_charge"]
	tests := []struct {
		name string
		args map[string]any
		ok   bool
	}{
		{"valid", map[string]any{"amount": 1250, "customer_id": "cus_1"}, true},
		{"missing required", map[string]any{"amount": 1250}, false},
		{"amount must be positive", map[string]any{"amount": 0, "customer_id": "cus_1"}, false},
		{"unknown argument", map[string]any{"amount": 1, "customer_id": "cus_1", "Authorization": "x"}, false},
		{"bad currency", map[string]any{"amount": 1, "customer_id": "cus_1", "currency": "gbp"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var args map[string]any
			b, _ := json.Marshal(tt.args)
			_ = json.Unmarshal(b, &args)
			err := tl.Input.Validate(args)
			if (err == nil) != tt.ok {
				t.Errorf("Validate() err = %v, want ok=%v", err, tt.ok)
			}
		})
	}
}

// TestServedOverMCP registers every tool on a go-sdk server and lists them
// through a client, which proves the SDK accepts the generated schemas
// (including array output schemas).
func TestServedOverMCP(t *testing.T) {
	tools := load(t, "payments.openapi.json")
	server := mcp.NewServer(&mcp.Implementation{Name: "writgate-test", Version: "0"}, nil)
	for _, tl := range tools {
		server.AddTool(tl.Def, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{}, nil
		})
	}
	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cs.Close() }()
	list, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Tools) != len(tools) {
		t.Errorf("listed %d tools, want %d", len(list.Tools), len(tools))
	}
}

// buildYAML builds the tools for a one-operation spec; opYAML is extra
// operation keys, one per line.
func buildYAML(t *testing.T, path, method, opYAML string) ([]*Tool, []string, error) {
	t.Helper()
	spec := "openapi: 3.1.0\ninfo: {title: t, version: \"1\"}\npaths:\n  " + path + ":\n    " + method + ":\n"
	if opYAML != "" {
		spec += "      " + opYAML + "\n"
	}
	spec += "      responses: {\"200\": {description: ok}}\n"
	doc, err := openapi.Load([]byte(spec), openapi.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return Build(doc)
}
