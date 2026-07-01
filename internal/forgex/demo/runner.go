package demo

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	forgexcontext "github.com/castwell/forge/internal/forgex/context"
	"github.com/castwell/forge/internal/forgex/failure"
	"github.com/castwell/forge/internal/forgex/model"
	forgexpolicy "github.com/castwell/forge/internal/forgex/policy"
	"github.com/castwell/forge/internal/forgex/report"
	forgexstate "github.com/castwell/forge/internal/forgex/state"
	"github.com/castwell/forge/internal/forgex/stop"
	"github.com/castwell/forge/internal/forgex/storage"
	forgextask "github.com/castwell/forge/internal/forgex/task"
	"github.com/castwell/forge/internal/forgex/toolgw"
	"github.com/castwell/forge/internal/forgex/trace"
	"github.com/google/uuid"
)

// Default paths used when the caller does not override them.
const (
	DefaultTaxonomyPath   = "configs/forgex/failure_taxonomy.yaml"
	DefaultPolicyPath     = "configs/forgex/stop_policies.yaml"
	DefaultPacketPath     = "examples/forgex/task_packet_generic_contract_violation.yaml"
	DefaultContractsPath  = "configs/forgex/tool_contracts/generic_tool_contracts.yaml"
	DefaultToolPolicyPath = "configs/forgex/policies/safe_default.yaml"
	DefaultAuthorityLevel = ""
	defaultExpensiveTool  = "demo.expensive_generation"
)

// RunGenericContractViolationDemo runs the "empty required_assets" contract
// violation bad case end to end without calling any external API. It reads the
// task packet, creates a run, records a simulated demo.expensive_generation tool
// call, builds an ErrorEnvelope for the empty required_assets failure, classifies
// it with the failure taxonomy, decides on a stop action with the
// StopConditionEngine, and persists the run streams plus a Markdown report and
// bad-case YAML under root/runs/<run_id>/. It returns the generated run ID.
//
// Empty taxonomyPath/policyPath/packetPath fall back to the ForgeX defaults.
func RunGenericContractViolationDemo(ctx context.Context, root, taxonomyPath, policyPath, packetPath string) (string, error) {
	return RunGenericContractViolationDemoWithControl(ctx, root, taxonomyPath, policyPath, packetPath, DefaultContractsPath, DefaultToolPolicyPath, DefaultAuthorityLevel)
}

