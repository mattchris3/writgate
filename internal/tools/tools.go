// Package tools turns the operations of an OpenAPI document into MCP tool
// definitions: one operation becomes one tool. It records, for each tool
// argument, where it goes in the HTTP request, so a handler can rebuild the
// call later. The rules are in docs/SPEC.md, section 3.
package tools

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pb33f/go-yaml"
	v3 "github.com/pb33f/libopenapi/datamodel/high/v3"
	"github.com/pb33f/libopenapi/orderedmap"
)

// Location says where a tool argument goes in the HTTP request.
type Location string

// Argument locations.
const (
	InPath      Location = "path"
	InQuery     Location = "query"
	InHeader    Location = "header"
	InBodyField Location = "body-field" // a top-level field of a JSON object body
	InBody      Location = "body"       // the whole JSON body
)

// Arg binds one tool argument to its place in the HTTP request.
type Arg struct {
	In   Location
	Name string // the name in the request: the parameter or body field name
}

// Tool is one operation served as an MCP tool.
type Tool struct {
	Def    *mcp.Tool
	Method string // upper case, e.g. "POST"
	Path   string // the path template, e.g. "/charges/{charge_id}"
	Args   map[string]Arg
	// Input is the resolved input schema, used to validate arguments before
	// any request is sent.
	Input *jsonschema.Resolved
}

// Name is the tool's name.
func (t *Tool) Name() string { return t.Def.Name }

// ReadOnly reports whether the tool is in the policy group "readonly".
func (t *Tool) ReadOnly() bool { return t.Def.Annotations.ReadOnlyHint }

// Destructive reports whether the tool is in the policy group "destructive".
func (t *Tool) Destructive() bool {
	a := t.Def.Annotations
	return !a.ReadOnlyHint && (a.DestructiveHint == nil || *a.DestructiveHint)
}

// Extension keys writgate reads from an operation.
const (
	extName        = "x-writgate-name"
	extDestructive = "x-writgate-destructive"
)

var (
	validName   = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	nonWord     = regexp.MustCompile(`\W`)
	badChars    = regexp.MustCompile(`[^A-Za-z0-9_-]`)
	underscores = regexp.MustCompile(`__+`)
)

// Build maps every operation in doc to a tool. An operation writgate can't
// serve is skipped with a warning; a conflict that would make tool names
// ambiguous is an error.
func Build(doc *v3.Document) (tools []*Tool, warnings []string, err error) {
	if doc == nil || doc.Paths == nil || doc.Paths.PathItems == nil {
		return nil, nil, nil
	}
	hidden := secretParams(doc)
	seen := map[string]string{}
	for pp := doc.Paths.PathItems.First(); pp != nil; pp = pp.Next() {
		path, item := pp.Key(), pp.Value()
		for op := item.GetOperations().First(); op != nil; op = op.Next() {
			method := strings.ToUpper(op.Key())
			call := method + " " + path
			t, skip, err := build(method, path, item, op.Value(), hidden)
			if err != nil {
				return nil, nil, fmt.Errorf("%s: %w", call, err)
			}
			if skip != "" {
				warnings = append(warnings, fmt.Sprintf("skipped %s: %s", call, skip))
				continue
			}
			if prev, dup := seen[t.Name()]; dup {
				return nil, nil, fmt.Errorf("tool name %q is used by both %s and %s; set %s on one of them", t.Name(), prev, call, extName)
			}
			seen[t.Name()] = call
			tools = append(tools, t)
		}
	}
	return tools, warnings, nil
}

// secretParams lists the parameters that carry credentials and so must
// never be exposed to the agent: the Authorization header, and every apiKey
// security scheme's header or query parameter. Keys are "in:name", with
// header names lower-cased.
func secretParams(doc *v3.Document) map[string]bool {
	hidden := map[string]bool{"header:authorization": true}
	if doc.Components == nil || doc.Components.SecuritySchemes == nil {
		return hidden
	}
	for s := doc.Components.SecuritySchemes.First(); s != nil; s = s.Next() {
		sc := s.Value()
		if sc != nil && sc.Type == "apiKey" && (sc.In == "header" || sc.In == "query") {
			hidden[paramKey(sc.In, sc.Name)] = true
		}
	}
	return hidden
}

