package coordinator

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	forgev1 "github.com/castwell/forge/api/proto/gen"
	"github.com/castwell/forge/internal/storage"
)

// scriptedWorker answers from a per-task-name script, so a loop's iterations can
// be driven deterministically.
type scriptedWorker struct {
	forgev1.WorkerServiceClient
	mu    sync.Mutex
	calls map[string]int
	// replies yields the output for the Nth call of a task (1-based). Past the
	// end of the list the last entry repeats.
	replies map[string][]string
}

func newScriptedWorker(replies map[string][]string) *scriptedWorker {
	return &scriptedWorker{calls: map[string]int{}, replies: replies}
}

func (w *scriptedWorker) ExecuteTask(_ context.Context, req *forgev1.TaskRequest, _ ...grpc.CallOption) (*forgev1.TaskResponse, error) {
	w.mu.Lock()
	name := req.GetTaskName()
	w.calls[name]++
	n := w.calls[name]
	list := w.replies[name]
	w.mu.Unlock()

	out := "{}"
	if len(list) > 0 {
		idx := n - 1
		if idx >= len(list) {
			idx = len(list) - 1
		}
		out = list[idx]
	}
	return &forgev1.TaskResponse{Success: true, Output: json.RawMessage(out)}, nil
}

func (w *scriptedWorker) countFor(name string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.calls[name]
}

func (w *scriptedWorker) total() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := 0
	for _, c := range w.calls {
		n += c
	}
	return n
}

func gotoTestSetup(t *testing.T, worker *scriptedWorker) *Coordinator {
	t.Helper()
	store, err := storage.NewBoltStorage(filepath.Join(t.TempDir(), "goto.db"))
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	coord := NewCoordinator(store)
	coord.mu.Lock()
	coord.workers["w-shell"] = &WorkerEntry{
		ID:       "w-shell",
		Handlers: []string{"shell"},
		Capacity: 10,
		Client:   worker,
	}
	coord.mu.Unlock()
	return coord
}

func waitForWorkflow(t *testing.T, coord *Coordinator, id string, want storage.WorkflowStatus) {
	t.Helper()
	require.Eventually(t, func() bool {
		wf, err := coord.store.GetWorkflow(context.Background(), id)
		return err == nil && wf.Status == want
	}, 25*time.Second, 25*time.Millisecond, "workflow should reach %s", want)
}

// taskByNameForTest reads one task row.
func taskByNameForTest(t *testing.T, coord *Coordinator, workflowID, name string) *storage.Task {
	t.Helper()
	got, err := coord.taskByName(context.Background(), workflowID, name)
	require.NoError(t, err)
	return got
}

// A goto re-runs the target, and the segment between target and the jumping
// task is rewound with it. The second pass sees fresh output, and the workflow
// then completes normally.
func TestGotoRerunsTheSegmentAndCompletes(t *testing.T) {
	// `check` reports "retry" the first time and "ok" afterwards; `judge` jumps
	// back to `check` when it sees "retry".
	worker := newScriptedWorker(map[string][]string{
		"check": {`{"verdict":"retry"}`, `{"verdict":"ok"}`},
		"judge": {`{"decision":"goto"}`, `{"decision":"done"}`},
	})
	coord := gotoTestSetup(t, worker)

	dagYAML := `
name: goto_basic
tasks:
  check:
    handler: shell
    output: check
    loop:
      max_iterations: 3
  judge:
    handler: shell
    depends_on: [check]
    on_result:
      success: "goto:check"
    condition: "results.check.verdict == 'retry'"
`
	// judge only runs while check says retry; once check says ok, judge is
	// skipped by its condition and the workflow completes.

	resp, err := coord.SubmitWorkflow(context.Background(), &forgev1.SubmitWorkflowRequest{DagYaml: dagYAML})
	require.NoError(t, err)

	waitForWorkflow(t, coord, resp.GetWorkflowId(), storage.WorkflowStatusCompleted)

	assert.Equal(t, 2, worker.countFor("check"), "check runs twice: the first pass and the goto re-run")
	assert.Equal(t, 1, worker.countFor("judge"), "judge runs once, then its condition excludes it")

	target := taskByNameForTest(t, coord, resp.GetWorkflowId(), "check")
	assert.Equal(t, 1, target.LoopIteration, "one revisit was consumed from the loop budget")
}

