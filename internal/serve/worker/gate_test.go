package worker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	forgev1 "github.com/castwell/forge/api/proto/gen"
	"github.com/castwell/forge/internal/forgex/model"
	"github.com/castwell/forge/internal/forgex/policy"
	"github.com/castwell/forge/internal/forgex/runtimegate"
	"github.com/castwell/forge/internal/worker"
)

func TestNormalizeGateMode(t *testing.T) {
	cases := []struct {
		in   string
		want model.GateMode
	}{
		{"shadow", model.GateModeShadow},
		{"enforce", model.GateModeEnforce},
		{" Enforce ", model.GateModeEnforce},
		// Disabled and unrecognised values must both leave the gate off: a typo
		// must never turn enforcement on.
		{"off", ""},
		{"disabled", ""},
		{"", ""},
		{"enforce!", ""},
	}
	for _, tc := range cases {
		if got := normalizeGateMode(tc.in); got != tc.want {
			t.Errorf("normalizeGateMode(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestGateDefaultsToShadowAndCanBeDisabled(t *testing.T) {
	t.Setenv(envGateMode, "")
	opts, enabled := runtimeGateOptionsFromEnv()
	if !enabled || opts.Mode != model.GateModeShadow {
		t.Fatalf("unset mode: enabled=%v mode=%q, want an enabled shadow gate", enabled, opts.Mode)
	}

	t.Setenv(envGateMode, "off")
	if _, enabled := runtimeGateOptionsFromEnv(); enabled {
		t.Error("FORGEX_GATE_MODE=off must disable the gate")
	}

	t.Setenv(envGateMode, "enforc")
	if _, enabled := runtimeGateOptionsFromEnv(); enabled {
		t.Error("an unrecognised mode must not enable the gate")
	}
}

// buildTestGate assembles a gate from the contracts that ship with the worker.
func buildTestGate(t *testing.T, mode model.GateMode, authority string) *runtimegate.Gate {
	t.Helper()
	gate, err := buildRuntimeGate(runtimeGateOptions{
		Mode:          mode,
		Authority:     authority,
		Root:          t.TempDir(),
		ContractsPath: filepath.Join("..", "..", "..", defaultWorkerContract),
	})
	if err != nil {
		t.Fatalf("buildRuntimeGate: %v", err)
	}
	return gate
}

func TestShippedContractsProduceRealDecisions(t *testing.T) {
	cases := []struct {
		authority string
		handler   string
		want      worker.GateAction
		why       string
	}{
		// Work that stays inside the workspace never needs elevated authority.
		{string(policy.AuthorityL0), "shell", worker.GateActionAllow, "local write"},
		{string(policy.AuthorityL0), "claude_code", worker.GateActionAllow, "local write"},
		{string(policy.AuthorityL0), "database", worker.GateActionAllow, "read only"},
		{string(policy.AuthorityL0), "hitl", worker.GateActionAllow, "no side effect"},

		// Reaching an external API is denied outright below L2.
		{string(policy.AuthorityL0), "ai", worker.GateActionBlock, "external API call below L2"},
		{string(policy.AuthorityL1), "review", worker.GateActionBlock, "external API call below L2"},
		{string(policy.AuthorityL2), "ai", worker.GateActionAllow, "external API call at L2"},
		{string(policy.AuthorityL2), "review", worker.GateActionAllow, "external API call at L2"},

		// A high-risk tool is never waved through: below L2 the external call is
		// denied, and from L2 onwards it still needs a human.
		{string(policy.AuthorityL0), "mcp", worker.GateActionBlock, "external API call below L2"},
		{string(policy.AuthorityL2), "mcp", worker.GateActionPause, "high risk needs approval"},
		{string(policy.AuthorityL4), "mcp", worker.GateActionPause, "high risk needs approval"},

		// Writing outside the workspace requires L3, and then still a human.
		{string(policy.AuthorityL0), "git", worker.GateActionBlock, "external write requires L3"},
		{string(policy.AuthorityL2), "git", worker.GateActionBlock, "external write requires L3"},
		{string(policy.AuthorityL3), "git", worker.GateActionPause, "high risk needs approval"},
	}

	for _, tc := range cases {
		gate := buildTestGate(t, model.GateModeEnforce, tc.authority)
		decision, err := gate.BeforeExecute(context.Background(), worker.GateRequest{
			TaskID: "task_1", WorkflowID: "wf_1", Handler: tc.handler,
		})
		if err != nil {
			t.Fatalf("authority=%s handler=%s: %v", tc.authority, tc.handler, err)
		}
		if decision.Action != tc.want {
			t.Errorf("authority=%s handler=%s: action = %q, want %q (%s)",
				tc.authority, tc.handler, decision.Action, tc.want, tc.why)
		}
	}
}

// TestEnforceBlocksTheHandler is the end-to-end proof: in enforce mode a denied
// task never reaches its handler, while shadow mode still executes it.
func TestEnforceBlocksTheHandler(t *testing.T) {
	cases := []struct {
		mode       model.GateMode
		wantCalled bool
	}{
		{model.GateModeEnforce, false},
		{model.GateModeShadow, true},
	}

	for _, tc := range cases {
		t.Run(string(tc.mode), func(t *testing.T) {
			called := false
			registry := worker.NewRegistry()
			registry.Register("mcp", func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
				called = true
				return map[string]interface{}{"ok": true}, nil
			})

			exec := worker.NewExecutor(registry).
				WithRuntimeGate(buildTestGate(t, tc.mode, string(policy.AuthorityL0)))

			resp := exec.Execute(context.Background(), &forgev1.TaskRequest{
				TaskId:     "task_1",
				WorkflowId: "wf_1",
				Handler:    "mcp",
				Input:      []byte("{}"),
			})

			if called != tc.wantCalled {
				t.Errorf("handler called = %v, want %v", called, tc.wantCalled)
			}
			if tc.mode == model.GateModeEnforce && resp.GetSuccess() {
				t.Error("enforce mode reported success for a task it blocked")
			}
			if !tc.wantCalled && resp.GetErrorMsg() == "" {
				t.Error("a blocked task must explain why")
			}
		})
	}
}

// TestGateDecisionIsPersisted checks the audit trail the gate exists to produce.
func TestGateDecisionIsPersisted(t *testing.T) {
	root := t.TempDir()
	gate, err := buildRuntimeGate(runtimeGateOptions{
		Mode:          model.GateModeShadow,
		Authority:     string(policy.AuthorityL0),
		Root:          root,
		ContractsPath: filepath.Join("..", "..", "..", defaultWorkerContract),
	})
	if err != nil {
		t.Fatalf("buildRuntimeGate: %v", err)
	}
	if _, err := gate.BeforeExecute(context.Background(), worker.GateRequest{
		TaskID: "task_1", WorkflowID: "wf_1", Handler: "git",
	}); err != nil {
		t.Fatalf("BeforeExecute: %v", err)
	}

	var found string
	walkErr := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		if strings.Contains(entry.Name(), "gate_decisions") {
			found = path
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk %s: %v", root, walkErr)
	}
	if found == "" {
		t.Fatalf("no gate_decisions file was written under %s", root)
	}
	data, err := os.ReadFile(found)
	if err != nil {
		t.Fatalf("read %s: %v", found, err)
	}
	if !strings.Contains(string(data), `"tool_name":"git"`) {
		t.Errorf("the persisted decision does not record which handler was gated: %s", data)
	}
	if !strings.Contains(string(data), `"action":"block"`) {
		t.Errorf("the persisted decision does not record the action: %s", data)
	}
}
