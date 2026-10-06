package workers

import (
	"sort"
	"strings"
	"testing"
)

// goldenAgentTools is the complete set of capabilities the agent is allowed to
// reach. Adding a tool to RegisterAll without updating this list fails the test,
// so the agent's capability surface can only grow deliberately.
//
// The set is domain-agnostic: generic work a coding agent is given everywhere
// (files, shell, code, git reads, web, data) plus one human channel, one skill
// channel, one knowledge channel and three context-window operations. Product
// vocabulary never re-enters this list - domain capabilities belong to the
// workflow plane's workers.
//
// Keep this sorted: the test compares sorted slices.
var goldenAgentTools = []string{
	"ask.user",
	"code.execute",
	"context.compact",
	"context.recall",
	"context.remaining",
	"data.query",
	"file.edit",
	"file.glob",
	"file.list",
	"file.read",
	"file.search",
	"file.write",
	"git.diff",
	"git.log",
	"git.status",
	"knowledge.search",
	"shell.run",
	"skill.activate",
	"web.fetch",
	"web.search",
}

// forbiddenAgentToolPatterns names capabilities the agent must never hold.
//
// The World State design is Claim -> Validation -> Fact, under the rule "an agent
// may propose a fact but may not modify one". Accepting a claim is the step that
// turns a proposal into a fact, so it must never become an agent tool. If it were
// registered, the model could promote its own statement and the claim space would
// collapse back into a writable blackboard.
var forbiddenAgentToolPatterns = []string{
	"accept_claim",
	"acceptclaim",
	"promote_claim",
	"force_fact",
	"world.state",
	"world_state",
	"worldstate",
	"state.accept",
	"state.fact",
	"state_authority",
	"state.force",
	"set_fact",
}

func registeredToolNames(t *testing.T) []string {
	t.Helper()
	registry := NewToolRegistry()
	if err := RegisterAll(registry, HandlerConfig{}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	tools := registry.ListTools()
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	sort.Strings(names)
	return names
}

// TestRegisterAllMatchesGoldenToolSet pins the exact agent capability surface.
func TestRegisterAllMatchesGoldenToolSet(t *testing.T) {
	got := registeredToolNames(t)

	want := append([]string(nil), goldenAgentTools...)
	sort.Strings(want)

	if len(got) != len(want) {
		t.Errorf("agent tool count = %d, want %d\ngot:  %v\nwant: %v", len(got), len(want), got, want)
	}

	gotSet := make(map[string]bool, len(got))
	for _, name := range got {
		gotSet[name] = true
	}
	wantSet := make(map[string]bool, len(want))
	for _, name := range want {
		wantSet[name] = true
	}

	for _, name := range want {
		if !gotSet[name] {
			t.Errorf("golden tool %q is no longer registered", name)
		}
	}
	for _, name := range got {
		if !wantSet[name] {
			t.Errorf("new agent tool %q is not in the golden list — the agent's capability surface grew; update goldenAgentTools deliberately", name)
		}
	}
}

// TestRegisterAllExposesNoFactMutationCapability keeps the Claim -> Validation ->
// Fact boundary honest: no registered tool may accept or overwrite world state.
func TestRegisterAllExposesNoFactMutationCapability(t *testing.T) {
	for _, name := range registeredToolNames(t) {
		lower := strings.ToLower(name)
		for _, forbidden := range forbiddenAgentToolPatterns {
			if strings.Contains(lower, forbidden) {
				t.Errorf("agent tool %q matches forbidden pattern %q: the agent must not be able to turn a claim into a fact", name, forbidden)
			}
		}
	}
}