// Exhausting max_iterations fails the workflow. A loop that cannot make progress
// is something a human must see; stopping quietly would masquerade as success.
func TestGotoExceedingMaxIterationsFailsTheWorkflow(t *testing.T) {
	worker := newScriptedWorker(map[string][]string{
		// Always says retry, so the loop never terminates on its own.
		"check": {`{"verdict":"retry"}`},
		"judge": {`{"decision":"goto"}`},
	})
	coord := gotoTestSetup(t, worker)

	dagYAML := `
name: goto_capped
tasks:
  check:
    handler: shell
    output: check
    loop:
      max_iterations: 2
  judge:
    handler: shell
    depends_on: [check]
    on_result:
      success: "goto:check"
`
	resp, err := coord.SubmitWorkflow(context.Background(), &forgev1.SubmitWorkflowRequest{DagYaml: dagYAML})
	require.NoError(t, err)

	waitForWorkflow(t, coord, resp.GetWorkflowId(), storage.WorkflowStatusFailed)

	target := taskByNameForTest(t, coord, resp.GetWorkflowId(), "check")
	assert.Equal(t, 2, target.LoopIteration, "the cap is where revisits stop")
	assert.LessOrEqual(t, worker.countFor("check"), 3, "the loop must not run away")
}

// break_on stops the loop without failing anything: the task's completion simply
// stands as an ordinary one.
func TestGotoBreakOnStopsTheLoopCleanly(t *testing.T) {
	worker := newScriptedWorker(map[string][]string{
		"check": {`{"verdict":"retry"}`},
		"judge": {`{"decision":"goto"}`},
	})
	coord := gotoTestSetup(t, worker)

	// break_on is evaluated with the target's CURRENT revisit count, which is 0
	// on the first jump. So "iteration >= 1" permits exactly one revisit and
	// then stops the second — the count is the number of revisits already made,
	// not the one being considered.
	dagYAML := `
name: goto_break
tasks:
  check:
    handler: shell
    output: check
    loop:
      max_iterations: 9
      break_on: "iteration >= 1"
  judge:
    handler: shell
    depends_on: [check]
    on_result:
      success: "goto:check"
`
	resp, err := coord.SubmitWorkflow(context.Background(), &forgev1.SubmitWorkflowRequest{DagYaml: dagYAML})
	require.NoError(t, err)

	waitForWorkflow(t, coord, resp.GetWorkflowId(), storage.WorkflowStatusCompleted)

	assert.Equal(t, 2, worker.countFor("check"),
		"the first jump is allowed (iteration 0), the second is broken (iteration 1)")
	assert.Equal(t, 1, taskByNameForTest(t, coord, resp.GetWorkflowId(), "check").LoopIteration,
		"exactly one revisit was taken")
}

// A goto to a task that cannot reach the current one has no defined re-run set,
// so it is refused — and the refusal fails the run rather than being dropped.
//
// The shape is deliberate: `target` DEPENDS ON `jumper`, so it is a descendant
// rather than an ancestor, and at the moment `jumper` completes `target` is still
// PENDING. The refusal is then the only thing in flight, which keeps this test
// about the refusal rather than about who finished first. An earlier version put
// the bad route on a task that raced an unrelated root, and the run legitimately
// ended either way depending on goroutine ordering.
func TestGotoToNonAncestorIsRefused(t *testing.T) {
	worker := newScriptedWorker(map[string][]string{})
	coord := gotoTestSetup(t, worker)

	dagYAML := `
name: goto_forward
tasks:
  jumper:
    handler: shell
    on_result:
      success: "goto:target"
  target:
    handler: shell
    depends_on: [jumper]
`
	resp, err := coord.SubmitWorkflow(context.Background(), &forgev1.SubmitWorkflowRequest{DagYaml: dagYAML})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		wf, err := coord.store.GetWorkflow(context.Background(), resp.GetWorkflowId())
		return err == nil && wf.Status == storage.WorkflowStatusFailed
	}, 25*time.Second, 25*time.Millisecond,
		"a route that cannot be honoured must fail the run, not stall it")

	assert.Equal(t, 0, worker.countFor("target"),
		"the refused goto must not run the target")
	assert.Equal(t, 0, taskByNameForTest(t, coord, resp.GetWorkflowId(), "target").LoopIteration,
		"and must not consume loop budget")
}

// A goto to a task that does not exist is an error, not a silent no-op.
func TestGotoToUnknownTaskIsRefused(t *testing.T) {
	worker := newScriptedWorker(map[string][]string{})
	coord := gotoTestSetup(t, worker)

	dagYAML := `
name: goto_missing
tasks:
  a:
    handler: shell
    on_result:
      success: "goto:nowhere"
`
	resp, err := coord.SubmitWorkflow(context.Background(), &forgev1.SubmitWorkflowRequest{DagYaml: dagYAML})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		wf, err := coord.store.GetWorkflow(context.Background(), resp.GetWorkflowId())
		return err == nil && wf.Status == storage.WorkflowStatusFailed
	}, 25*time.Second, 25*time.Millisecond, "an unknown goto target must fail the run")

	assert.Equal(t, 1, worker.total(), "only the declared task ran")
}