// RunGenericContractViolationDemoWithControl runs the contract violation bad case
// through the M3 tool gateway and policy controls. The tool call remains
// simulated: the demo never invokes a real external API.
func RunGenericContractViolationDemoWithControl(ctx context.Context, root, taxonomyPath, policyPath, packetPath, contractsPath, toolPolicyPath, authorityLevel string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if taxonomyPath == "" {
		taxonomyPath = DefaultTaxonomyPath
	}
	if policyPath == "" {
		policyPath = DefaultPolicyPath
	}
	if packetPath == "" {
		packetPath = DefaultPacketPath
	}
	if contractsPath == "" {
		contractsPath = DefaultContractsPath
	}
	if toolPolicyPath == "" {
		toolPolicyPath = DefaultToolPolicyPath
	}
	// 1. Read TaskPacket.
	packet, err := forgextask.LoadPacket(packetPath)
	if err != nil {
		return "", err
	}
	authorityLevel = effectiveAuthorityLevel(authorityLevel, packet)

	suitability := forgextask.EvaluatePacket(packet)

	// Load classification taxonomy, stop policies, tool contracts and tool
	// policies up front so a config error fails the demo before any artifacts are
	// written.
	taxonomy, err := failure.LoadTaxonomy(taxonomyPath)
	if err != nil {
		return "", err
	}
	policy, err := stop.LoadPolicy(policyPath)
	if err != nil {
		return "", err
	}
	contracts, err := toolgw.LoadContracts(contractsPath)
	if err != nil {
		return "", err
	}
	toolContract, err := contracts.MustGet(defaultExpensiveTool)
	if err != nil {
		return "", err
	}
	toolPolicy, err := forgexpolicy.LoadConfig(toolPolicyPath)
	if err != nil {
		return "", err
	}

	// 2. Create Run.
	now := time.Now().UTC()
	runID := "run_" + uuid.NewString()
	run := model.Run{
		ID:        runID,
		TaskID:    packet.ID,
		Name:      packet.Name,
		Status:    model.RunRunning,
		StartedAt: now,
	}

	// 3. InitRun.
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
			{ID: "validate_required_assets", Title: "Validate required assets", Status: model.ProgressFailed, Evidence: "required_assets is empty"},
			{ID: "generate_report", Title: "Generate report and badcase", Status: model.ProgressInProgress},
		},
		Decisions: []string{
			fmt.Sprintf("AgentSuitabilityGate=%s controls=%v", suitability.Decision, suitability.RequiredControls),
			"Tool call must pass policy decision and contract validation before external execution.",
			"Stop before paid generation when required_assets is empty.",
		},
		NextActions: []string{"Ask upstream to provide non-empty required assets."},
		UpdatedAt:   now,
	}
	if err := store.SaveProgressLedger(ctx, ledger); err != nil {
		return "", fmt.Errorf("save progress ledger: %w", err)
	}

	contextPack := forgexcontext.NewBudgetManager(128).Build(runID, "tool_contract_check", packet.Goal+"\nconstraints: "+fmt.Sprint(packet.Constraints), []string{"task_packet.yaml"})
	if err := store.AppendContextPack(ctx, contextPack); err != nil {
		return "", fmt.Errorf("append context pack: %w", err)
	}

	// 4. Record run_started.
	if err := recorder.Event(ctx, model.EventRunStarted, "run started", map[string]any{
		"task_id":              packet.ID,
		"goal":                 packet.Goal,
		"suitability_decision": suitability.Decision,
		"required_controls":    suitability.RequiredControls,
	}); err != nil {
		return "", fmt.Errorf("record run_started: %w", err)
	}

	// 5. Evaluate policy before the simulated tool call.
	args := map[string]any{"prompt": packet.Goal, "required_assets": []any{}}
	policyDecision := forgexpolicy.NewEngine(toolPolicy).Decide(runID, forgexpolicy.AuthorityLevel(authorityLevel), toolContract)
	modelPolicyDecision := toModelPolicyDecision(policyDecision)
	if err := store.AppendPolicyDecision(ctx, modelPolicyDecision); err != nil {
		return "", fmt.Errorf("append policy decision: %w", err)
	}

	// 6. Simulate the tool call: demo.expensive_generation with empty required_assets.
	// This records the attempt but never calls any external API.
	callID, err := recorder.ToolCallStarted(ctx, defaultExpensiveTool, args)
	if err != nil {
		return "", fmt.Errorf("record tool call: %w", err)
	}

	validationResults := toolgw.ValidateInputs(runID, toolContract, args)
	contractValidations := make([]model.ContractValidation, 0, len(validationResults))
	for _, result := range validationResults {
		validation := toModelContractValidation(result)
		contractValidations = append(contractValidations, validation)
		if err := store.AppendContractValidation(ctx, validation); err != nil {
			return "", fmt.Errorf("append contract validation: %w", err)
		}
	}

	missingAssetArtifact := forgexstate.NewArtifactRecord(runID, "required_asset", model.ArtifactMissing, "contract_validator", map[string]string{
		"reason":      "required_assets is empty",
		"tool":        defaultExpensiveTool,
		"input_key":   "required_assets",
		"required":    "true",
		"source_step": "contract_validation",
	})
	if err := store.AppendArtifact(ctx, missingAssetArtifact); err != nil {
		return "", fmt.Errorf("append artifact: %w", err)
	}

	claim := forgexstate.NewClaim(runID, "claim_"+uuid.NewString(), "required_assets.status", "contract_validator", map[string]any{
		"status": "missing",
		"reason": "required_assets is empty",
		"tool":   defaultExpensiveTool,
	}, []string{missingAssetArtifact.ID, "contract_validations.jsonl"})
	if err := store.AppendStateClaim(ctx, claim); err != nil {
		return "", fmt.Errorf("append state claim: %w", err)
	}
	worldState := forgexstate.AcceptClaim(model.WorldState{RunID: runID, Version: 1, UpdatedAt: now}, claim)
	if err := store.SaveWorldState(ctx, worldState); err != nil {
		return "", fmt.Errorf("save world state: %w", err)
	}

	// 7. Construct the ErrorEnvelope for the contract validation failure.
	toolErr := errors.New(firstValidationFailureMessage(contractValidations, "required_assets is empty"))
	if err := recorder.ToolCallFailed(ctx, callID, toolErr); err != nil {
		return "", fmt.Errorf("record tool failure: %w", err)
	}
	envelope := model.ErrorEnvelope{
		RunID:     runID,
		Source:    "tool_contract",
		Operation: defaultExpensiveTool,
		Message:   "required_assets is empty",
		RawError:  toolErr.Error(),
		Timestamp: time.Now().UTC(),
	}

	// 8. Classify with the failure taxonomy.
	envelope = failure.Classify(taxonomy, envelope)

	// 9a. Persist the classified error.
	if err := recorder.Error(ctx, envelope); err != nil {
		return "", fmt.Errorf("record error: %w", err)
	}

	// 9b. Convert validation failure into a termination signal and arbitrate.
	engine := stop.NewEngine(policy)
	engineDecision := engine.Decide(runID, envelope)
	contractSignal := stop.NewSignal(runID, stop.SignalSourceContractValidation, stop.SignalSeverityHigh, engineDecision.Action, "contract validation failed: required_assets_not_empty", []string{contractValidations[len(contractValidations)-1].ID, envelope.ID})
	modelStopSignal := toModelStopSignal(contractSignal)
	if err := store.AppendStopSignal(ctx, modelStopSignal); err != nil {
		return "", fmt.Errorf("append stop signal: %w", err)
	}
	decision := stop.NewArbiter().Decide(runID, []stop.StopSignal{contractSignal})
	decision.ErrorID = envelope.ID
	decision.Signals = stop.EvidenceSummary([]stop.StopSignal{contractSignal})

	// 9c. Persist the arbiter stop decision (also emits a stop_decided event).
	if err := recorder.StopDecision(ctx, decision); err != nil {
		return "", fmt.Errorf("record stop decision: %w", err)
	}

	// Reflect the decision on the run record.
	run.Status = statusForAction(decision.Action)
	run.EndedAt = time.Now().UTC()
	run.Summary = fmt.Sprintf("%s: %s", decision.Action, decision.Reason)
	if err := store.SaveRun(ctx, run); err != nil {
		return "", fmt.Errorf("save run: %w", err)
	}

	ledger.Checklist[3].Status = model.ProgressDone
	ledger.Checklist[3].Evidence = "report.md and badcase.yaml"
	ledger.UpdatedAt = run.EndedAt
	if err := store.SaveProgressLedger(ctx, ledger); err != nil {
		return "", fmt.Errorf("update progress ledger: %w", err)
	}

	// Build the snapshot from the in-memory objects we recorded.
	snapshot := report.RunSnapshot{
		Run:        run,
		TaskPacket: packet,
		Events: []model.Event{
			{Type: model.EventRunStarted, Message: "run started", Timestamp: now},
			{Type: model.EventToolCalled, Message: "tool called: " + defaultExpensiveTool, Timestamp: now},
			{Type: model.EventToolFailed, Message: "tool failed: " + defaultExpensiveTool, Timestamp: now},
			{Type: model.EventStopDecided, Message: "stop decision: " + string(decision.Action), Timestamp: run.EndedAt},
		},
		ToolCalls: []model.ToolCall{
			{
				ID:        callID,
				RunID:     runID,
				ToolName:  defaultExpensiveTool,
				Args:      args,
				Error:     toolErr.Error(),
				StartedAt: now,
				EndedAt:   run.EndedAt,
			},
		},
		PolicyDecisions:     []model.PolicyDecision{modelPolicyDecision},
		ContractValidations: contractValidations,
		WorldState:          &worldState,
		StateClaims:         []model.StateClaim{claim},
		Artifacts:           []model.ArtifactRecord{missingAssetArtifact},
		Errors:              []model.ErrorEnvelope{envelope},
		StopSignals:         []model.StopSignalRecord{modelStopSignal},
		StopDecisions:       []model.StopDecision{decision},
		ProgressLedger:      &ledger,
		ContextPacks:        []model.ContextPack{contextPack},
	}

	// 10. Generate the Markdown report.
	markdown := report.GenerateMarkdown(snapshot)
	if err := store.WriteReport(ctx, runID, markdown); err != nil {
		return "", fmt.Errorf("write report: %w", err)
	}
	if err := recorder.Event(ctx, model.EventReportGenerated, "report generated", nil); err != nil {
		return "", fmt.Errorf("record report_generated: %w", err)
	}

	// 11. Generate the bad-case YAML.
	badcase, err := report.GenerateBadCaseYAML(snapshot)
	if err != nil {
		return "", fmt.Errorf("generate bad case: %w", err)
	}
	if err := store.WriteBadCase(ctx, runID, badcase); err != nil {
		return "", fmt.Errorf("write bad case: %w", err)
	}

	if err := recorder.Event(ctx, model.EventRunFinished, "run finished", map[string]any{
		"status": string(run.Status),
	}); err != nil {
		return "", fmt.Errorf("record run_finished: %w", err)
	}

	// 12. Return the run ID.
	return runID, nil
}

