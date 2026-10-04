package demo

import (
	"context"
	"fmt"
	"time"

	"github.com/castwell/forge/internal/forgex/failure"
	"github.com/castwell/forge/internal/forgex/lessons"
	"github.com/castwell/forge/internal/forgex/model"
	forgexpolicy "github.com/castwell/forge/internal/forgex/policy"
	"github.com/castwell/forge/internal/forgex/report"
	"github.com/castwell/forge/internal/forgex/stop"
	"github.com/castwell/forge/internal/forgex/storage"
	"github.com/castwell/forge/internal/forgex/trace"
)

// barrierInput carries what the non-executing outcome needs to close a run.
type barrierInput struct {
	Store          *storage.FileStore
	Recorder       *trace.Recorder
	Run            model.Run
	Ledger         model.ProgressLedger
	Packet         model.TaskPacket
	Now            time.Time
	ContextPack    model.ContextPack
	PolicyDecision model.PolicyDecision
	Action         forgexpolicy.Action
	Mode           forgexpolicy.ExecutionMode
	Reason         string
	Taxonomy       *failure.Taxonomy
	StopPolicy     *stop.PolicyConfig
}

// finishWithoutExecution closes a run whose tool call the policy refused or
// held. It is the "governance actually gates execution" path: no tool call is
// recorded as started, no artifact is produced, and the run status, ledger and
// report all derive from the real decision.
//
// Block (deny) and hold (approval/pause/escalate) differ in outcome, not in
// whether the tool ran:
//
//	block -> error envelope -> classified -> run stopped   (a refusal)
//	hold  -> no error, approval pending    -> run paused   (a wait)
//
// Both are halting: an agent that was refused must not keep working as if it
// had permission, and one waiting on a human must not proceed without one.
func finishWithoutExecution(ctx context.Context, in barrierInput) (string, error) {
	run := in.Run
	ledger := in.Ledger
	blocked := in.Mode == forgexpolicy.ExecutionBlock

	// 1. Record the refusal as an event, so the run stream explains itself.
	message := fmt.Sprintf("tool call %s by policy: %s", string(in.Mode), in.Reason)
	if err := in.Recorder.Event(ctx, model.EventPolicyDecided, message, map[string]any{
		"policy_decision_id": in.PolicyDecision.ID,
		"action":             string(in.Action),
		"execution_mode":     string(in.Mode),
		"tool":               defaultExpensiveTool,
	}); err != nil {
		return "", fmt.Errorf("record policy outcome: %w", err)
	}

	// 2. A refusal produces a classified error envelope; a hold does not — a
	// pending approval is not a failure, and classifying it as one would put a
	// false lesson into the corpus.
	var envelopes []model.ErrorEnvelope
	if blocked {
		envelope := model.ErrorEnvelope{
			RunID:     run.ID,
			Source:    "policy_decision",
			Operation: defaultExpensiveTool,
			Message:   fmt.Sprintf("policy denied tool call: %s", in.Reason),
			RawError:  fmt.Sprintf("policy_decision_id=%s action=%s authority=%s", in.PolicyDecision.ID, in.Action, in.PolicyDecision.Authority),
			Timestamp: time.Now().UTC(),
		}
		envelope = failure.Classify(in.Taxonomy, envelope)
		envelopes = append(envelopes, envelope)
		if err := in.Store.AppendError(ctx, envelope); err != nil {
			return "", fmt.Errorf("append policy error: %w", err)
		}
	}

	// 3. Stop signal + arbitration. The source is policy_decision and the
	// suggested action comes from the shared Action table, so a deny can only
	// ever normalize to stop and a hold only to pause.
	signal := stop.NewSignal(run.ID, stop.SignalSourcePolicyDecision, stop.SignalSeverityHigh,
		in.Action.StopAction(), in.Reason, []string{in.PolicyDecision.ID})
	modelStopSignal := toModelStopSignal(signal)
	if err := in.Store.AppendStopSignal(ctx, modelStopSignal); err != nil {
		return "", fmt.Errorf("append stop signal: %w", err)
	}
	decision := stop.NewArbiter().Decide(run.ID, []stop.StopSignal{signal})
	decision.Signals = stop.EvidenceSummary([]stop.StopSignal{signal})
	if err := in.Recorder.StopDecision(ctx, decision); err != nil {
		return "", fmt.Errorf("record stop decision: %w", err)
	}

	// 4. Status and ledger, derived from the decision.
	run.Status = model.RunPaused
	if blocked {
		run.Status = model.RunStopped
	}
	run.EndedAt = time.Now().UTC()
	run.Summary = fmt.Sprintf("%s: %s", decision.Action, decision.Reason)
	if err := in.Store.SaveRun(ctx, run); err != nil {
		return "", fmt.Errorf("save run: %w", err)
	}

	// The authorize item reflects what actually happened: the call was denied
	// or held, not "done". The later items never ran.
	for i := range ledger.Checklist {
		switch ledger.Checklist[i].ID {
		case "policy_check":
			ledger.Checklist[i].Status = model.ProgressBlocked
			ledger.Checklist[i].Evidence = fmt.Sprintf("%s: %s", in.Action, in.Reason)
		case "validate_contract":
			ledger.Checklist[i].Status = model.ProgressTodo
			ledger.Checklist[i].Evidence = "not reached: the tool call was not permitted"
		case "generate_report":
			ledger.Checklist[i].Status = model.ProgressTodo
			ledger.Checklist[i].Evidence = "not reached: the tool call was not permitted"
		}
	}
	ledger.CurrentPhase = "policy_barrier"
	ledger.Decisions = []string{
		fmt.Sprintf("Tool call was not executed: policy %s at authority %s.", in.Action, in.PolicyDecision.Authority),
		fmt.Sprintf("Policy decision %s -> execution mode %s (%s)", in.PolicyDecision.ID, in.Mode, in.Reason),
	}
	if blocked {
		ledger.Blockers = append(ledger.Blockers, "policy denied the tool call: "+in.Reason)
		ledger.NextActions = []string{"Request a policy exception or raise the run authority, then replay."}
	} else {
		ledger.Blockers = append(ledger.Blockers, "waiting for human approval: "+in.Reason)
		ledger.NextActions = []string{"A human must approve or reject; the run resumes from there."}
	}
	ledger.UpdatedAt = run.EndedAt
	if err := in.Store.SaveProgressLedger(ctx, ledger); err != nil {
		return "", fmt.Errorf("update progress ledger: %w", err)
	}

	// 5. Finish the trail: report, and for a refusal the bad case + lessons —
	// a denied call is exactly the kind of run worth learning from.
	if err := closeBarrier(ctx, barrierCloseInput{
		Store:         in.Store,
		Recorder:      in.Recorder,
		Run:           run,
		Packet:        in.Packet,
		Now:           in.Now,
		ContextPack:   in.ContextPack,
		StopSignal:    modelStopSignal,
		StopDecision:  decision,
		Ledger:        ledger,
		Errors:        envelopes,
		PolicyDec:     in.PolicyDecision,
		WriteBadCase:  blocked,
		BarrierReason: in.Reason,
	}); err != nil {
		return "", err
	}

	return run.ID, nil
}

