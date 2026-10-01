package demo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	forgexcontext "github.com/castwell/forge/internal/forgex/context"
	"github.com/castwell/forge/internal/forgex/failure"
	"github.com/castwell/forge/internal/forgex/lessons"
	"github.com/castwell/forge/internal/forgex/model"
	forgexpolicy "github.com/castwell/forge/internal/forgex/policy"
	"github.com/castwell/forge/internal/forgex/report"
	forgexstate "github.com/castwell/forge/internal/forgex/state"
	"github.com/castwell/forge/internal/forgex/stop"
	"github.com/castwell/forge/internal/forgex/storage"
	"github.com/castwell/forge/internal/forgex/toolgw"
	"github.com/castwell/forge/internal/forgex/trace"
)

// ScenarioConfig describes one replay of a registered case.
type ScenarioConfig struct {
	Root           string
	TaxonomyPath   string
	PolicyPath     string
	PacketPath     string
	ContractsPath  string
	ToolPolicyPath string
	AuthorityLevel string
}

// RunScenario executes a case end to end from its task packet.
//
// A scenario is data, not code. The packet declares the tool payload to simulate
// and, optionally, the result that payload produces; the outcome then follows
// from whether the payload satisfies the tool contract:
//
//	contract satisfied  -> artifact produced, no error, continue, run succeeded
//	contract violated   -> artifact missing, error envelope, stop, run stopped
//
// Nothing here is keyed on a case id, which is what lets a case promoted from a
// bad case be replayed without adding a Go function for it. The two original
// scenarios were hand-written scripts whose differences (arguments, validation
// steps, artifacts, stop reason) are all expressible in the packet, so they are
// now thin wrappers over this function.
func RunScenario(ctx context.Context, cfg ScenarioConfig) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}

	// Load and validate every config up front so a config error fails before any
	// run artifacts are written.
	inputs, err := loadDemoInputs(cfg.TaxonomyPath, cfg.PolicyPath, cfg.PacketPath, cfg.ContractsPath, cfg.ToolPolicyPath, cfg.AuthorityLevel)
	if err != nil {
		return "", err
	}
	packet := inputs.packet
	toolContract := inputs.contract

	args := scenarioArgs(packet)
	simulatedOutput := scenarioOutput(packet)

	// 1. Create the run and its artifact directory.
	now := time.Now().UTC()
	runID := "run_" + uuid.NewString()
	run := model.Run{
		ID:        runID,
		TaskID:    packet.ID,
		Name:      packet.Name,
		Status:    model.RunRunning,
		StartedAt: now,
	}
	store := storage.NewFileStore(cfg.Root)
	if err := store.InitRun(ctx, run, packet); err != nil {
		return "", fmt.Errorf("init run %s: %w", runID, err)
	}
	recorder := trace.NewRecorder(store, runID)

	// The ledger is written before validation, so the validation item only gets
	// its outcome once the contract has actually been checked. Pre-marking it, as
	// the two original scripts did, meant the artifact described a result it had
	// not observed yet.
	ledger := model.ProgressLedger{
		RunID:        runID,
		CurrentPhase: "contract_validation",
		Checklist: []model.ProgressItem{
			{ID: "load_packet", Title: "Load task packet", Status: model.ProgressDone, Evidence: cfg.PacketPath},
			{ID: "policy_check", Title: "Authorize tool call", Status: model.ProgressDone, Evidence: cfg.ToolPolicyPath},
			{ID: "validate_contract", Title: "Validate the declared payload", Status: model.ProgressInProgress},
			{ID: "generate_report", Title: "Generate report", Status: model.ProgressInProgress},
		},
		Decisions: []string{
			fmt.Sprintf("AgentSuitabilityGate=%s controls=%v", inputs.suitability.Decision, inputs.suitability.RequiredControls),
			"Tool call passed the policy decision before simulated execution.",
		},
		NextActions: []string{"None; run completed successfully."},
		UpdatedAt:   now,
	}
	const validateItem = 2
	if err := store.SaveProgressLedger(ctx, ledger); err != nil {
		return "", fmt.Errorf("save progress ledger: %w", err)
	}

	contextPack := forgexcontext.NewBudgetManager(128).Build(runID, "tool_contract_check",
		packet.Goal+"\nconstraints: "+fmt.Sprint(packet.Constraints), []string{"task_packet.yaml"})
	if err := store.AppendContextPack(ctx, contextPack); err != nil {
		return "", fmt.Errorf("append context pack: %w", err)
	}

	if err := recorder.Event(ctx, model.EventRunStarted, "run started", map[string]any{
		"task_id":              packet.ID,
		"goal":                 packet.Goal,
		"suitability_decision": inputs.suitability.Decision,
		"required_controls":    inputs.suitability.RequiredControls,
	}); err != nil {
		return "", fmt.Errorf("record run_started: %w", err)
	}

	// 2. Authorize the call before simulating it.
	policyDecision := forgexpolicy.NewEngine(inputs.toolPolicy).
		Decide(runID, forgexpolicy.AuthorityLevel(inputs.authorityLevel), toolContract)
	modelPolicyDecision := toModelPolicyDecision(policyDecision)
	if err := store.AppendPolicyDecision(ctx, modelPolicyDecision); err != nil {
		return "", fmt.Errorf("append policy decision: %w", err)
	}

	// 3. Simulate the tool call. Nothing external is ever invoked; the arguments
	// come from the packet, so what the scenario does is what the packet says.
	callID, err := recorder.ToolCallStarted(ctx, defaultExpensiveTool, args)
	if err != nil {
		return "", fmt.Errorf("record tool call: %w", err)
	}

	// 4. Validate the declared payload against the contract. Outputs are only
	// validated when the packet declares a simulated result, which is exactly how
	// the two original scenarios differed.
	validationResults := toolgw.ValidateInputs(runID, toolContract, args)
	if len(simulatedOutput) > 0 {
		validationResults = append(validationResults, toolgw.ValidateOutputs(runID, toolContract, simulatedOutput)...)
	}
	contractValidations := make([]model.ContractValidation, 0, len(validationResults))
	violated := false
	for _, result := range validationResults {
		validation := toModelContractValidation(result)
		contractValidations = append(contractValidations, validation)
		if result.Status == toolgw.ValidationFailed {
			violated = true
		}
		if err := store.AppendContractValidation(ctx, validation); err != nil {
			return "", fmt.Errorf("append contract validation: %w", err)
		}
	}

	// 5. Record the validation outcome on the ledger, then the artifacts that
	// outcome implies.
	ledgerItem := &ledger.Checklist[validateItem]
	if violated {
		ledgerItem.Status = model.ProgressFailed
		ledgerItem.Evidence = "contract validation failed"
	} else {
		ledgerItem.Status = model.ProgressDone
		ledgerItem.Evidence = "contract validation passed"
	}
	if err := store.SaveProgressLedger(ctx, ledger); err != nil {
		return "", fmt.Errorf("update progress ledger: %w", err)
	}

	artifacts, err := recordScenarioArtifacts(ctx, store, runID, callID, args, simulatedOutput, violated)
	if err != nil {
		return "", err
	}

	// 6. Accept the world-state claim for whichever outcome we recorded, moving it
	// through the Claim -> permission -> validation -> Fact pipeline rather than
	// writing state directly.
	claim := scenarioClaim(runID, simulatedOutput, artifacts, violated)
	if err := store.AppendStateClaim(ctx, claim); err != nil {
		return "", fmt.Errorf("append state claim: %w", err)
	}
	outcome := forgexstate.SubmitClaim(ctx, forgexstate.SubmitInput{
		World:     model.WorldState{RunID: runID, Version: 1, UpdatedAt: now},
		Actor:     claim.Producer,
		Claim:     claim,
		Authority: inputs.stateAuthority,
		Classify:  func(env model.ErrorEnvelope) model.ErrorEnvelope { return failure.Classify(inputs.taxonomy, env) },
	})
	if err := persistClaimOutcome(ctx, store, runID, outcome); err != nil {
		return "", err
	}
	if outcome.Rejected {
		return "", fmt.Errorf("state claim rejected: %s", outcome.Claim.Reason)
	}
	worldState := outcome.World
	if err := store.SaveWorldState(ctx, worldState); err != nil {
		return "", fmt.Errorf("save world state: %w", err)
	}

	// 7. Close the tool call, and on a violation build the error envelope and
	// classify it through the failure taxonomy.
	var envelopes []model.ErrorEnvelope
	if violated {
		// The message is the real validation failure. The failure taxonomy matches
		// on it, so it has to name the missing key and say the value was empty.
		toolErr := errors.New(firstValidationFailureMessage(contractValidations, "contract validation failed"))
		if err := recorder.ToolCallFailed(ctx, callID, toolErr); err != nil {
			return "", fmt.Errorf("record tool failure: %w", err)
		}
		envelope := model.ErrorEnvelope{
			RunID:     runID,
			Source:    "tool_contract",
			Operation: defaultExpensiveTool,
			Message:   toolErr.Error(),
			RawError:  toolErr.Error(),
			Timestamp: time.Now().UTC(),
		}
		envelope = failure.Classify(inputs.taxonomy, envelope)
		envelopes = append(envelopes, envelope)
		if err := store.AppendError(ctx, envelope); err != nil {
			return "", fmt.Errorf("append error envelope: %w", err)
		}
	} else {
		if err := recorder.ToolCallFinished(ctx, callID, simulatedOutput); err != nil {
			return "", fmt.Errorf("record tool success: %w", err)
		}
	}

	// 8. Emit the termination signal the outcome implies and arbitrate it.
	var signal stop.StopSignal
	if violated {
		envelope := envelopes[0]
		engineDecision := stop.NewEngine(inputs.stopPolicy).Decide(runID, envelope)
		signal = stop.NewSignal(runID, stop.SignalSourceContractValidation, stop.SignalSeverityHigh,
			engineDecision.Action, "contract validation failed: "+envelope.Message,
			[]string{contractValidations[len(contractValidations)-1].ID, envelope.ID})
	} else {
		signal = stop.NewSignal(runID, stop.SignalSourceLLMSuggestedDone, stop.SignalSeverityLow,
			model.StopActionContinue, "contract satisfied and result produced; agent reports done",
			[]string{artifacts[len(artifacts)-1].ID, contractValidations[len(contractValidations)-1].ID})
	}
	modelStopSignal := toModelStopSignal(signal)
	if err := store.AppendStopSignal(ctx, modelStopSignal); err != nil {
		return "", fmt.Errorf("append stop signal: %w", err)
	}
	decision := stop.NewArbiter().Decide(runID, []stop.StopSignal{signal})
	decision.Signals = stop.EvidenceSummary([]stop.StopSignal{signal})
	if err := recorder.StopDecision(ctx, decision); err != nil {
		return "", fmt.Errorf("record stop decision: %w", err)
	}

	// 9. Reflect the outcome on the run record.
	run.Status = model.RunSucceeded
	if violated {
		run.Status = model.RunStopped
	}
	run.EndedAt = time.Now().UTC()
	run.Summary = fmt.Sprintf("%s: %s", decision.Action, decision.Reason)
	if err := store.SaveRun(ctx, run); err != nil {
		return "", fmt.Errorf("save run: %w", err)
	}

	ledger.CurrentPhase = "reporting"
	ledger.Checklist[len(ledger.Checklist)-1].Status = model.ProgressDone
	ledger.Checklist[len(ledger.Checklist)-1].Evidence = scenarioReportEvidence(violated)
	ledger.UpdatedAt = run.EndedAt
	if err := store.SaveProgressLedger(ctx, ledger); err != nil {
		return "", fmt.Errorf("update progress ledger: %w", err)
	}

	// 10. Report. Only a halting run derives lessons and a bad case.
	snapshot := scenarioSnapshot(scenarioSnapshotInput{
		Run:            run,
		Packet:         packet,
		Now:            now,
		CallID:         callID,
		Args:           args,
		Output:         simulatedOutput,
		PolicyDecision: modelPolicyDecision,
		Validations:    contractValidations,
		WorldState:     worldState,
		Claim:          claim,
		Artifacts:      artifacts,
		StopSignal:     modelStopSignal,
		StopDecision:   decision,
		Ledger:         ledger,
		ContextPack:    contextPack,
		Errors:         envelopes,
		Violated:       violated,
	})

	derivedLessons := lessons.Derive(snapshot)
	for _, lesson := range derivedLessons {
		if err := store.AppendLesson(ctx, lesson); err != nil {
			return "", fmt.Errorf("append lesson: %w", err)
		}
	}
	snapshot.Lessons = derivedLessons

	if err := store.WriteReport(ctx, runID, report.GenerateMarkdown(snapshot)); err != nil {
		return "", fmt.Errorf("write report: %w", err)
	}
	if err := recorder.Event(ctx, model.EventReportGenerated, "report generated", nil); err != nil {
		return "", fmt.Errorf("record report_generated: %w", err)
	}
	if violated {
		badcase, err := report.GenerateBadCaseYAML(snapshot)
		if err != nil {
			return "", fmt.Errorf("generate bad case: %w", err)
		}
		if err := store.WriteBadCase(ctx, runID, badcase); err != nil {
			return "", fmt.Errorf("write bad case: %w", err)
		}
	}
	if err := recorder.Event(ctx, model.EventRunFinished, "run finished", map[string]any{
		"status": string(run.Status),
	}); err != nil {
		return "", fmt.Errorf("record run_finished: %w", err)
	}

	return runID, nil
}

// scenarioArgs builds the simulated tool arguments from the packet.
//
// The packet has always carried these under inputs.tool_payload; the original
// scenarios ignored that and hardcoded the values, which is why a case could not
// be replayed without writing Go for it.
func scenarioArgs(packet model.TaskPacket) map[string]any {
	args := map[string]any{"prompt": packet.Goal}
	for key, value := range scenarioMap(packet.Inputs["tool_payload"]) {
		args[key] = value
	}
	return args
}

// scenarioOutput returns the simulated tool result, or nil when the packet does
// not declare one. A packet without a result skips output validation.
func scenarioOutput(packet model.TaskPacket) map[string]any {
	return scenarioMap(packet.Inputs["tool_result"])
}

func scenarioMap(value any) map[string]any {
	if m, ok := value.(map[string]any); ok {
		return m
	}
	return nil
}

// firstAssetURI returns the first entry of the declared required assets, which is
// what the produced artifact points at.
func firstAssetURI(args map[string]any) string {
	assets, ok := args["required_assets"].([]any)
	if !ok || len(assets) == 0 {
		return ""
	}
	first, ok := assets[0].(string)
	if !ok {
		return ""
	}
	return first
}