func paramKey(in, name string) string {
	if in == "header" {
		name = strings.ToLower(name)
	}
	return in + ":" + name
}

// build maps one operation. It returns a non-empty skip reason when the
// operation can't be served.
func build(method, path string, item *v3.PathItem, op *v3.Operation, hidden map[string]bool) (*Tool, string, error) {
	name, err := toolName(op, method, path)
	if err != nil {
		return nil, "", err
	}
	t := &Tool{Method: method, Path: path, Args: map[string]Arg{}}
	props := map[string]any{}
	var required []string

	for _, p := range mergeParams(item.Parameters, op.Parameters) {
		if hidden[paramKey(p.In, p.Name)] {
			continue // credentials come from writgate's env, never from the agent
		}
		isRequired := p.In == "path" || (p.Required != nil && *p.Required)
		if p.In == "cookie" {
			if isRequired {
				return nil, fmt.Sprintf("required cookie parameter %q (cookie parameters are not supported)", p.Name), nil
			}
			continue
		}
		if _, clash := props[p.Name]; clash {
			return nil, fmt.Sprintf("two parameters are both named %q", p.Name), nil
		}
		s, err := paramSchema(p)
		if err != nil {
			return nil, "", fmt.Errorf("parameter %q: %w", p.Name, err)
		}
		if p.Description != "" {
			s["description"] = p.Description
		}
		props[p.Name] = s
		t.Args[p.Name] = Arg{In: Location(p.In), Name: p.Name}
		if isRequired {
			required = append(required, p.Name)
		}
	}

	if rb := op.RequestBody; rb != nil && rb.Content != nil && rb.Content.Len() > 0 {
		mt := jsonMediaType(rb.Content)
		if mt == nil {
			return nil, "request body has no JSON media type", nil
		}
		body, err := inlineSchema(mt.Schema)
		if err != nil {
			return nil, "", fmt.Errorf("request body: %w", err)
		}
		bodyRequired := rb.Required != nil && *rb.Required
		fields, isObject := body["properties"].(map[string]any)
		clash := false
		for k := range fields {
			if _, ok := props[k]; ok || k == "body" {
				clash = true
			}
		}
		if isObject && !clash && body["type"] != "array" {
			for k, v := range fields {
				props[k] = v
				t.Args[k] = Arg{In: InBodyField, Name: k}
			}
			if bodyRequired {
				if req, ok := body["required"].([]any); ok {
					for _, r := range req {
						if s, ok := r.(string); ok {
							required = append(required, s)
						}
					}
				}
			}
		} else {
			if _, ok := props["body"]; ok {
				return nil, `a parameter is named "body", which clashes with the request body`, nil
			}
			props["body"] = body
			t.Args["body"] = Arg{In: InBody, Name: "body"}
			if bodyRequired {
				required = append(required, "body")
			}
		}
	}

	input := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		sort.Strings(required)
		input["required"] = required
	}
	resolved, err := resolve(input)
	if err != nil {
		return nil, "", fmt.Errorf("input schema: %w", err)
	}
	t.Input = resolved

	output, err := outputSchema(op)
	if err != nil {
		return nil, "", fmt.Errorf("response schema: %w", err)
	}

	t.Def = &mcp.Tool{
		Name:        name,
		Description: description(op, method, path),
		InputSchema: input,
		Annotations: annotations(op, method),
	}
	if output != nil {
		t.Def.OutputSchema = output
	}
	return t, "", nil
}

// mergeParams applies OpenAPI's rule that an operation's parameter overrides
// a path-level one with the same name and location.
func mergeParams(pathLevel, opLevel []*v3.Parameter) []*v3.Parameter {
	out := make([]*v3.Parameter, 0, len(pathLevel)+len(opLevel))
	overridden := map[string]bool{}
	for _, p := range opLevel {
		overridden[paramKey(p.In, p.Name)] = true
	}
	for _, p := range pathLevel {
		if !overridden[paramKey(p.In, p.Name)] {
			out = append(out, p)
		}
	}
	return append(out, opLevel...)
}