func effectiveAuthorityLevel(override string, packet model.TaskPacket) string {
	override = strings.TrimSpace(override)
	if override != "" {
		return override
	}
	if strings.TrimSpace(packet.Authority) != "" {
		return packet.Authority
	}
	return string(forgexpolicy.AuthorityL0)
}

func toModelPolicyDecision(decision forgexpolicy.Decision) model.PolicyDecision {
	return model.PolicyDecision{
		ID:           decision.ID,
		RunID:        decision.RunID,
		ToolName:     decision.ToolName,
		Action:       string(decision.Action),
		Reason:       decision.Reason,
		RiskLevel:    decision.RiskLevel,
		SideEffect:   decision.SideEffect,
		Authority:    string(decision.Authority),
		RequiresHITL: decision.RequiresHITL,
		CreatedAt:    decision.CreatedAt,
	}
}

func toModelStopSignal(signal stop.StopSignal) model.StopSignalRecord {
	return model.StopSignalRecord{
		ID:        signal.ID,
		RunID:     signal.RunID,
		Source:    string(signal.Source),
		Severity:  string(signal.Severity),
		Suggested: signal.Suggested,
		Reason:    signal.Reason,
		Evidence:  append([]string(nil), signal.Evidence...),
		CreatedAt: signal.CreatedAt,
	}
}

func toModelContractValidation(result toolgw.ValidationResult) model.ContractValidation {
	return model.ContractValidation{
		ID:        result.ID,
		RunID:     result.RunID,
		ToolName:  result.ToolName,
		Status:    string(result.Status),
		Validator: result.Validator,
		Message:   result.Message,
		CreatedAt: result.CreatedAt,
	}
}

func firstValidationFailureMessage(validations []model.ContractValidation, fallback string) string {
	for _, validation := range validations {
		if validation.Status == string(toolgw.ValidationFailed) && validation.Message != "" {
			return validation.Message
		}
	}
	return fallback
}

// statusForAction maps a stop action to the terminal run status it implies.
func statusForAction(action model.StopAction) model.RunStatus {
	switch action {
	case model.StopActionStop:
		return model.RunStopped
	case model.StopActionEscalate:
		return model.RunEscalated
	case model.StopActionPause:
		return model.RunPaused
	case model.StopActionContinue, model.StopActionRetry:
		return model.RunRunning
	default:
		return model.RunFailed
	}
}
