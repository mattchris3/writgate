// Package policy decides which tools an agent may call. It implements
// docs/SPEC.md section 4: allow and deny lists of tool-name patterns and the
// groups "readonly" and "destructive", deny by default, and deny wins.
//
// Matthew implements this package; see README.md in this directory.
package policy

import "errors"

// Tool is what the policy needs to know about a tool. *tools.Tool
// satisfies it.
type Tool interface {
	Name() string
	ReadOnly() bool    // in the group "readonly"
	Destructive() bool // in the group "destructive"
}

// Group names a rule can use in place of a tool-name pattern.
const (
	GroupReadOnly    = "readonly"
	GroupDestructive = "destructive"
)

// Policy is a parsed writgate.yaml.
type Policy struct {
	Allow []string `yaml:"allow"`
	Deny  []string `yaml:"deny"`
}

// Decision is the result of checking one tool. Rule is written to the audit
// log, so a reader can see why a call was allowed or denied.
type Decision struct {
	Allowed bool
	// Rule is the rule that decided, as "allow <rule>" or "deny <rule>",
	// e.g. "deny cancel_*" or "allow readonly". When no rule matches it is
	// "default deny".
	Rule string
}

// ErrNotImplemented is returned by the stubs below until they are written.
var ErrNotImplemented = errors.New("policy: not implemented")

// Default is the policy used when no --policy file is given: only readonly
// tools are allowed.
func Default() *Policy {
	// TODO(Matthew): implement.
	return &Policy{}
}

// Load reads and parses a policy file.
func Load(path string) (*Policy, error) {
	// TODO(Matthew): read the file, then call Parse.
	return nil, ErrNotImplemented
}

// Parse parses a policy from YAML. It rejects unknown keys, empty rules,
// and rules with characters a tool name can't contain, so a typo fails at
// startup instead of silently allowing or denying.
func Parse(data []byte) (*Policy, error) {
	// TODO(Matthew): implement.
	return nil, ErrNotImplemented
}

// Decide checks one tool. Deny wins over allow whatever the order, and a
// tool no allow rule matches is denied.
func (p *Policy) Decide(t Tool) Decision {
	// TODO(Matthew): implement.
	return Decision{}
}

// match reports whether a tool name matches a pattern, where '*' matches
// any run of characters, including none. Matching is case-sensitive.
func match(pattern, name string) bool {
	// TODO(Matthew): implement.
	return false
}
