package demo

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/castwell/forge/internal/forgex/model"
	"github.com/castwell/forge/internal/forgex/report"
	forgexstate "github.com/castwell/forge/internal/forgex/state"
	"github.com/castwell/forge/internal/forgex/storage"
)

// scenarioReportEvidence is the evidence recorded on the final checklist item.
func scenarioReportEvidence(violated bool) string {
	if violated {
		return "report.md and badcase.yaml"
	}
	return "report.md"
}

// recordScenarioArtifacts records what the validation outcome implies: a produced
// required asset when the contract held, a missing one when it did not, plus the
// generated result when the packet declared a simulated output.
func recordScenarioArtifacts(
	ctx context.Context,
	store *storage.FileStore,
	runID string,
	callID string,
	args map[string]any,
	simulatedOutput map[string]any,
	violated bool,
) ([]model.ArtifactRecord, error) {
	status := model.ArtifactProduced
	if violated {
		status = model.ArtifactMissing
	}
	asset := forgexstate.NewArtifactRecord(runID, "required_asset", status, "contract_validator", map[string]string{
		"tool":        defaultExpensiveTool,
		"input_key":   "required_assets",
		"source_step": "contract_validation",
	})
	asset.URI = firstAssetURI(args)
	if err := store.AppendArtifact(ctx, asset); err != nil {
		return nil, fmt.Errorf("append required asset artifact: %w", err)
	}
	artifacts := []model.ArtifactRecord{asset}

	if len(simulatedOutput) == 0 {
		return artifacts, nil
	}
	resultURI, _ := simulatedOutput["result_url"].(string)
	generated := forgexstate.NewArtifactRecord(runID, "generated_result", model.ArtifactValid, defaultExpensiveTool, map[string]string{
		"tool":         defaultExpensiveTool,
		"output_key":   "result_url",
		"source_step":  "tool_output_validation",
		"tool_call_id": callID,
	})
	generated.URI = resultURI
	generated.ToolCallID = callID
	if err := store.AppendArtifact(ctx, generated); err != nil {
		return nil, fmt.Errorf("append generated result artifact: %w", err)
	}
	return append(artifacts, generated), nil
}

// scenarioClaim builds the world-state claim for the recorded outcome. A violated
// contract claims the missing asset; a satisfied one claims the generated result.
func scenarioClaim(runID string, simulatedOutput map[string]any, artifacts []model.ArtifactRecord, violated bool) model.StateClaim {
	key := "generated_result.status"
	payload := map[string]any{
		"status":     "produced",
		"result_url": simulatedOutput["result_url"],
		"tool":       defaultExpensiveTool,
	}
	evidenceID := artifacts[len(artifacts)-1].ID
	if violated {
		key = "required_assets.status"
		payload = map[string]any{
			"status": "missing",
			"tool":   defaultExpensiveTool,
		}
		evidenceID = artifacts[0].ID
	}
	claim := forgexstate.NewClaim(runID, "claim_"+uuid.NewString(), key, "contract_validator",
		payload, []string{evidenceID, "contract_validations.jsonl"})
	claim.Scope = forgexstate.ScopeGlobal
	return claim
}

// scenarioSnapshotInput carries everything the report needs, as one value rather
// than a very long argument list.
type scenarioSnapshotInput struct {
	Run            model.Run
	Packet         model.TaskPacket
	Now            time.Time
	CallID         string
	Args           map[string]any
	Output         map[string]any
	PolicyDecision model.PolicyDecision
	Validations    []model.ContractValidation
	WorldState     model.WorldState
	Claim          model.StateClaim
	Artifacts      []model.ArtifactRecord
	StopSignal     model.StopSignalRecord
	StopDecision   model.StopDecision
	Ledger         model.ProgressLedger
	ContextPack    model.ContextPack
	Errors         []model.ErrorEnvelope
	Violated       bool
}

// scenarioSnapshot assembles the report snapshot. Its event list mirrors what the
// recorder wrote, so the report and the event stream tell the same story.
func scenarioSnapshot(in scenarioSnapshotInput) report.RunSnapshot {
	toolEvent := model.EventToolSucceeded
	toolMessage := "tool succeeded: " + defaultExpensiveTool
	if in.Violated {
		toolEvent = model.EventToolFailed
		toolMessage = "tool failed: " + defaultExpensiveTool
	}
	return report.RunSnapshot{
		Run:        in.Run,
		TaskPacket: in.Packet,
		Events: []model.Event{
			{Type: model.EventRunStarted, Message: "run started", Timestamp: in.Now},
			{Type: model.EventToolCalled, Message: "tool called: " + defaultExpensiveTool, Timestamp: in.Now},
			{Type: toolEvent, Message: toolMessage, Timestamp: in.Run.EndedAt},
			{Type: model.EventStopDecided, Message: "stop decision: " + string(in.StopDecision.Action), Timestamp: in.Run.EndedAt},
		},
		ToolCalls: []model.ToolCall{{
			ID:        in.CallID,
			RunID:     in.Run.ID,
			ToolName:  defaultExpensiveTool,
			Args:      in.Args,
			Result:    in.Output,
			StartedAt: in.Now,
			EndedAt:   in.Run.EndedAt,
		}},
		PolicyDecisions:     []model.PolicyDecision{in.PolicyDecision},
		ContractValidations: in.Validations,
		WorldState:          &in.WorldState,
		StateClaims:         []model.StateClaim{in.Claim},
		Artifacts:           in.Artifacts,
		StopSignals:         []model.StopSignalRecord{in.StopSignal},
		StopDecisions:       []model.StopDecision{in.StopDecision},
		ProgressLedger:      &in.Ledger,
		ContextPacks:        []model.ContextPack{in.ContextPack},
		Errors:              in.Errors,
	}
}
