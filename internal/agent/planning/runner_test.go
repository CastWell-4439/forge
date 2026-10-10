package planning

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeSubmitter stands in for the transport. It records what was submitted and
// answers snapshots from a script, so the runner's waiting logic can be driven
// without a coordinator.
type fakeSubmitter struct {
	mu        sync.Mutex
	submitted []string
	submitErr error
	// snapshots are returned in order; the last one repeats.
	snapshots []*WorkflowSnapshot
	snapErr   error
	polls     int
}

func (f *fakeSubmitter) Submit(_ context.Context, dagYAML string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.submitErr != nil {
		return "", f.submitErr
	}
	f.submitted = append(f.submitted, dagYAML)
	return "wf-1", nil
}

func (f *fakeSubmitter) Snapshot(_ context.Context, _ string) (*WorkflowSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.snapErr != nil {
		return nil, f.snapErr
	}
	f.polls++
	if len(f.snapshots) == 0 {
		return &WorkflowSnapshot{Status: "RUNNING"}, nil
	}
	idx := f.polls - 1
	if idx >= len(f.snapshots) {
		idx = len(f.snapshots) - 1
	}
	return f.snapshots[idx], nil
}

// runnerFor builds a runner whose planner is driven by the given DAG reply.
func runnerFor(t *testing.T, planReply string, sub *fakeSubmitter) *Runner {
	t.Helper()
	llm := &scriptedLLM{replies: []string{planReply}}
	return NewRunner(NewDAGGenerator(llm, testCatalog(), stubProfile{}), sub)
}

const validPlan = `name: planned
tasks:
  work:
    handler: agent
    params:
      action: run
      task: 做点什么
`

// The whole point: a requirement becomes a plan, the plan is submitted, and the
// run is awaited to its end.
func TestRunnerPlansSubmitsAndAwaits(t *testing.T) {
	sub := &fakeSubmitter{snapshots: []*WorkflowSnapshot{
		{ID: "wf-1", Status: "RUNNING"},
		{ID: "wf-1", Status: "COMPLETED", Tasks: []TaskSnapshot{
			{Name: "work", Handler: "agent", Status: "COMPLETED", Output: json.RawMessage(`{"ok":true}`)},
		}},
	}}

	runner := runnerFor(t, validPlan, sub)
	result, err := runner.PlanAndRun(context.Background(), &Requirement{Description: "做点什么"}, RunOptions{
		PollInterval: time.Millisecond,
	})
	require.NoError(t, err)

	assert.Equal(t, "wf-1", result.WorkflowID)
	assert.Equal(t, "llm", result.Strategy)
	assert.Contains(t, result.YAML, "planned", "the plan travels back with the result")

	require.NotNil(t, result.Snapshot)
	assert.True(t, result.Snapshot.Succeeded())
	assert.False(t, result.TimedOut)

	sub.mu.Lock()
	defer sub.mu.Unlock()
	require.Len(t, sub.submitted, 1)
	assert.Contains(t, sub.submitted[0], "agent", "the submitted YAML is the generated plan")
}

// A failed workflow is a completed run, not an error: the run finished, and its
// outcome is a fact the caller reads from the snapshot.
func TestRunnerReportsAFailedRunAsAResult(t *testing.T) {
	sub := &fakeSubmitter{snapshots: []*WorkflowSnapshot{
		{ID: "wf-1", Status: "FAILED", ErrorMsg: "task blew up"},
	}}

	runner := runnerFor(t, validPlan, sub)
	result, err := runner.PlanAndRun(context.Background(), &Requirement{Description: "x"}, RunOptions{
		PollInterval: time.Millisecond,
	})
	require.NoError(t, err)

	require.NotNil(t, result.Snapshot)
	assert.False(t, result.Snapshot.Succeeded())
	assert.Contains(t, result.Snapshot.ErrorMsg, "blew up")
}

// A deadline that passes before the run ends is reported as a timeout, not as a
// failure: the workflow is still going, and a caller can wait longer or go and
// look. Collapsing that into an error would destroy both options.
func TestRunnerReportsTimeoutWithoutFailing(t *testing.T) {
	sub := &fakeSubmitter{snapshots: []*WorkflowSnapshot{
		{ID: "wf-1", Status: "RUNNING"},
	}}

	runner := runnerFor(t, validPlan, sub)
	result, err := runner.PlanAndRun(context.Background(), &Requirement{Description: "x"}, RunOptions{
		PollInterval: time.Millisecond,
		Timeout:      20 * time.Millisecond,
	})
	require.NoError(t, err)

	assert.True(t, result.TimedOut)
	assert.Equal(t, "wf-1", result.WorkflowID, "the id survives so the caller can go and look")
	require.NotNil(t, result.Snapshot)
	assert.Equal(t, "RUNNING", result.Snapshot.Status)
}

