package demo

import (
	"context"
	"fmt"
	"time"

	forgexcontext "github.com/castwell/forge/internal/forgex/context"
	"github.com/castwell/forge/internal/forgex/lessons"
	"github.com/castwell/forge/internal/forgex/model"
	forgexpolicy "github.com/castwell/forge/internal/forgex/policy"
	"github.com/castwell/forge/internal/forgex/report"
	forgexstate "github.com/castwell/forge/internal/forgex/state"
	"github.com/castwell/forge/internal/forgex/stop"
	"github.com/castwell/forge/internal/forgex/storage"
	"github.com/castwell/forge/internal/forgex/toolgw"
	"github.com/castwell/forge/internal/forgex/trace"
	"github.com/google/uuid"
)

// Local placeholders used by the happy-path demo. They stand in for real
// inputs/outputs so the demo never calls any external API.
const (
	successRequiredAsset0 = "local://required_asset_0"
	successRequiredAsset1 = "local://required_asset_1"
	successGeneratedURI   = "local://generated_result_0"
)

// RunGenericContractSuccessDemo runs the happy-path counterpart to the contract
// violation demo. It reads the success task packet, authorizes and simulates a
// demo.expensive_generation call whose required_assets are non-empty, validates
// the inputs and outputs against the tool contract, produces artifacts, accepts
// the resulting world-state claim and arbitrates a continue decision. It proves
// the ForgeX control plane does not always stop and that the control metrics do
// not false-positive on a clean run. It returns the generated run ID.
//
// Empty taxonomyPath/policyPath/packetPath fall back to the ForgeX defaults.
func RunGenericContractSuccessDemo(ctx context.Context, root, taxonomyPath, policyPath, packetPath string) (string, error) {
	return RunGenericContractSuccessDemoWithControl(ctx, root, taxonomyPath, policyPath, packetPath, DefaultContractsPath, DefaultToolPolicyPath, DefaultAuthorityLevel)
}