func paramSchema(p *v3.Parameter) (map[string]any, error) {
	if p.Schema != nil {
		return inlineSchema(p.Schema)
	}
	if p.Content != nil {
		if mt := jsonMediaType(p.Content); mt != nil {
			return inlineSchema(mt.Schema)
		}
	}
	return map[string]any{}, nil
}

// jsonMediaType returns application/json, or else the first +json type.
func jsonMediaType(content *orderedmap.Map[string, *v3.MediaType]) *v3.MediaType {
	if mt, ok := content.Get("application/json"); ok {
		return mt
	}
	for c := content.First(); c != nil; c = c.Next() {
		base, _, _ := strings.Cut(c.Key(), ";")
		base = strings.TrimSpace(base)
		if base == "application/json" || strings.HasSuffix(base, "+json") {
			return c.Value()
		}
	}
	return nil
}

// outputSchema is the schema of the first 2xx JSON response, if any.
func outputSchema(op *v3.Operation) (map[string]any, error) {
	if op.Responses == nil || op.Responses.Codes == nil {
		return nil, nil
	}
	for r := op.Responses.Codes.First(); r != nil; r = r.Next() {
		if !strings.HasPrefix(r.Key(), "2") || r.Value() == nil || r.Value().Content == nil {
			continue
		}
		if mt := jsonMediaType(r.Value().Content); mt != nil && mt.Schema != nil {
			return inlineSchema(mt.Schema)
		}
	}
	return nil, nil
}

// toolName: x-writgate-name, else the operationId (FastAPI's
// _<path>_<method> suffix stripped), else <method>_<path>.
func toolName(op *v3.Operation, method, path string) (string, error) {
	if n := extension(op, extName); n != nil {
		if !validName.MatchString(n.Value) {
			return "", fmt.Errorf("%s %q must be 1-64 characters from [A-Za-z0-9_-]", extName, n.Value)
		}
		return n.Value, nil
	}
	lower := strings.ToLower(method)
	name := op.OperationId
	if name != "" {
		// FastAPI's default id is <function name> + re.sub(r"\W", "_", path) + "_" + method.
		suffix := nonWord.ReplaceAllString(path, "_") + "_" + lower
		if strings.HasSuffix(name, suffix) && len(name) > len(suffix) {
			name = strings.TrimSuffix(name, suffix)
		}
	} else {
		name = lower + "_" + strings.Trim(nonWord.ReplaceAllString(path, "_"), "_")
		name = underscores.ReplaceAllString(name, "_")
	}
	name = badChars.ReplaceAllString(name, "_")
	if len(name) > 64 {
		name = name[:64]
	}
	return name, nil
}

// description: the summary, then the description, then the call.
func description(op *v3.Operation, method, path string) string {
	var parts []string
	if s := strings.TrimSpace(op.Summary); s != "" {
		if !strings.HasSuffix(s, ".") {
			s += "."
		}
		parts = append(parts, s)
	}
	if d := strings.TrimSpace(op.Description); d != "" {
		parts = append(parts, d)
	}
	parts = append(parts, fmt.Sprintf("(%s %s)", method, path))
	return strings.Join(parts, " ")
}

// annotations: GET/HEAD are read-only; PUT/DELETE are idempotent; every
// write is destructive unless the operation sets x-writgate-destructive: false.
func annotations(op *v3.Operation, method string) *mcp.ToolAnnotations {
	a := &mcp.ToolAnnotations{Title: op.Summary}
	switch method {
	case "GET", "HEAD":
		a.ReadOnlyHint = true
		return a
	case "PUT", "DELETE":
		a.IdempotentHint = true
	}
	destructive := true
	if n := extension(op, extDestructive); n != nil && n.Value == "false" {
		destructive = false
	}
	a.DestructiveHint = &destructive
	return a
}

func extension(op *v3.Operation, key string) *yaml.Node {
	if op.Extensions == nil {
		return nil
	}
	n, ok := op.Extensions.Get(key)
	if !ok {
		return nil
	}
	return n
}

func resolve(schema map[string]any) (*jsonschema.Resolved, error) {
	raw, err := json.Marshal(schema)
	if err != nil {
		return nil, err
	}
	var js jsonschema.Schema
	if err := json.Unmarshal(raw, &js); err != nil {
		return nil, err
	}
	return js.Resolve(nil)
}
