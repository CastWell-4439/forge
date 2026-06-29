package demo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/castwell/forge/internal/forgex/failure"
	"github.com/castwell/forge/internal/forgex/model"
	"github.com/castwell/forge/internal/forgex/report"
	"github.com/castwell/forge/internal/forgex/stop"
	"github.com/castwell/forge/internal/forgex/storage"
	"github.com/castwell/forge/internal/forgex/trace"
	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

// Default paths used when the caller does not override them.
const (
	DefaultTaxonomyPath = "configs/forgex/failure_taxonomy.yaml"
	DefaultPolicyPath   = "configs/forgex/stop_policies.yaml"
	DefaultPacketPath   = "examples/forgex/task_packet_aihook_empty_images_refs.yaml"
)

// RunAIHookEmptyImagesRefsDemo runs the AIhook "empty images_refs" bad case end
// to end without calling any external API. It reads the task packet, creates a
// run, records a simulated vidu.reference2video tool call, builds an
// ErrorEnvelope for the empty images_refs failure, classifies it with the
// failure taxonomy, decides on a stop action with the StopConditionEngine, and
// persists the run streams plus a Markdown report and bad-case YAML under
// root/runs/<run_id>/. It returns the generated run ID.
//
// Empty taxonomyPath/policyPath/packetPath fall back to the ForgeX defaults.
func RunAIHookEmptyImagesRefsDemo(ctx context.Context, root, taxonomyPath, policyPath, packetPath string) (string, error) {
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

	// 1. Read TaskPacket.
	packet, err := loadTaskPacket(packetPath)
	if err != nil {
		return "", err
	}

	// Load classification taxonomy and stop policies up front so a config error
	// fails the demo before any artifacts are written.
	taxonomy, err := failure.LoadTaxonomy(taxonomyPath)
	if err != nil {
		return "", err
	}
	policy, err := stop.LoadPolicy(policyPath)
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

	// 4. Record run_started.
	if err := recorder.Event(ctx, model.EventRunStarted, "run started", map[string]any{
		"task_id": packet.ID,
		"goal":    packet.Goal,
	}); err != nil {
		return "", fmt.Errorf("record run_started: %w", err)
	}

	// 5. Simulate the tool call: vidu.reference2video with empty images_refs.
	// This is a pure simulation — no external request is made.
	args := map[string]any{"images_refs": []any{}}
	callID, err := recorder.ToolCallStarted(ctx, "vidu.reference2video", args)
	if err != nil {
		return "", fmt.Errorf("record tool call: %w", err)
	}

	// 6. Construct the ErrorEnvelope for the empty images_refs failure.
	toolErr := errors.New("images_refs is empty")
	if err := recorder.ToolCallFailed(ctx, callID, toolErr); err != nil {
		return "", fmt.Errorf("record tool failure: %w", err)
	}
	envelope := model.ErrorEnvelope{
		RunID:     runID,
		Source:    "tool",
		Operation: "vidu.reference2video",
		Message:   "images_refs is empty",
		RawError:  toolErr.Error(),
		Timestamp: time.Now().UTC(),
	}

	// 7. Classify with the failure taxonomy.
	envelope = failure.Classify(taxonomy, envelope)

	// 9a. Persist the classified error.
	if err := recorder.Error(ctx, envelope); err != nil {
		return "", fmt.Errorf("record error: %w", err)
	}

	// 8. Decide with the StopConditionEngine.
	engine := stop.NewEngine(policy)
	decision := engine.Decide(runID, envelope)

	// 9b. Persist the stop decision (also emits a stop_decided event).
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

	// Build the snapshot from the in-memory objects we recorded.
	snapshot := report.RunSnapshot{
		Run:        run,
		TaskPacket: packet,
		Events: []model.Event{
			{Type: model.EventRunStarted, Message: "run started", Timestamp: now},
			{Type: model.EventToolCalled, Message: "tool called: vidu.reference2video", Timestamp: now},
			{Type: model.EventToolFailed, Message: "tool failed: vidu.reference2video", Timestamp: now},
			{Type: model.EventStopDecided, Message: "stop decision: " + string(decision.Action), Timestamp: run.EndedAt},
		},
		ToolCalls: []model.ToolCall{
			{
				ID:        callID,
				RunID:     runID,
				ToolName:  "vidu.reference2video",
				Args:      args,
				Error:     toolErr.Error(),
				StartedAt: now,
				EndedAt:   run.EndedAt,
			},
		},
		Errors:        []model.ErrorEnvelope{envelope},
		StopDecisions: []model.StopDecision{decision},
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

// loadTaskPacket reads and parses a TaskPacket YAML file.
func loadTaskPacket(path string) (model.TaskPacket, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return model.TaskPacket{}, fmt.Errorf("read task packet %s: %w", path, err)
	}
	var packet model.TaskPacket
	if err := yaml.Unmarshal(data, &packet); err != nil {
		return model.TaskPacket{}, fmt.Errorf("parse task packet %s: %w", path, err)
	}
	return packet, nil
}

// statusForAction maps a stop action to the terminal run status it implies.
func statusForAction(action model.StopAction) model.RunStatus {
	switch action {
	case model.StopActionStop:
		return model.RunStopped
	case model.StopActionEscalate:
		return model.RunEscalated
	case model.StopActionContinue, model.StopActionRetry:
		return model.RunRunning
	default:
		return model.RunFailed
	}
}
