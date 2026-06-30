package context

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/castwell/forge/internal/forgex/model"
	forgexstate "github.com/castwell/forge/internal/forgex/state"
	"gopkg.in/yaml.v3"
)

// RunContext is the inspectable context state for one ForgeX run.
type RunContext struct {
	Ledger       *model.ProgressLedger
	ContextPacks []model.ContextPack
	WorldState   *model.WorldState
	Artifacts    []model.ArtifactRecord
}

// LoadRunContext loads progress_ledger.yaml and context_packs.jsonl from a run directory.
func LoadRunContext(runDir string) (RunContext, error) {
	var result RunContext
	ledgerPath := filepath.Join(runDir, "progress_ledger.yaml")
	if data, err := os.ReadFile(ledgerPath); err == nil {
		var ledger model.ProgressLedger
		if err := yaml.Unmarshal(data, &ledger); err != nil {
			return RunContext{}, err
		}
		result.Ledger = &ledger
	} else if !os.IsNotExist(err) {
		return RunContext{}, err
	}

	packsPath := filepath.Join(runDir, "context_packs.jsonl")
	packs, err := readJSONLFile[model.ContextPack](packsPath)
	if err != nil {
		return RunContext{}, err
	}
	result.ContextPacks = packs

	worldStatePath := filepath.Join(runDir, "world_state.yaml")
	if data, err := os.ReadFile(worldStatePath); err == nil {
		var worldState model.WorldState
		if err := yaml.Unmarshal(data, &worldState); err != nil {
			return RunContext{}, err
		}
		result.WorldState = &worldState
	} else if !os.IsNotExist(err) {
		return RunContext{}, err
	}

	artifactsPath := filepath.Join(runDir, "artifacts.jsonl")
	artifacts, err := readJSONLFile[model.ArtifactRecord](artifactsPath)
	if err != nil {
		return RunContext{}, err
	}
	result.Artifacts = artifacts
	return result, nil
}

// FormatInspect renders context state for CLI output.
func FormatInspect(state RunContext) string {
	var b strings.Builder
	b.WriteString("ForgeX Context Inspect\n")
	b.WriteString("\nProgress Ledger\n")
	if state.Ledger == nil {
		b.WriteString("  (none)\n")
	} else {
		ledger := state.Ledger
		b.WriteString(fmt.Sprintf("  run_id: %s\n", ledger.RunID))
		b.WriteString(fmt.Sprintf("  phase: %s\n", emptyPlaceholder(ledger.CurrentPhase)))
		b.WriteString(fmt.Sprintf("  completion: %.0f%%\n", ledger.CompletionRatio()*100))
		for _, item := range ledger.Checklist {
			b.WriteString(fmt.Sprintf("  - [%s] %s", item.Status, item.Title))
			if item.Evidence != "" {
				b.WriteString(fmt.Sprintf(" (%s)", item.Evidence))
			}
			b.WriteString("\n")
		}
		for _, blocker := range ledger.Blockers {
			b.WriteString(fmt.Sprintf("  blocker: %s\n", blocker))
		}
		for _, action := range ledger.NextActions {
			b.WriteString(fmt.Sprintf("  next: %s\n", action))
		}
	}

	b.WriteString("\nContext Packs\n")
	if len(state.ContextPacks) == 0 {
		b.WriteString("  (none)\n")
	} else {
		for _, pack := range state.ContextPacks {
			b.WriteString(fmt.Sprintf("  - %s purpose=%s tokens=%d/%d truncated=%t exceeded=%t\n", pack.ID, pack.Purpose, pack.EstimatedTokens, pack.BudgetTokens, pack.Truncated, pack.BudgetExceeded))
			if pack.Summary != "" {
				summary := pack.Summary
				if len(summary) > 120 {
					summary = summary[:120] + "..."
				}
				b.WriteString(fmt.Sprintf("    summary: %s\n", strings.ReplaceAll(summary, "\n", " ")))
			}
			for _, ref := range pack.ArtifactRefs {
				b.WriteString(fmt.Sprintf("    artifact: %s\n", ref))
			}
		}
	}

	summary := forgexstate.Summarize(state.WorldState, state.Artifacts)
	b.WriteString("\nWorld State\n")
	if state.WorldState == nil {
		b.WriteString("  (none)\n")
	} else {
		b.WriteString(fmt.Sprintf("  version: %d\n", summary.Version))
		b.WriteString(fmt.Sprintf("  accepted: %d\n", summary.AcceptedEntries))
		b.WriteString(fmt.Sprintf("  proposed: %d\n", summary.ProposedEntries))
		b.WriteString(fmt.Sprintf("  rejected: %d\n", summary.RejectedEntries))
		b.WriteString(fmt.Sprintf("  conflicted: %d\n", summary.ConflictedEntries))
		b.WriteString(fmt.Sprintf("  stale: %d\n", summary.StaleEntries))
	}
	b.WriteString("\nArtifacts\n")
	b.WriteString(fmt.Sprintf("  total: %d\n", summary.TotalArtifacts))
	b.WriteString(fmt.Sprintf("  missing: %d\n", summary.MissingArtifacts))
	return b.String()
}

func readJSONLFile[T any](path string) ([]T, error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer file.Close()

	var items []T
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var item T
		if err := json.Unmarshal([]byte(line), &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, scanner.Err()
}

func emptyPlaceholder(value string) string {
	if strings.TrimSpace(value) == "" {
		return "(unset)"
	}
	return value
}
