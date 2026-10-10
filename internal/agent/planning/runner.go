package planning

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// The plan-and-execute half of the agent design.
//
// The architecture is two halves: plan a DAG, submit it, then reflect on what
// came back. Only the ReAct half was wired; this is the other one, and the seam
// it needs already existed (session.ForgeClient) with nothing calling it.
//
// The pieces stay separate on purpose. Planner turns text into a DAG, Submitter
// runs a DAG, and Runner joins them without either learning about the other. The
// value is in the joining: a caller with its own submission path can still use
// the planner, and a caller that only wants to await a workflow can use the
// submitter.

// Submitter submits a DAG for execution and reports on it.
//
// It is an interface rather than *session.ForgeClient so tests can drive the
// runner without a gRPC connection, and so the planning package does not pull in
// the session package's transport. session.ForgeClient satisfies it as-is.
type Submitter interface {
	// Submit sends the DAG and returns the workflow's ID.
	Submit(ctx context.Context, dagYAML string) (string, error)
	// Snapshot fetches the workflow's current state.
	Snapshot(ctx context.Context, workflowID string) (*WorkflowSnapshot, error)
}

// WorkflowSnapshot is what a runner needs to know about a running workflow: the
// part of the coordinator's response that decides what happens next.
type WorkflowSnapshot struct {
	ID       string
	Name     string
	Status   string
	ErrorMsg string
	Tasks    []TaskSnapshot
}

// TaskSnapshot is one task's outcome.
type TaskSnapshot struct {
	Name     string
	Handler  string
	Status   string
	Output   json.RawMessage
	ErrorMsg string
}

// IsTerminal reports whether the workflow has finished, one way or another.
//
// PAUSED is NOT terminal: a workflow waiting on a human has not finished, and
// treating it as finished would report a half-run as a result. The caller decides
// how long to wait, which is why it is its own status rather than a timeout.
func (s *WorkflowSnapshot) IsTerminal() bool {
	switch s.Status {
	case "COMPLETED", "FAILED", "CANCELLED":
		return true
	default:
		return false
	}
}

// Succeeded reports whether the workflow finished well.
func (s *WorkflowSnapshot) Succeeded() bool { return s.Status == "COMPLETED" }

// RunOptions bounds one plan-and-execute run.
type RunOptions struct {
	// PollInterval is how often the workflow is checked. Zero means one second.
	PollInterval time.Duration
	// Timeout caps the whole wait. Zero means no cap, which is only sensible
	// when the workflow's own tasks carry deadlines — the caller's choice.
	Timeout time.Duration
	// OnTick, when set, is called after each poll. It exists so a runner can be
	// watched without the runner knowing how it is being watched.
	OnTick func(*WorkflowSnapshot)
}

// RunResult is the outcome of one plan-and-execute run.
type RunResult struct {
	// WorkflowID is the submitted workflow, kept even on failure so a caller can
	// go and look at it.
	WorkflowID string
	// YAML is the plan that was submitted — the artefact, not just its effect.
	YAML string
	// Strategy is how the plan was produced: "template", "llm" or "fallback".
	Strategy string
	// Snapshot is the final state, or nil when the wait timed out.
	Snapshot *WorkflowSnapshot
	// TimedOut reports that the deadline passed before the workflow finished.
	// It is separate from an error because the run is still going: a caller can
	// wait longer or go and look, and neither is possible if this is collapsed
	// into a failure.
	TimedOut bool
}

// Runner plans a requirement and executes the plan.
type Runner struct {
	generator *DAGGenerator
	submitter Submitter
}

// NewRunner joins a generator to a submitter. Both are required: a runner with
// no generator has nothing to run, and one with no submitter has nowhere to send
// it.
func NewRunner(generator *DAGGenerator, submitter Submitter) *Runner {
	return &Runner{generator: generator, submitter: submitter}
}

// PlanAndRun generates a DAG for the requirement and waits for it to finish.
//
// The plan is submitted as a child workflow rather than executed inline: that is
// the design, and it is also what makes the run auditable — the child's own event
// log, artefact set and task rows are the record, rather than a side effect
// inside this process.
func (r *Runner) PlanAndRun(ctx context.Context, req *Requirement, opts RunOptions) (*RunResult, error) {
	generated, err := r.generator.Generate(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("plan and run: %w", err)
	}

	workflowID, err := r.submitter.Submit(ctx, generated.YAML)
	if err != nil {
		// The plan is returned alongside the error: it was produced and paid for,
		// and a caller debugging a rejected plan needs to see it.
		return &RunResult{
			YAML:     generated.YAML,
			Strategy: generated.Strategy,
		}, fmt.Errorf("plan and run: submit: %w", err)
	}

	result := &RunResult{
		WorkflowID: workflowID,
		YAML:       generated.YAML,
		Strategy:   generated.Strategy,
	}

	snapshot, timedOut, err := r.await(ctx, workflowID, opts)
	result.Snapshot = snapshot
	result.TimedOut = timedOut
	if err != nil {
		return result, fmt.Errorf("plan and run: await %s: %w", workflowID, err)
	}
	return result, nil
}

