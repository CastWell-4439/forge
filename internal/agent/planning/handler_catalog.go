package planning

import (
	"fmt"
	"sort"
	"strings"
)

// HandlerSpec describes one workflow handler a generated DAG may use.
//
// Description is written for a model to read, so it says when to reach for this
// handler rather than only what it is called.
type HandlerSpec struct {
	// Name is the handler as the workflow dialect spells it.
	Name string
	// Description is one line for the planning prompt.
	Description string
	// Required lists params a task using this handler must carry. L4 enforces
	// it, so an incomplete task is caught before dispatch instead of by a
	// worker that cannot run it.
	Required []string
}

// HandlerCatalog is the set of workflow handlers a generated DAG may use.
//
// It replaces the agent tool registry as the source of truth for validation.
// Those are different vocabularies — a tool is something the agent calls during
// a run, a handler is something the workflow dispatches to a worker — and
// validating one against the other is how a DAG naming `web.fetch` passed every
// layer and then had nowhere to go at dispatch time.
//
// The assembly layer builds this from the workers it actually registered, so the
// catalog cannot drift from what the deployment can run.
type HandlerCatalog struct {
	specs map[string]HandlerSpec
}

// NewHandlerCatalog builds a catalog from specs. Order does not matter; the
// prompt is sorted so the same deployment produces the same prompt.
func NewHandlerCatalog(specs []HandlerSpec) *HandlerCatalog {
	m := make(map[string]HandlerSpec, len(specs))
	for _, s := range specs {
		m[s.Name] = s
	}
	return &HandlerCatalog{specs: m}
}

// Has reports whether a handler is in the catalog.
func (c *HandlerCatalog) Has(name string) bool {
	if c == nil {
		return false
	}
	_, ok := c.specs[name]
	return ok
}

// Spec returns a handler's spec. The bool reports whether it exists, so a caller
// cannot mistake a zero spec for a real one with no requirements.
func (c *HandlerCatalog) Spec(name string) (HandlerSpec, bool) {
	if c == nil {
		return HandlerSpec{}, false
	}
	s, ok := c.specs[name]
	return s, ok
}

// Names returns every handler name, sorted.
func (c *HandlerCatalog) Names() []string {
	if c == nil {
		return nil
	}
	out := make([]string, 0, len(c.specs))
	for name := range c.specs {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// FormatForPrompt renders the catalog for the planning prompt: every handler
// with its description and required params.
//
// Required params travel with the description because the model cannot infer
// them from a name, and a task missing one is rejected at L4 with an error it
// would otherwise have had no way to avoid.
func (c *HandlerCatalog) FormatForPrompt() string {
	if c == nil || len(c.specs) == 0 {
		return "(no handlers available)"
	}
	var b strings.Builder
	for _, name := range c.Names() {
		s := c.specs[name]
		fmt.Fprintf(&b, "- %s: %s", name, s.Description)
		if len(s.Required) > 0 {
			fmt.Fprintf(&b, " (params 必填: %s)", strings.Join(s.Required, ", "))
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// FindSimilar suggests a handler name close to the one given, for a validation
// error a model can act on. It matches on substring in either direction, which
// catches the common cases — a name with a suffix (`agent.run` for `agent`) or a
// truncation (`agnt` will not match, but `fetch` for `web.fetch` will).
//
// The suggestion is advisory: an empty return means "no idea", and the caller
// says only that the handler is unknown. Guessing a close-but-wrong name would
// be worse than saying nothing.
func (c *HandlerCatalog) FindSimilar(name string) string {
	if c == nil || name == "" {
		return ""
	}
	lower := strings.ToLower(name)
	var best string
	for candidate := range c.specs {
		cl := strings.ToLower(candidate)
		if strings.Contains(cl, lower) || strings.Contains(lower, cl) {
			// Prefer the shortest match: the closest name to what was written.
			if best == "" || len(candidate) < len(best) {
				best = candidate
			}
		}
	}
	return best
}
