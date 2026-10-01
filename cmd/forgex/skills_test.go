package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/castwell/forge/internal/forgex/cases"
	"github.com/castwell/forge/internal/forgex/demo"
	"github.com/castwell/forge/internal/forgex/skillpack"
)

// TestSkillsEndToEnd walks the whole loop-terminating flow the CLI exposes:
// a verified replay is distilled into a draft, the draft is refused by the
// publish gates, a review fixes it, publish succeeds, and verify re-runs the
// bound case through both gates.
func TestSkillsEndToEnd(t *testing.T) {
	// Case registry entries carry repo-relative packet paths and the demo
	// config constants assume the repository root as the working directory, so
	// run the test from there. Restored before returning: the other tests in
	// this package address configs with their own ../.. prefix.
	oldCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(filepath.Join("..", "..")); err != nil {
		t.Fatalf("chdir repo root: %v", err)
	}
	defer func() { _ = os.Chdir(oldCwd) }()

	casesPath := "configs/forgex/cases.yaml"
	rulesPath := "configs/forgex/eval_rules.yaml"

	reg, err := cases.Load(casesPath)
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	spec, err := reg.Find("generic-contract-success")
	if err != nil {
		t.Fatalf("find case: %v", err)
	}

	// 1. Produce a verified replay: run the scenario through both gates so the
	// run carries eval_result.json and replay_result.json.
	root := t.TempDir()
	runID, err := demo.RunScenario(context.Background(), demo.ScenarioConfig{
		Root:           root,
		TaxonomyPath:   demo.DefaultTaxonomyPath,
		PolicyPath:     demo.DefaultPolicyPath,
		PacketPath:     spec.TaskPacket,
		ContractsPath:  demo.DefaultContractsPath,
		ToolPolicyPath: demo.DefaultToolPolicyPath,
		AuthorityLevel: demo.DefaultAuthorityLevel,
	})
	if err != nil {
		t.Fatalf("run scenario: %v", err)
	}
	runDir := filepath.Join(root, "runs", runID)
	if _, err := evaluateRunDir(runDir, rulesPath, spec.Suite); err != nil {
		t.Fatalf("evaluate run: %v", err)
	}
	if _, err := replayCase(runDir, spec); err != nil {
		t.Fatalf("replay case: %v", err)
	}

	skillsDir := t.TempDir()
	skillsArgs := func(extra ...string) []string {
		return append(extra, "--cases", casesPath, "--skills-dir", skillsDir)
	}

	// 2. Distill.
	if err := runSkills(skillsArgs("distill", "--run", runDir)); err != nil {
		t.Fatalf("distill: %v", err)
	}
	draftPath := filepath.Join(skillsDir, "drafts", spec.ID+".yaml")
	if _, err := os.Stat(draftPath); err != nil {
		t.Fatalf("draft was not written: %v", err)
	}

	// 3. Publish before review must be refused: the readme is still the
	// template and the recorded tool is not workflow vocabulary.
	err = runSkills(skillsArgs("publish", draftPath))
	if err == nil {
		t.Fatal("publishing an unrevised draft should be blocked")
	}
	if !strings.Contains(err.Error(), "blocked") {
		t.Errorf("error should report the gates: %v", err)
	}

	// 4. Review: write the readme and normalize the tool vocabulary.
	pack, err := skillpack.LoadPath(draftPath)
	if err != nil {
		t.Fatalf("load draft: %v", err)
	}
	pack.Readme = "## Reviewed skill\n\nWritten during review; explains when to apply it."
	pack.Spec.ToolPermissions = []string{"ai.analyze"}
	pack.Spec.Steps = []skillpack.Step{{Name: "analyze", Tools: []string{"ai.analyze"}}}
	if _, err := skillpack.NewStore(skillsDir).Save(pack, true); err != nil {
		t.Fatalf("save reviewed draft: %v", err)
	}

	// 5. Publish now succeeds.
	if err := runSkills(skillsArgs("publish", draftPath)); err != nil {
		t.Fatalf("publish after review: %v", err)
	}
	published := filepath.Join(skillsDir, spec.ID+".yaml")
	if _, err := os.Stat(published); err != nil {
		t.Fatalf("published file missing: %v", err)
	}

	// 6. Verify re-runs the bound case; both gates must hold.
	verify := skillsArgs("verify", "--skill", spec.ID, "--root", root, "--rules", rulesPath)
	if err := runSkills(verify); err != nil {
		t.Fatalf("verify: %v", err)
	}

	// 7. Machine-facing surfaces.
	if err := runSkills([]string{"list", "--skills-dir", skillsDir}); err != nil {
		t.Errorf("list: %v", err)
	}
	if err := runSkills([]string{"export", "--skills-dir", skillsDir}); err != nil {
		t.Errorf("export: %v", err)
	}
}

// TestOrderFlagsFirst covers the argument reordering publish relies on.
func TestOrderFlagsFirst(t *testing.T) {
	got := orderFlagsFirst([]string{"draft.yaml", "--skills-dir", "d", "--cases", "c.yaml"})
	want := []string{"--skills-dir", "d", "--cases", "c.yaml", "draft.yaml"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}