// A paused workflow is NOT terminal: it is waiting on a person, and treating it
// as finished would report a half-run as a result.
func TestPausedIsNotTerminal(t *testing.T) {
	snap := &WorkflowSnapshot{Status: "PAUSED"}
	assert.False(t, snap.IsTerminal())
	assert.False(t, snap.Succeeded())

	for _, status := range []string{"COMPLETED", "FAILED", "CANCELLED"} {
		assert.True(t, (&WorkflowSnapshot{Status: status}).IsTerminal(), status)
	}
	for _, status := range []string{"PENDING", "RUNNING", "PAUSED", "COMPENSATING", "UNKNOWN"} {
		assert.False(t, (&WorkflowSnapshot{Status: status}).IsTerminal(), status)
	}
}

// A submission failure returns the plan alongside the error: it was produced and
// paid for, and a caller debugging a rejected plan needs to see it.
func TestRunnerReturnsThePlanWhenSubmissionFails(t *testing.T) {
	sub := &fakeSubmitter{submitErr: errors.New("coordinator refused it")}

	runner := runnerFor(t, validPlan, sub)
	result, err := runner.PlanAndRun(context.Background(), &Requirement{Description: "x"}, RunOptions{})
	require.Error(t, err)

	require.NotNil(t, result, "the result must not be nil when there is a plan to hand back")
	assert.Contains(t, result.YAML, "planned")
	assert.Contains(t, err.Error(), "coordinator refused it")
}

// A planning failure is an error with nothing to submit.
func TestRunnerReportsPlanningFailure(t *testing.T) {
	llm := &scriptedLLM{err: errors.New("model unreachable")}
	sub := &fakeSubmitter{}
	runner := NewRunner(NewDAGGenerator(llm, testCatalog(), stubProfile{}), sub)

	_, err := runner.PlanAndRun(context.Background(), &Requirement{Description: "x"}, RunOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "model unreachable")

	sub.mu.Lock()
	defer sub.mu.Unlock()
	assert.Empty(t, sub.submitted, "nothing may be submitted when planning failed")
}

