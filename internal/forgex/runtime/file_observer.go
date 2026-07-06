package runtime

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/castwell/forge/internal/forgex/eval"
	"github.com/castwell/forge/internal/forgex/failure"
	"github.com/castwell/forge/internal/forgex/lessons"
	"github.com/castwell/forge/internal/forgex/model"
	"github.com/castwell/forge/internal/forgex/policy"
	"github.com/castwell/forge/internal/forgex/report"
	forgexstorage "github.com/castwell/forge/internal/forgex/storage"
	"github.com/castwell/forge/internal/forgex/toolgw"
	forgestorage "github.com/castwell/forge/internal/storage"
)

// FileObserver persists Forge runtime events into ForgeX local run artifacts.
// It is observe-only: callers should treat errors as warnings and keep Forge
// workflow execution independent from this observer.
type FileObserver struct {
	store     *forgexstorage.FileStore
	indexPath string
	autoIndex bool
	authority policy.AuthorityLevel
	contracts *toolgw.Registry
	policy    *policy.Engine
	taxonomy  *failure.Taxonomy
	evalRules string
	evalSuite string

	mu      sync.Mutex
	writeMu sync.Mutex
	runs    map[string]observedRun
}

type observedRun struct {
	RunID     string
	StartedAt time.Time
}

// FileObserverConfig configures a FileObserver.
type FileObserverConfig struct {
	// Root is the ForgeX artifact root. Empty defaults to .forgex-runtime.
	Root string
	// AutoIndex indexes a terminal run into Root/index.db.
	AutoIndex bool
	// Authority is used for observe-only policy shadow decisions. Empty defaults to L0.
	Authority string
	// Contracts optionally enables ToolContract shadow validation for matching handlers.
	Contracts *toolgw.Registry
	// Policy optionally customizes policy shadow decisions. Nil means safe defaults.
	Policy *policy.Engine
	// Taxonomy optionally classifies shadow validation and worker errors.
	Taxonomy *failure.Taxonomy
	// EvalRules and EvalSuite enable explicit terminal auto-eval. Both are required.
	EvalRules string
	EvalSuite string
}

// NewFileObserver creates a FileObserver backed by a ForgeX FileStore.
func NewFileObserver(cfg FileObserverConfig) *FileObserver {
	root := cfg.Root
	if root == "" {
		root = ".forgex-runtime"
	}
	return &FileObserver{
		store:     forgexstorage.NewFileStore(root),
		indexPath: filepath.Join(root, "index.db"),
		autoIndex: cfg.AutoIndex,
		authority: policy.NormalizeAuthority(policy.AuthorityLevel(cfg.Authority)),
		contracts: cfg.Contracts,
		policy:    cfg.Policy,
		taxonomy:  cfg.Taxonomy,
		evalRules: cfg.EvalRules,
		evalSuite: cfg.EvalSuite,
		runs:      make(map[string]observedRun),
	}
}

// Store returns the underlying ForgeX file store, primarily for tests and CLI
// diagnostics.
func (o *FileObserver) Store() *forgexstorage.FileStore {
	return o.store
}

// ObserveTaskCall records the Coordinator-side Worker ExecuteTask RPC as a
// ForgeX tool call. It does not validate or block the call.
func (o *FileObserver) ObserveTaskCall(ctx context.Context, call TaskCall) error {
	o.writeMu.Lock()
	defer o.writeMu.Unlock()

	if err := ctx.Err(); err != nil {
		return err
	}
	if call.WorkflowID == "" || call.TaskID == "" {
		return fmt.Errorf("runtime observer: workflow_id and task_id are required for task call")
	}
	event := &forgestorage.Event{WorkflowID: call.WorkflowID, TaskID: call.TaskID, Type: forgestorage.EventTaskStarted, Timestamp: call.StartedAt}
	if _, err := o.ensureRun(ctx, event); err != nil {
		return err
	}
	toolCall := taskCallToToolCall(call)
	if err := o.store.AppendToolCall(ctx, toolCall); err != nil {
		return err
	}
	if !call.Success && call.Error != "" {
		if err := o.appendClassifiedError(ctx, model.ErrorEnvelope{
			ID:        fmt.Sprintf("forge_err_%s", call.TaskID),
			RunID:     toolCall.RunID,
			Source:    "forge_worker",
			Operation: call.Handler,
			Message:   call.Error,
			RawError:  call.Error,
			Timestamp: toolCall.EndedAt,
		}); err != nil {
			return err
		}
	}
	return o.shadowValidateTaskCall(ctx, call, toolCall)
}

