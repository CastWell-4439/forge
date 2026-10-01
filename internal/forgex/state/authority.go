package state

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// AuthorityRule grants one actor ownership of a state key pattern and read access
// to the actors listed in Readers.
//
// Patterns support a trailing wildcard: "character.*" matches every key under the
// "character." prefix, "*" matches everything, and a pattern without a wildcard
// matches that exact key.
type AuthorityRule struct {
	Pattern string   `json:"pattern" yaml:"pattern"`
	Owner   string   `json:"owner" yaml:"owner"`
	Readers []string `json:"readers,omitempty" yaml:"readers,omitempty"`
}

// AuthorityTable is the state_authority configuration.
//
// Enabled is the single switch. It exists so that "permission checks are off" is
// always an explicit, written-down decision rather than something inferred from a
// missing file or an empty rule list.
type AuthorityTable struct {
	Version int             `json:"version" yaml:"version"`
	Enabled bool            `json:"enabled" yaml:"enabled"`
	Rules   []AuthorityRule `json:"rules" yaml:"rules"`
}

// LoadAuthority reads and validates a state authority YAML file.
//
// A missing or unreadable file is an error, never a silent fallback: the switch
// lives inside the file, so the file must exist.
func LoadAuthority(path string) (*AuthorityTable, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read state authority %s: %w", path, err)
	}

	var table AuthorityTable
	if err := yaml.Unmarshal(data, &table); err != nil {
		return nil, fmt.Errorf("parse state authority %s: %w", path, err)
	}

	for i, rule := range table.Rules {
		if strings.TrimSpace(rule.Pattern) == "" {
			return nil, fmt.Errorf("state authority %s: rule %d has an empty pattern", path, i)
		}
		if strings.TrimSpace(rule.Owner) == "" {
			return nil, fmt.Errorf("state authority %s: rule %d (%s) has an empty owner", path, i, rule.Pattern)
		}
	}

	// Enabled with no rules would deny every write, which is a configuration
	// mistake rather than a security posture.
	if table.Enabled && len(table.Rules) == 0 {
		return nil, fmt.Errorf("state authority %s: enabled is true but no rules are defined", path)
	}

	return &table, nil
}

// Enforcing reports whether write/read permission is actually checked. A nil
// table never enforces.
func (a *AuthorityTable) Enforcing() bool {
	return a != nil && a.Enabled
}

// CanWrite reports whether actor may turn a claim for key into a fact, along with
// a human-readable reason when it may not.
//
// When enforcement is off every actor may write. When it is on, only the owner of
// the first matching rule may write, and a key matching no rule is denied.
func (a *AuthorityTable) CanWrite(actor, key string) (bool, string) {
	if !a.Enforcing() {
		return true, ""
	}
	rule, ok := a.matchRule(key)
	if !ok {
		return false, fmt.Sprintf("no state authority rule covers key %q", key)
	}
	if strings.TrimSpace(actor) != rule.Owner {
		return false, fmt.Sprintf("actor %q is not the owner of %q (owner is %q)", actor, key, rule.Owner)
	}
	return true, ""
}

// CanRead reports whether actor may read state under key. When enforcement is off
// every actor may read. When it is on, the owner and the rule's readers may read,
// and a key matching no rule is denied.
func (a *AuthorityTable) CanRead(actor, key string) (bool, string) {
	if !a.Enforcing() {
		return true, ""
	}
	rule, ok := a.matchRule(key)
	if !ok {
		return false, fmt.Sprintf("no state authority rule covers key %q", key)
	}
	actor = strings.TrimSpace(actor)
	if actor == rule.Owner {
		return true, ""
	}
	for _, reader := range rule.Readers {
		if strings.TrimSpace(reader) == actor {
			return true, ""
		}
	}
	return false, fmt.Sprintf("actor %q may not read %q (owner %q, readers %v)", actor, key, rule.Owner, rule.Readers)
}

// matchRule returns the first rule whose pattern covers key. Order matters: the
// first match wins, so put more specific patterns first.
func (a *AuthorityTable) matchRule(key string) (AuthorityRule, bool) {
	if a == nil {
		return AuthorityRule{}, false
	}
	for _, rule := range a.Rules {
		if MatchKeyPattern(rule.Pattern, key) {
			return rule, true
		}
	}
	return AuthorityRule{}, false
}

// MatchKeyPattern reports whether a state key pattern covers a key.
func MatchKeyPattern(pattern, key string) bool {
	pattern = strings.TrimSpace(pattern)
	key = strings.TrimSpace(key)
	if pattern == "" || key == "" {
		return false
	}
	if pattern == "*" {
		return true
	}
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(key, strings.TrimSuffix(pattern, "*"))
	}
	return pattern == key
}
