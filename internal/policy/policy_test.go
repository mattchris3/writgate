package policy

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/mattchris3/writgate/internal/tools"
)

// *tools.Tool must satisfy Tool, so serve can pass tools straight in.
var _ Tool = (*tools.Tool)(nil)

type fakeTool struct {
	name        string
	readOnly    bool
	destructive bool
}

func (f fakeTool) Name() string      { return f.name }
func (f fakeTool) ReadOnly() bool    { return f.readOnly }
func (f fakeTool) Destructive() bool { return f.destructive }

// The six tools of the demo payments API.
var (
	createCharge = fakeTool{name: "create_charge", destructive: true}
	listCharges  = fakeTool{name: "list_charges", readOnly: true}
	getCharge    = fakeTool{name: "get_charge", readOnly: true}
	createRefund = fakeTool{name: "create_refund", destructive: true}
	listRefunds  = fakeTool{name: "list_refunds", readOnly: true}
	cancelRefund = fakeTool{name: "cancel_refund", destructive: true}
	// A write marked x-writgate-destructive: false: in neither group.
	tagCharge = fakeTool{name: "tag_charge"}
)

func TestMatch(t *testing.T) {
	tests := []struct {
		pattern, name string
		want          bool
	}{
		{"create_refund", "create_refund", true},
		{"create_refund", "create_refunds", false},
		{"create_refund", "Create_Refund", false}, // case-sensitive
		{"*", "anything", true},
		{"*", "", true},
		{"create_*", "create_charge", true},
		{"create_*", "create_", true}, // * matches nothing too
		{"create_*", "recreate_charge", false},
		{"*_refund", "cancel_refund", true},
		{"*_refund", "cancel_refunds", false},
		{"list_*_v2", "list_charges_v2", true},
		{"list_*_v2", "list_charges_v3", false},
		{"*charge*", "list_charges", true},
		{"a*b*c", "abc", true},
		{"a*b*c", "axxbyyc", true},
		{"a*b*c", "axxcyyb", false},
		{"**", "x", true},
	}
	for _, tt := range tests {
		if got := match(tt.pattern, tt.name); got != tt.want {
			t.Errorf("match(%q, %q) = %v, want %v", tt.pattern, tt.name, got, tt.want)
		}
	}
}

func TestDecide(t *testing.T) {
	tests := []struct {
		name   string
		policy *Policy
		tool   fakeTool
		want   Decision
	}{
		// Default policy: only readonly.
		{"default allows readonly", Default(), listCharges, Decision{true, "allow readonly"}},
		{"default denies writes", Default(), createRefund, Decision{false, "default deny"}},
		{"default denies a non-destructive write", Default(), tagCharge, Decision{false, "default deny"}},

		// The demo policy.
		{"demo: readonly", demo(), getCharge, Decision{true, "allow readonly"}},
		{"demo: named tool", demo(), createRefund, Decision{true, "allow create_refund"}},
		{"demo: other writes denied", demo(), createCharge, Decision{false, "default deny"}},
		{"demo: cancel denied", demo(), cancelRefund, Decision{false, "default deny"}},

		// Deny wins, whatever the order.
		{"deny beats allow", &Policy{Allow: []string{"create_refund"}, Deny: []string{"create_refund"}}, createRefund, Decision{false, "deny create_refund"}},
		{"deny group beats named allow", &Policy{Allow: []string{"create_refund"}, Deny: []string{"destructive"}}, createRefund, Decision{false, "deny destructive"}},
		{"deny wildcard beats allow *", &Policy{Allow: []string{"*"}, Deny: []string{"cancel_*"}}, cancelRefund, Decision{false, "deny cancel_*"}},
		{"allow * still allows the rest", &Policy{Allow: []string{"*"}, Deny: []string{"cancel_*"}}, createCharge, Decision{true, "allow *"}},

		// Groups.
		{"allow destructive group", &Policy{Allow: []string{"destructive"}}, createCharge, Decision{true, "allow destructive"}},
		{"destructive group skips non-destructive writes", &Policy{Allow: []string{"destructive"}}, tagCharge, Decision{false, "default deny"}},
		{"deny readonly group", &Policy{Allow: []string{"*"}, Deny: []string{"readonly"}}, listRefunds, Decision{false, "deny readonly"}},

		// The first matching rule in list order is reported.
		{"first allow reported", &Policy{Allow: []string{"list_*", "readonly"}}, listCharges, Decision{true, "allow list_*"}},
		{"first deny reported", &Policy{Allow: []string{"*"}, Deny: []string{"*_refund", "destructive"}}, cancelRefund, Decision{false, "deny *_refund"}},

		// Empty lists.
		{"empty policy denies everything", &Policy{}, listCharges, Decision{false, "default deny"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.policy.Decide(tt.tool); got != tt.want {
				t.Errorf("Decide(%s) = %+v, want %+v", tt.tool.name, got, tt.want)
			}
		})
	}
}

// demo is the policy from the v0.1 demo story.
func demo() *Policy {
	return &Policy{Allow: []string{"readonly", "create_refund"}}
}

func TestParse(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		p, err := Parse([]byte("allow: [readonly, create_refund]\ndeny:\n  - cancel_*\n"))
		if err != nil {
			t.Fatal(err)
		}
		if len(p.Allow) != 2 || p.Allow[1] != "create_refund" || len(p.Deny) != 1 || p.Deny[0] != "cancel_*" {
			t.Errorf("Parse() = %+v", p)
		}
	})
	t.Run("deny may be left out", func(t *testing.T) {
		p, err := Parse([]byte("allow: [readonly]\n"))
		if err != nil {
			t.Fatal(err)
		}
		if len(p.Deny) != 0 {
			t.Errorf("Deny = %v, want empty", p.Deny)
		}
	})

	bad := []struct{ name, yaml string }{
		{"unknown key (typo)", "alow: [readonly]\n"},
		{"empty rule", "allow: ['']\n"},
		{"rule with a space", "allow: ['create refund']\n"},
		{"rule with a slash", "allow: ['/charges']\n"},
		{"not a list", "allow: readonly\n"},
		{"not YAML", "allow: [readonly\n"},
	}
	for _, tt := range bad {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Parse([]byte(tt.yaml)); err == nil {
				t.Error("Parse() succeeded, want an error")
			} else if errors.Is(err, ErrNotImplemented) {
				t.Error("Parse() is not implemented yet")
			}
		})
	}
}

func TestLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "writgate.yaml")
	if err := os.WriteFile(path, []byte("allow: [readonly]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Decide(listCharges); !got.Allowed {
		t.Errorf("loaded policy denied a readonly tool: %+v", got)
	}

	if _, err := Load(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Error("Load() of a missing file succeeded")
	}
}