// ObserveEvent records one Forge runtime event as a ForgeX event and updates the
// run metadata/report on terminal workflow events.
func (o *FileObserver) ObserveEvent(ctx context.Context, event *forgestorage.Event) error {
	o.writeMu.Lock()
	defer o.writeMu.Unlock()

	if err := ctx.Err(); err != nil {
		return err
	}
	if event == nil {
		return fmt.Errorf("runtime observer: nil event")
	}
	if event.WorkflowID == "" {
		return fmt.Errorf("runtime observer: workflow_id is required")
	}

	observed, err := o.ensureRun(ctx, event)
	if err != nil {
		return err
	}
	if err := o.store.AppendEvent(ctx, eventToForgeX(event)); err != nil {
		return err
	}

	if status, ok := terminalRunStatus(event.Type); ok {
		endedAt := event.Timestamp
		if endedAt.IsZero() {
			endedAt = time.Now().UTC()
		}
		run := initialRunForWorkflow(event.WorkflowID, event)
		run.ID = observed.RunID
		run.StartedAt = observed.StartedAt
		run.Status = status
		run.EndedAt = endedAt.UTC()
		run.Summary = fmt.Sprintf("Forge workflow %s finished with status %s via %s.", event.WorkflowID, status, event.Type)
		if err := o.store.SaveRun(ctx, run); err != nil {
			return err
		}
		if event.Type == forgestorage.EventWorkflowFailed {
			if err := o.store.AppendStopDecision(ctx, model.StopDecision{
				ID:        "forge_stop_" + event.WorkflowID,
				RunID:     run.ID,
				Action:    model.StopActionStop,
				Reason:    "Forge workflow failed; runtime observer recorded terminal failure",
				DecidedAt: endedAt.UTC(),
			}); err != nil {
				return err
			}
		}
		if err := o.postProcessTerminalRun(ctx, run.ID, event.WorkflowID, status, observed.StartedAt, endedAt.UTC(), event.Type); err != nil {
			return err
		}
	}
	return nil
}

func (o *FileObserver) postProcessTerminalRun(ctx context.Context, runID, workflowID string, status model.RunStatus, startedAt, endedAt time.Time, terminalEvent forgestorage.EventType) error {
	runDir := o.store.Layout().RunDir(runID)
	if o.evalRules != "" && o.evalSuite != "" {
		if _, err := eval.Run(ctx, runDir, o.evalRules, o.evalSuite); err != nil {
			if appendErr := o.appendClassifiedError(ctx, model.ErrorEnvelope{
				ID:        fmt.Sprintf("forge_eval_err_%d", endedAt.UnixNano()),
				RunID:     runID,
				Source:    "forgex_eval",
				Operation: "runtime_auto_eval",
				Message:   err.Error(),
				RawError:  err.Error(),
				Timestamp: endedAt,
			}); appendErr != nil {
				return appendErr
			}
		}
	}
	artifacts, err := eval.LoadRunArtifacts(runDir)
	if err != nil {
		return err
	}
	snapshot := report.RunSnapshot{
		Run:                 artifacts.Run,
		TaskPacket:          artifacts.TaskPacket,
		Events:              artifacts.Events,
		ToolCalls:           artifacts.ToolCalls,
		PolicyDecisions:     artifacts.PolicyDecisions,
		ContractValidations: artifacts.ContractValidations,
		WorldState:          &artifacts.WorldState,
		StateClaims:         artifacts.StateClaims,
		Artifacts:           artifacts.Artifacts,
		Errors:              artifacts.Errors,
		StopSignals:         artifacts.StopSignals,
		StopDecisions:       artifacts.StopDecisions,
	}
	for _, lesson := range lessons.Derive(snapshot) {
		if err := o.store.AppendLesson(ctx, lesson); err != nil {
			return err
		}
		snapshot.Lessons = append(snapshot.Lessons, lesson)
	}
	if len(snapshot.Errors) > 0 && len(snapshot.StopDecisions) > 0 {
		badcase, err := report.GenerateBadCaseYAML(snapshot)
		if err != nil {
			return err
		}
		if err := o.store.WriteBadCase(ctx, runID, badcase); err != nil {
			return err
		}
	}
	markdown := report.GenerateMarkdown(snapshot)
	if markdown == "" {
		markdown = renderReport(runID, workflowID, status, startedAt, endedAt, terminalEvent)
	}
	if err := o.store.WriteReport(ctx, runID, markdown); err != nil {
		return err
	}
	if err := o.store.AppendEvent(ctx, reportGeneratedEvent(runID, workflowID, terminalEvent, endedAt)); err != nil {
		return err
	}
	if o.autoIndex {
		if err := o.indexRun(ctx, runID); err != nil {
			return err
		}
	}
	return nil
}