// RunGenericContractSuccessDemoWithControl runs the happy-path case through the
// M3 tool gateway and policy controls. The tool call remains simulated: the demo
// never invokes a real external API and only records local placeholders.
func RunGenericContractSuccessDemoWithControl(ctx context.Context, root, taxonomyPath, policyPath, packetPath, contractsPath, toolPolicyPath, authorityLevel string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if packetPath == "" {
		packetPath = DefaultSuccessPacketPath
	}
	if toolPolicyPath == "" {
		toolPolicyPath = DefaultToolPolicyPath
	}
	// Load and validate every config up front so a config error fails the demo
	// before any run artifacts are written.
	inputs, err := loadDemoInputs(taxonomyPath, policyPath, packetPath, contractsPath, toolPolicyPath, authorityLevel)
	if err != nil {
		return "", err
	}
	packet := inputs.packet
	authorityLevel = inputs.authorityLevel
	suitability := inputs.suitability
	toolContract := inputs.contract
	toolPolicy := inputs.toolPolicy

	// 1. Create Run.
	now := time.Now().UTC()
	runID := "run_" + uuid.NewString()
	run := model.Run{
		ID:        runID,
		TaskID:    packet.ID,
		Name:      packet.Name,
		Status:    model.RunRunning,
		StartedAt: now,
	}

	// 2. InitRun.
	store := storage.NewFileStore(root)
	if err := store.InitRun(ctx, run, packet); err != nil {
		return "", fmt.Errorf("init run %s: %w", runID, err)
	}
	recorder := trace.NewRecorder(store, runID)

	ledger := model.ProgressLedger{
		RunID:        runID,
		CurrentPhase: "contract_validation",
		Checklist: []model.ProgressItem{
			{ID: "load_packet", Title: "Load task packet", Status: model.ProgressDone, Evidence: packetPath},
			{ID: "policy_check", Title: "Authorize tool call", Status: model.ProgressDone, Evidence: toolPolicyPath},
			{ID: "validate_required_assets", Title: "Validate required assets", Status: model.ProgressDone, Evidence: "required_assets has 2 local placeholders"},
			{ID: "generate_result", Title: "Generate result and validate output", Status: model.ProgressDone, Evidence: "result_url produced"},
			{ID: "generate_report", Title: "Generate report", Status: model.ProgressInProgress},
		},
		Decisions: []string{
			fmt.Sprintf("AgentSuitabilityGate=%s controls=%v", suitability.Decision, suitability.RequiredControls),
			"Tool call passed the policy decision and contract validation before simulated execution.",
			"required_assets is non-empty so the generation contract is satisfied; continue.",
		},
		NextActions: []string{"None; run completed successfully."},
		UpdatedAt:   now,
	}
	if err := store.SaveProgressLedger(ctx, ledger); err != nil {
		return "", fmt.Errorf("save progress ledger: %w", err)
	}

	contextPack := forgexcontext.NewBudgetManager(128).Build(runID, "tool_contract_check", packet.Goal+"\nconstraints: "+fmt.Sprint(packet.Constraints), []string{"task_packet.yaml"})
	if err := store.AppendContextPack(ctx, contextPack); err != nil {
		return "", fmt.Errorf("append context pack: %w", err)
	}

	// 3. Record run_started.
	if err := recorder.Event(ctx, model.EventRunStarted, "run started", map[string]any{
		"task_id":              packet.ID,
		"goal":                 packet.Goal,
		"suitability_decision": suitability.Decision,
		"required_controls":    suitability.RequiredControls,
	}); err != nil {
		return "", fmt.Errorf("record run_started: %w", err)
	}

	// 4. Evaluate policy before the simulated tool call.
	requiredAssets := []any{successRequiredAsset0, successRequiredAsset1}
	args := map[string]any{"prompt": packet.Goal, "required_assets": requiredAssets}
	policyDecision := forgexpolicy.NewEngine(toolPolicy).Decide(runID, forgexpolicy.AuthorityLevel(authorityLevel), toolContract)
	modelPolicyDecision := toModelPolicyDecision(policyDecision)
	if err := store.AppendPolicyDecision(ctx, modelPolicyDecision); err != nil {
		return "", fmt.Errorf("append policy decision: %w", err)
	}

	// 5. Simulate the tool call: demo.expensive_generation with non-empty
	// required_assets. This records the attempt but never calls any external API.
	callID, err := recorder.ToolCallStarted(ctx, defaultExpensiveTool, args)
	if err != nil {
		return "", fmt.Errorf("record tool call: %w", err)
	}

	// 6. Validate inputs and the simulated local output against the contract.
	output := map[string]any{"result_url": successGeneratedURI}
	validationResults := append(
		toolgw.ValidateInputs(runID, toolContract, args),
		toolgw.ValidateOutputs(runID, toolContract, output)...,
	)
	contractValidations := make([]model.ContractValidation, 0, len(validationResults))
	for _, result := range validationResults {
		validation := toModelContractValidation(result)
		contractValidations = append(contractValidations, validation)
		if err := store.AppendContractValidation(ctx, validation); err != nil {
			return "", fmt.Errorf("append contract validation: %w", err)
		}
	}

	// 7. Record the produced artifacts: the resolved required asset and the
	// generated result.
	requiredAssetArtifact := forgexstate.NewArtifactRecord(runID, "required_asset", model.ArtifactProduced, "contract_validator", map[string]string{
		"tool":        defaultExpensiveTool,
		"input_key":   "required_assets",
		"source_step": "contract_validation",
	})
	requiredAssetArtifact.URI = successRequiredAsset0
	if err := store.AppendArtifact(ctx, requiredAssetArtifact); err != nil {
		return "", fmt.Errorf("append required asset artifact: %w", err)
	}
	generatedArtifact := forgexstate.NewArtifactRecord(runID, "generated_result", model.ArtifactValid, defaultExpensiveTool, map[string]string{
		"tool":         defaultExpensiveTool,
		"output_key":   "result_url",
		"source_step":  "tool_output_validation",
		"tool_call_id": callID,
	})
	generatedArtifact.URI = successGeneratedURI
	generatedArtifact.ToolCallID = callID
	if err := store.AppendArtifact(ctx, generatedArtifact); err != nil {
		return "", fmt.Errorf("append generated result artifact: %w", err)
	}

	// 8. Accept the world-state claim for the produced result.
	claim := forgexstate.NewClaim(runID, "claim_"+uuid.NewString(), "generated_result.status", "contract_validator", map[string]any{
		"status":     "produced",
		"result_url": successGeneratedURI,
		"tool":       defaultExpensiveTool,
	}, []string{generatedArtifact.ID, "contract_validations.jsonl"})
	if err := store.AppendStateClaim(ctx, claim); err != nil {
		return "", fmt.Errorf("append state claim: %w", err)
	}
	worldState := forgexstate.AcceptClaim(model.WorldState{RunID: runID, Version: 1, UpdatedAt: now}, claim)
	if err := store.SaveWorldState(ctx, worldState); err != nil {
		return "", fmt.Errorf("save world state: %w", err)
	}

	// 9. Record the successful tool completion. No error envelope is produced.
	if err := recorder.ToolCallFinished(ctx, callID, output); err != nil {
		return "", fmt.Errorf("record tool success: %w", err)
	}

	// 10. Emit a non-blocking completion signal and arbitrate. With no blocking
	// signals the arbiter selects continue, proving the control plane does not
	// always stop.
	completionSignal := stop.NewSignal(runID, stop.SignalSourceLLMSuggestedDone, stop.SignalSeverityLow, model.StopActionContinue, "contract satisfied and result produced; agent reports done", []string{generatedArtifact.ID, contractValidations[len(contractValidations)-1].ID})
	modelStopSignal := toModelStopSignal(completionSignal)
	if err := store.AppendStopSignal(ctx, modelStopSignal); err != nil {
		return "", fmt.Errorf("append stop signal: %w", err)
	}
	decision := stop.NewArbiter().Decide(runID, []stop.StopSignal{completionSignal})
	decision.Signals = stop.EvidenceSummary([]stop.StopSignal{completionSignal})
	if err := recorder.StopDecision(ctx, decision); err != nil {
		return "", fmt.Errorf("record stop decision: %w", err)
	}

	// 11. Reflect the successful completion on the run record. The model has no
	// dedicated "completed" status, so a clean continue maps to succeeded.
	run.Status = model.RunSucceeded
	run.EndedAt = time.Now().UTC()
	run.Summary = fmt.Sprintf("%s: %s", decision.Action, decision.Reason)
	if err := store.SaveRun(ctx, run); err != nil {
		return "", fmt.Errorf("save run: %w", err)
	}

	ledger.Checklist[4].Status = model.ProgressDone
	ledger.Checklist[4].Evidence = "report.md"
	ledger.UpdatedAt = run.EndedAt
	if err := store.SaveProgressLedger(ctx, ledger); err != nil {
		return "", fmt.Errorf("update progress ledger: %w", err)
	}

	// 12. Generate the Markdown report from the in-memory objects we recorded.
	snapshot := report.RunSnapshot{
		Run:        run,
		TaskPacket: packet,
		Events: []model.Event{
			{Type: model.EventRunStarted, Message: "run started", Timestamp: now},
			{Type: model.EventToolCalled, Message: "tool called: " + defaultExpensiveTool, Timestamp: now},
			{Type: model.EventToolSucceeded, Message: "tool succeeded: " + defaultExpensiveTool, Timestamp: run.EndedAt},
			{Type: model.EventStopDecided, Message: "stop decision: " + string(decision.Action), Timestamp: run.EndedAt},
		},
		ToolCalls: []model.ToolCall{
			{
				ID:        callID,
				RunID:     runID,
				ToolName:  defaultExpensiveTool,
				Args:      args,
				Result:    output,
				StartedAt: now,
				EndedAt:   run.EndedAt,
			},
		},
		PolicyDecisions:     []model.PolicyDecision{modelPolicyDecision},
		ContractValidations: contractValidations,
		WorldState:          &worldState,
		StateClaims:         []model.StateClaim{claim},
		Artifacts:           []model.ArtifactRecord{requiredAssetArtifact, generatedArtifact},
		StopSignals:         []model.StopSignalRecord{modelStopSignal},
		StopDecisions:       []model.StopDecision{decision},
		ProgressLedger:      &ledger,
		ContextPacks:        []model.ContextPack{contextPack},
	}
	// A clean run produces no lessons: Derive returns nil for a non-halting
	// outcome, so no lessons.jsonl is written and the report shows no
	// misleading bad case. The call documents that this is intentional.
	derivedLessons := lessons.Derive(snapshot)
	for _, lesson := range derivedLessons {
		if err := store.AppendLesson(ctx, lesson); err != nil {
			return "", fmt.Errorf("append lesson: %w", err)
		}
	}
	snapshot.Lessons = derivedLessons

	markdown := report.GenerateMarkdown(snapshot)
	if err := store.WriteReport(ctx, runID, markdown); err != nil {
		return "", fmt.Errorf("write report: %w", err)
	}
	if err := recorder.Event(ctx, model.EventReportGenerated, "report generated", nil); err != nil {
		return "", fmt.Errorf("record report_generated: %w", err)
	}

	if err := recorder.Event(ctx, model.EventRunFinished, "run finished", map[string]any{
		"status": string(run.Status),
	}); err != nil {
		return "", fmt.Errorf("record run_finished: %w", err)
	}

	// 13. Return the run ID.
	return runID, nil
}