// A snapshot error stops the wait and is reported: carrying on would poll
// forever against something that is not answering.
func TestRunnerReportsSnapshotFailure(t *testing.T) {
	sub := &fakeSubmitter{snapErr: errors.New("coordinator is down")}

	runner := runnerFor(t, validPlan, sub)
	result, err := runner.PlanAndRun(context.Background(), &Requirement{Description: "x"}, RunOptions{
		PollInterval: time.Millisecond,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "coordinator is down")
	assert.Equal(t, "wf-1", result.WorkflowID, "the id is still reported")
}

// Cancelling the context stops the wait promptly and reports why.
func TestRunnerStopsOnContextCancel(t *testing.T) {
	sub := &fakeSubmitter{snapshots: []*WorkflowSnapshot{{ID: "wf-1", Status: "RUNNING"}}}
	ctx, cancel := context.WithCancel(context.Background())

	runner := runnerFor(t, validPlan, sub)
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	_, err := runner.PlanAndRun(ctx, &Requirement{Description: "x"}, RunOptions{
		PollInterval: time.Millisecond,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

// OnTick lets a caller watch a run without the runner knowing how.
func TestRunnerCallsOnTick(t *testing.T) {
	sub := &fakeSubmitter{snapshots: []*WorkflowSnapshot{
		{Status: "RUNNING"},
		{Status: "COMPLETED"},
	}}

	var seen []string
	runner := runnerFor(t, validPlan, sub)
	_, err := runner.PlanAndRun(context.Background(), &Requirement{Description: "x"}, RunOptions{
		PollInterval: time.Millisecond,
		OnTick:       func(s *WorkflowSnapshot) { seen = append(seen, s.Status) },
	})
	require.NoError(t, err)

	assert.Contains(t, seen, "RUNNING")
	assert.Contains(t, seen, "COMPLETED")
}

// Fallback plans run too: the guarantee is dispatchability, not elegance.
func TestRunnerRunsAFallbackPlan(t *testing.T) {
	sub := &fakeSubmitter{snapshots: []*WorkflowSnapshot{{ID: "wf-1", Status: "COMPLETED"}}}
	llm := &scriptedLLM{replies: []string{"not yaml"}}
	runner := NewRunner(NewDAGGenerator(llm, testCatalog(), stubProfile{}), sub)

	result, err := runner.PlanAndRun(context.Background(), &Requirement{Description: "做点什么"}, RunOptions{
		PollInterval: time.Millisecond,
	})
	require.NoError(t, err)

	assert.Equal(t, "fallback", result.Strategy)
	require.NotNil(t, result.Snapshot)
	assert.True(t, result.Snapshot.Succeeded())
}

// --- acceptance evidence ---

// Evidence reports what the run's outputs show about each check. It is evidence,
// not a verdict: a miss means "no output mentioned this", which is what a
// reviewer should look at first.
func TestCheckAcceptanceReportsEvidence(t *testing.T) {
	snapshot := &WorkflowSnapshot{
		Status: "COMPLETED",
		Tasks: []TaskSnapshot{
			{Name: "gather", Output: json.RawMessage(`已收集 3 个来源，并记录了出处`)},
			{Name: "produce", Output: json.RawMessage(`已生成报告，时长在 5~15 秒之间`)},
		},
	}

	evidence := CheckAcceptance(Acceptance{
		Criteria: "报告覆盖全部指标",
		Checks:   []string{"记录了出处", "时长在 5~15 秒之间", "覆盖了全部指标"},
	}, snapshot)

	assert.True(t, evidence.RunEnded)
	assert.True(t, evidence.Passed)
	assert.Equal(t, "报告覆盖全部指标", evidence.Criteria)

	require.Len(t, evidence.Checks, 3)
	assert.True(t, evidence.Checks[0].Mentioned)
	assert.True(t, evidence.Checks[1].Mentioned)
	assert.False(t, evidence.Checks[2].Mentioned)

	assert.Equal(t, []string{"覆盖了全部指标"}, evidence.Unmentioned())
}

// Case is not a meaningful difference between a check and the output that
// answers it.
func TestCheckAcceptanceIgnoresASCIICase(t *testing.T) {
	snapshot := &WorkflowSnapshot{
		Status: "COMPLETED",
		Tasks:  []TaskSnapshot{{Output: json.RawMessage(`Tests PASSED cleanly`)}},
	}

	evidence := CheckAcceptance(Acceptance{Checks: []string{"tests passed"}}, snapshot)
	require.Len(t, evidence.Checks, 1)
	assert.True(t, evidence.Checks[0].Mentioned)
}

// Multi-byte text must survive the case folding: lowercasing bytes would corrupt
// it, and this project's prose is frequently Chinese.
func TestCheckAcceptanceHandlesMultiByteText(t *testing.T) {
	snapshot := &WorkflowSnapshot{
		Status: "COMPLETED",
		Tasks:  []TaskSnapshot{{Output: json.RawMessage(`报告已生成，结论有数据支撑`)}},
	}

	evidence := CheckAcceptance(Acceptance{Checks: []string{"结论有数据支撑"}}, snapshot)
	require.Len(t, evidence.Checks, 1)
	assert.True(t, evidence.Checks[0].Mentioned, "Chinese text must match itself exactly")
}

// A nil snapshot is reported as not-ended rather than panicking: the run may
// simply not have been observed yet.
func TestCheckAcceptanceWithoutSnapshot(t *testing.T) {
	evidence := CheckAcceptance(Acceptance{Criteria: "x", Checks: []string{"y"}}, nil)

	assert.False(t, evidence.RunEnded)
	assert.False(t, evidence.Passed)
	require.Len(t, evidence.Checks, 1)
	assert.False(t, evidence.Checks[0].Mentioned)
}

// An empty check is never "mentioned": matching the empty string would make
// every check pass, which is the opposite of useful.
func TestCheckAcceptanceRefusesEmptyCheck(t *testing.T) {
	snapshot := &WorkflowSnapshot{Status: "COMPLETED", Tasks: []TaskSnapshot{{Output: json.RawMessage("anything")}}}

	evidence := CheckAcceptance(Acceptance{Checks: []string{""}}, snapshot)
	require.Len(t, evidence.Checks, 1)
	assert.False(t, evidence.Checks[0].Mentioned)
}