// await polls until the workflow is terminal, the deadline passes, or the context
// is cancelled.
//
// Polling rather than a server-side watch because the coordinator has no
// subscribe RPC: adding one to serve this caller would put a new mechanism on the
// critical path for a feature that already works without it.
func (r *Runner) await(ctx context.Context, workflowID string, opts RunOptions) (*WorkflowSnapshot, bool, error) {
	interval := opts.PollInterval
	if interval <= 0 {
		interval = time.Second
	}

	var deadline time.Time
	if opts.Timeout > 0 {
		deadline = time.Now().Add(opts.Timeout)
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		snapshot, err := r.submitter.Snapshot(ctx, workflowID)
		if err != nil {
			return nil, false, err
		}
		if opts.OnTick != nil {
			opts.OnTick(snapshot)
		}
		if snapshot.IsTerminal() {
			return snapshot, false, nil
		}

		if !deadline.IsZero() && time.Now().After(deadline) {
			// The run is still going; that is not a failure and must not be
			// reported as one. The last snapshot travels so a caller can see
			// where it got to.
			return snapshot, true, nil
		}

		select {
		case <-ctx.Done():
			return nil, false, ctx.Err()
		case <-ticker.C:
		}
	}
}

// CheckAcceptance reports which of a requirement's checks the run's outputs
// appear to satisfy.
//
// It matches each check against the tasks' output text. That is a coarse test and
// is meant to be: it answers "is there evidence in the run for this check", which
// a person can then confirm. Claiming to decide whether an outcome was achieved
// would be a judgement this code cannot make — the checks are prose, and prose
// needs a reader.
//
// The result is therefore evidence, not a verdict. An unmet check means "no
// output mentioned this", which is exactly what someone reviewing the run needs
// to look at first.
func CheckAcceptance(a Acceptance, snapshot *WorkflowSnapshot) AcceptanceEvidence {
	evidence := AcceptanceEvidence{
		Criteria: a.Criteria,
		RunEnded: snapshot != nil && snapshot.IsTerminal(),
		Passed:   snapshot != nil && snapshot.Succeeded(),
	}

	// The checks are always reported, even with nothing to match them against.
	// Returning early on a nil snapshot would drop them from the result, and
	// "we have no evidence" is a different statement from "there was nothing to
	// check" — the first is what a caller needs to see.
	var corpus []byte
	if snapshot != nil {
		for _, task := range snapshot.Tasks {
			corpus = append(corpus, task.Output...)
			corpus = append(corpus, '\n')
		}
	}

	for _, check := range a.Checks {
		evidence.Checks = append(evidence.Checks, CheckEvidence{
			Check: check,
			// Matching the check's own words against the outputs: a run that
			// addressed it usually names it. Absence is the signal worth
			// surfacing, so a miss is reported as "no evidence", not "failed".
			Mentioned: containsFold(string(corpus), check),
		})
	}
	return evidence
}

// AcceptanceEvidence is what a run shows about a requirement's acceptance.
type AcceptanceEvidence struct {
	Criteria string
	RunEnded bool
	Passed   bool
	Checks   []CheckEvidence
}

// CheckEvidence is one check and whether the run's outputs mention it.
type CheckEvidence struct {
	Check     string
	Mentioned bool
}

// Unmentioned returns the checks with no supporting text, which is what a
// reviewer should look at. Empty does not mean passed — it means the run's
// outputs did not use these words.
func (e AcceptanceEvidence) Unmentioned() []string {
	var out []string
	for _, c := range e.Checks {
		if !c.Mentioned {
			out = append(out, c.Check)
		}
	}
	return out
}

// containsFold is a case-insensitive substring test. It lives here rather than
// using strings.Contains because the checks and the outputs are prose written at
// different times, and case is not a meaningful difference between them.
func containsFold(haystack, needle string) bool {
	if needle == "" {
		return false
	}
	h := []rune(lowerASCII(haystack))
	n := []rune(lowerASCII(needle))
	if len(n) > len(h) {
		return false
	}
	for i := 0; i+len(n) <= len(h); i++ {
		if string(h[i:i+len(n)]) == string(n) {
			return true
		}
	}
	return false
}

// lowerASCII lowercases A–Z only, leaving other bytes alone. Prose here is
// frequently Chinese, where byte-wise lowercasing of a multi-byte rune would
// corrupt it; ASCII case is the only case that exists in this data.
func lowerASCII(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}
