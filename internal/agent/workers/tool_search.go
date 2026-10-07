package workers

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// ToolSearchName is the tool-discovery tool's name.
const ToolSearchName = "tool.search"

// ToolSearchDef lets the model look a tool up by name or keyword.
//
// It exists because the prompt no longer lists every tool (C3): only the most
// relevant ones are injected, and without a way to ask for the rest the model
// would silently lose capabilities it cannot see. The cutoff has to be
// discoverable, or it becomes an invisible ceiling.
func ToolSearchDef() *ToolDef {
	return &ToolDef{
		Name:        ToolSearchName,
		DisplayName: "Search Tools",
		Category:    "agent",
		Description: "Find tools by name or keyword. Use it when the tool you need is not in the visible " +
			"list — that list is a relevant subset, and the full set is larger. Returns matching tool names " +
			"with their descriptions.",
		InputSchema: map[string]ParamDef{
			"query": {Type: "string", Description: "Keyword or tool name to search for", Required: true},
			"limit": {Type: "integer", Description: "Maximum matches to return (default 10)"},
		},
		OutputSchema: map[string]ParamDef{
			"matches": {Type: "array", Description: "Matching tools as {name, description}"},
			"status":  {Type: "string", Description: `"ok" or "no_match"`},
		},
		RequiredParams: []string{"query"},
		// Discovery reads the registry and changes nothing.
		Effect:        EffectRead,
		EstimatedTime: 0,
	}
}

// ToolSearcher is the registry-side capability tool.search needs: a way to ask
// what tools exist. Injected rather than reached for, because the registry
// lives in this package while the loop intercepts the call.
type ToolSearcher interface {
	// SearchTools returns tools whose name or description matches query,
	// newest-registered-agnostic but deterministic (sorted by name).
	SearchTools(query string, limit int) []ToolMatch
}

// ToolMatch is one search hit.
type ToolMatch struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// Effect is included so the model can reason about what a tool does before
	// asking for it — a delete-class tool is worth knowing about in advance.
	Effect string `json:"effect,omitempty"`
}

// SearchTools finds tools whose name or description matches query.
//
// Match order: name matches rank above description matches (a model asking for
// "git" means the git tools, not every tool whose prose mentions git), and
// ties break by name so repeated searches answer identically.
//
// A function rather than a method: ToolRegistry is an alias of core.ToolRegistry
// (see registry.go), and Go does not allow methods on a non-local type.
func SearchTools(registry *ToolRegistry, query string, limit int) []ToolMatch {
	if limit <= 0 {
		limit = 10
	}
	needle := strings.ToLower(strings.TrimSpace(query))
	if needle == "" {
		return nil
	}

	var nameHits, descHits []ToolMatch
	for _, def := range registry.ListTools() {
		match := ToolMatch{Name: def.Name, Description: def.Description, Effect: string(def.Effect)}
		switch {
		case strings.Contains(strings.ToLower(def.Name), needle):
			nameHits = append(nameHits, match)
		case strings.Contains(strings.ToLower(def.Description), needle):
			descHits = append(descHits, match)
		}
	}

	sort.Slice(nameHits, func(i, j int) bool { return nameHits[i].Name < nameHits[j].Name })
	sort.Slice(descHits, func(i, j int) bool { return descHits[i].Name < descHits[j].Name })

	out := append(nameHits, descHits...)
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// toolSearchUnavailable is the registry handler for tool.search.
//
// Like the other meta tools it is intercepted by the loop (which owns the
// registry view); reaching this handler means the interception is missing, and
// a silent empty result would look like "no such tool" instead.
func toolSearchUnavailable(name string) HandlerFunc {
	return func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
		return nil, fmt.Errorf(
			"%s must be handled by the agent loop, not dispatched as a handler; "+
				"reaching this handler means the interception is missing", name)
	}
}