// The rewind clears the target's output, so the next pass cannot read the value
// the previous pass produced. A stale output is worse than none: successors
// would treat it as this run's answer.
func TestGotoClearsTheRewoundOutput(t *testing.T) {
	worker := newScriptedWorker(map[string][]string{
		"check": {`{"verdict":"retry"}`, `{"verdict":"ok"}`},
		"judge": {`{"decision":"goto"}`},
	})
	coord := gotoTestSetup(t, worker)

	dagYAML := `
name: goto_clears
tasks:
  check:
    handler: shell
    output: check
    loop:
      max_iterations: 3
  judge:
    handler: shell
    depends_on: [check]
    on_result:
      success: "goto:check"
    condition: "results.check.verdict == 'retry'"
`
	resp, err := coord.SubmitWorkflow(context.Background(), &forgev1.SubmitWorkflowRequest{DagYaml: dagYAML})
	require.NoError(t, err)
	waitForWorkflow(t, coord, resp.GetWorkflowId(), storage.WorkflowStatusCompleted)

	// The final output is the second pass's value, not the first pass's.
	check := taskByNameForTest(t, coord, resp.GetWorkflowId(), "check")
	assert.Contains(t, string(check.Output), "ok",
		"the surviving output must be the re-run's, not the stale one")
}

// A goto records itself in the event stream, and a replay reconstructs the loop.
func TestGotoIsRecordedAndReplayable(t *testing.T) {
	worker := newScriptedWorker(map[string][]string{
		"check": {`{"verdict":"retry"}`, `{"verdict":"ok"}`},
		"judge": {`{"decision":"goto"}`},
	})
	coord := gotoTestSetup(t, worker)

	dagYAML := `
name: goto_events
tasks:
  check:
    handler: shell
    output: check
    loop:
      max_iterations: 3
  judge:
    handler: shell
    depends_on: [check]
    on_result:
      success: "goto:check"
    condition: "results.check.verdict == 'retry'"
`
	resp, err := coord.SubmitWorkflow(context.Background(), &forgev1.SubmitWorkflowRequest{DagYaml: dagYAML})
	require.NoError(t, err)
	waitForWorkflow(t, coord, resp.GetWorkflowId(), storage.WorkflowStatusCompleted)

	events, err := coord.store.GetWorkflowHistory(context.Background(), resp.GetWorkflowId())
	require.NoError(t, err)

	var gotGoto, gotRewound bool
	for _, e := range events {
		switch e.Type {
		case storage.EventTaskGoto:
			gotGoto = true
		case storage.EventTaskRewound:
			gotRewound = true
		}
	}
	assert.True(t, gotGoto, "the jump must appear in the log")
	assert.True(t, gotRewound, "so must the rewind it caused")
}

// Without a goto route nothing changes: this is the regression guard for the
// whole feature.
func TestNoGotoRouteLeavesBehaviourAlone(t *testing.T) {
	worker := newScriptedWorker(map[string][]string{})
	coord := gotoTestSetup(t, worker)

	dagYAML := `
name: goto_none
tasks:
  a:
    handler: shell
  b:
    handler: shell
    depends_on: [a]
`
	resp, err := coord.SubmitWorkflow(context.Background(), &forgev1.SubmitWorkflowRequest{DagYaml: dagYAML})
	require.NoError(t, err)
	waitForWorkflow(t, coord, resp.GetWorkflowId(), storage.WorkflowStatusCompleted)

	assert.Equal(t, 1, worker.countFor("a"))
	assert.Equal(t, 1, worker.countFor("b"))
	assert.Equal(t, 0, taskByNameForTest(t, coord, resp.GetWorkflowId(), "a").LoopIteration)
}

// --- helpers ---

func TestReachableSetFindsTheWholeDownstream(t *testing.T) {
	dag := &DAG{Tasks: map[string]*TaskDef{
		"a": {DependsOn: nil},
		"b": {DependsOn: []string{"a"}},
		"c": {DependsOn: []string{"b"}},
		"d": {DependsOn: []string{"a"}},
		"x": {DependsOn: nil},
	}}

	from := reachableSet(dag, "a")
	assert.True(t, from["b"])
	assert.True(t, from["c"])
	assert.True(t, from["d"])
	assert.False(t, from["x"], "an unrelated branch is not reachable")
	assert.False(t, from["a"], "the root is not its own descendant")

	assert.True(t, reachableSet(dag, "a")["c"], "transitivity holds through b")
	assert.Empty(t, reachableSet(dag, "c"), "a leaf reaches nothing")
}
