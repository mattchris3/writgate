package tools

import (
	"encoding/json"
	"fmt"

	"github.com/pb33f/libopenapi/datamodel/high/base"
)

// inlineSchema renders a schema with every $ref inlined and tidies it into
// the JSON Schema an agent sees.
func inlineSchema(sp *base.SchemaProxy) (map[string]any, error) {
	if sp == nil {
		return map[string]any{}, nil
	}
	s := sp.Schema()
	if s == nil {
		if err := sp.GetBuildError(); err != nil {
			return nil, fmt.Errorf("build schema: %w", err)
		}
		return map[string]any{}, nil
	}
	b, err := s.MarshalJSONInline()
	if err != nil {
		return nil, fmt.Errorf("inline schema: %w", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("decode schema: %w", err)
	}
	tidy(m)
	return m, nil
}

// tidy rewrites a schema in place:
//   - drops "title" (FastAPI adds one to every field, which is noise for an agent);
//   - collapses anyOf [X, {type: null}] to X, since an omitted optional
//     argument already means "no value";
//   - converts OpenAPI 3.0 forms to JSON Schema 2020-12: drops "nullable",
//     and turns boolean exclusiveMinimum/exclusiveMaximum into numbers.
//
// Keys under "properties" are field names, not keywords, so a field called
// "title" survives.
func tidy(v any) {
	switch t := v.(type) {
	case map[string]any:
		collapseNullable(t) // first, so keywords it pulls up are tidied too
		delete(t, "title")
		delete(t, "nullable")
		fixExclusive(t, "exclusiveMinimum", "minimum")
		fixExclusive(t, "exclusiveMaximum", "maximum")
		for k, val := range t {
			if k == "properties" || k == "patternProperties" || k == "$defs" || k == "definitions" {
				if props, ok := val.(map[string]any); ok {
					for _, pv := range props {
						tidy(pv)
					}
				}
				continue
			}
			tidy(val)
		}
	case []any:
		for _, item := range t {
			tidy(item)
		}
	}
}

// collapseNullable turns {anyOf: [X, {type: null}]} into X, keeping any
// keywords (such as description or default) set next to the anyOf.
func collapseNullable(t map[string]any) {
	alts, ok := t["anyOf"].([]any)
	if !ok || len(alts) != 2 {
		return
	}
	for i, alt := range alts {
		am, ok := alt.(map[string]any)
		if !ok || am["type"] != "null" || len(am) != 1 {
			continue
		}
		other, ok := alts[1-i].(map[string]any)
		if !ok {
			return
		}
		delete(t, "anyOf")
		for k, val := range other {
			if _, exists := t[k]; !exists {
				t[k] = val
			}
		}
		return
	}
}

// fixExclusive converts OpenAPI 3.0's boolean exclusive bounds.
func fixExclusive(t map[string]any, excl, bound string) {
	b, ok := t[excl].(bool)
	if !ok {
		return
	}
	delete(t, excl)
	if b {
		if n, ok := t[bound]; ok {
			t[excl] = n
			delete(t, bound)
		}
	}
}