func (o *FileObserver) shadowValidateTaskCall(ctx context.Context, call TaskCall, toolCall model.ToolCall) error {
	if o.contracts == nil {
		return nil
	}
	contract, ok := o.contracts.Get(call.Handler)
	if !ok {
		return nil
	}
	engine := o.policy
	if engine == nil {
		engine = policy.NewEngine(nil)
	}
	decision := engine.Decide(toolCall.RunID, o.authority, contract)
	if err := o.store.AppendPolicyDecision(ctx, model.PolicyDecision{
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
	}); err != nil {
		return err
	}
	for _, result := range toolgw.ValidateInputs(toolCall.RunID, contract, toolCall.Args) {
		if err := o.appendContractValidation(ctx, result); err != nil {
			return err
		}
		if err := o.appendValidationErrorIfFailed(ctx, result); err != nil {
			return err
		}
	}
	if call.Success {
		for _, result := range toolgw.ValidateOutputs(toolCall.RunID, contract, toolCall.Result) {
			if err := o.appendContractValidation(ctx, result); err != nil {
				return err
			}
			if err := o.appendValidationErrorIfFailed(ctx, result); err != nil {
				return err
			}
		}
	}
	return nil
}

func (o *FileObserver) appendContractValidation(ctx context.Context, result toolgw.ValidationResult) error {
	return o.store.AppendContractValidation(ctx, model.ContractValidation{
		ID:        result.ID,
		RunID:     result.RunID,
		ToolName:  result.ToolName,
		Status:    string(result.Status),
		Validator: result.Validator,
		Message:   result.Message,
		CreatedAt: result.CreatedAt,
	})
}

func (o *FileObserver) appendValidationErrorIfFailed(ctx context.Context, result toolgw.ValidationResult) error {
	if result.Status != toolgw.ValidationFailed {
		return nil
	}
	return o.appendClassifiedError(ctx, model.ErrorEnvelope{
		ID:        "err_" + result.ID,
		RunID:     result.RunID,
		Source:    "tool_contract",
		Operation: result.ToolName,
		Message:   result.Message,
		RawError:  result.Message,
		Timestamp: result.CreatedAt,
	})
}

func (o *FileObserver) appendClassifiedError(ctx context.Context, envelope model.ErrorEnvelope) error {
	if envelope.Timestamp.IsZero() {
		envelope.Timestamp = time.Now().UTC()
	}
	return o.store.AppendError(ctx, failure.Classify(o.taxonomy, envelope))
}

func (o *FileObserver) indexRun(ctx context.Context, runID string) error {
	idx, err := forgexstorage.OpenSQLiteIndex(o.indexPath)
	if err != nil {
		return err
	}
	defer idx.Close()
	return idx.IndexRunDir(ctx, o.store.Layout().RunDir(runID))
}

func (o *FileObserver) ensureRun(ctx context.Context, event *forgestorage.Event) (observedRun, error) {
	o.mu.Lock()
	if run, ok := o.runs[event.WorkflowID]; ok {
		o.mu.Unlock()
		return run, nil
	}
	o.mu.Unlock()

	observed := observedRun{
		RunID:     RunIDForWorkflow(event.WorkflowID),
		StartedAt: event.Timestamp.UTC(),
	}
	if observed.StartedAt.IsZero() {
		observed.StartedAt = time.Now().UTC()
	}

	run := initialRunForWorkflow(event.WorkflowID, event)
	run.ID = observed.RunID
	run.StartedAt = observed.StartedAt
	packet := taskPacketForWorkflow(event.WorkflowID, event)
	if err := o.store.InitRun(ctx, run, packet); err != nil {
		return observedRun{}, err
	}

	o.mu.Lock()
	if existing, ok := o.runs[event.WorkflowID]; ok {
		o.mu.Unlock()
		return existing, nil
	}
	o.runs[event.WorkflowID] = observed
	o.mu.Unlock()
	return observed, nil
}