type barrierCloseInput struct {
	Store         *storage.FileStore
	Recorder      *trace.Recorder
	Run           model.Run
	Packet        model.TaskPacket
	Now           time.Time
	ContextPack   model.ContextPack
	StopSignal    model.StopSignalRecord
	StopDecision  model.StopDecision
	Ledger        model.ProgressLedger
	Errors        []model.ErrorEnvelope
	PolicyDec     model.PolicyDecision
	WriteBadCase  bool
	BarrierReason string
}

// closeBarrier writes the report and, for a refusal, the bad case and lessons.
func closeBarrier(ctx context.Context, in barrierCloseInput) error {
	snapshot := report.RunSnapshot{
		Run:             in.Run,
		TaskPacket:      in.Packet,
		PolicyDecisions: []model.PolicyDecision{in.PolicyDec},
		StopSignals:     []model.StopSignalRecord{in.StopSignal},
		StopDecisions:   []model.StopDecision{in.StopDecision},
		ProgressLedger:  &in.Ledger,
		ContextPacks:    []model.ContextPack{in.ContextPack},
		Errors:          in.Errors,
		Events: []model.Event{
			{Type: model.EventRunStarted, Message: "run started", Timestamp: in.Now},
			{Type: model.EventPolicyDecided, Message: "tool call not executed: " + in.BarrierReason, Timestamp: in.Run.EndedAt},
			{Type: model.EventStopDecided, Message: "stop decision: " + string(in.StopDecision.Action), Timestamp: in.Run.EndedAt},
		},
	}

	if in.WriteBadCase {
		derived := lessons.Derive(snapshot)
		for _, lesson := range derived {
			if err := in.Store.AppendLesson(ctx, lesson); err != nil {
				return fmt.Errorf("append lesson: %w", err)
			}
		}
		snapshot.Lessons = derived
	}

	if err := in.Store.WriteReport(ctx, in.Run.ID, report.GenerateMarkdown(snapshot)); err != nil {
		return fmt.Errorf("write report: %w", err)
	}
	if err := in.Recorder.Event(ctx, model.EventReportGenerated, "report generated", nil); err != nil {
		return fmt.Errorf("record report_generated: %w", err)
	}
	if in.WriteBadCase {
		badcase, err := report.GenerateBadCaseYAML(snapshot)
		if err != nil {
			return fmt.Errorf("generate bad case: %w", err)
		}
		if err := in.Store.WriteBadCase(ctx, in.Run.ID, badcase); err != nil {
			return fmt.Errorf("write bad case: %w", err)
		}
	}
	if err := in.Recorder.Event(ctx, model.EventRunFinished, "run finished", map[string]any{
		"status": string(in.Run.Status),
	}); err != nil {
		return fmt.Errorf("record run_finished: %w", err)
	}
	return nil
}
