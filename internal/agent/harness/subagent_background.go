package harness

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/castwell/forge/internal/agent/core"
)

// Background delegation: the spread version of RunSubagent.
//
// RunSubagent blocks the parent fold until the child finishes. That is usually
// right — the parent folded the subtask out precisely because it needs the
// result. But some delegations need no result soon: the parent can keep folding
// while the child runs, and collect it later.
//
// This is that: dispatch-and-collect, inside one run. A background delegation
// returns a handle immediately; the run goes on folding; when the result is
// needed it is collected, which blocks until the child is done or collects
// whatever it has reached.
//
// The shape is a REGISTER, not a fire-and-forget spawn. Fire-and-forget means
// nobody owns the cleanup: a background child whose post-processing happens via
// Release has to be ended by someone, and a parent that finished without
// collecting would leave a live child with no session to answer to. The register
// tracks what's outstanding so the run's end can collect or cancel what it
// left behind — and a caller is told "N running" instead of silently fanning
// further.
//
// Everything about the child is the same as a synchronous delegation: same
// factory, same depth rule, same semaphore. Background is a scheduling choice,
// not a changed runtime.

// BackgroundTask is a handle to one delegated child that is running (or done)
// without having yet been collected.
type BackgroundTask struct {
	// ChildID identifies the child; collecting by id answers "the one I started".
	ChildID string
	// Task is the work the child is doing, recorded so a listing can say what
	// is outstanding without touching the child's session.
	Task string

	mu   sync.Mutex
	done chan struct{}
	// result and err are set exactly once, when the child finishes.
	result  *core.SubagentResult
	err     error
	release func() // factory release, called on close
	// cancel stops the child itself. The factory's release is NOT enough for
	// that: it cleans up what the factory created (a connection, a temp dir) but
	// the child's own run continues, so cancelling needs a context of its own.
	cancel    context.CancelFunc
	cancelled bool
}

// BackgroundRegistry tracks one delegation's outstanding children.
//
// It is per-RUN, not per-runner: the register belongs to whoever delegated,
// so one run's obligations are not shared with another's.
type BackgroundRegistry struct {
	mu     sync.Mutex
	tasks  map[string]*BackgroundTask
	active int
	// maxActive bounds outstanding children. It is a number rather than the
	// semaphore because the semaphore is per-RUNNER and a register is per-RUN:
	// a run that delegates to two children at once must not find one of them
	// already occupying the runner's slot while waiting.
	maxActive int
}

// NewBackgroundRegistry builds an empty register. maxActive bounds how many
// children may be outstanding at once (0 = unlimited).
func NewBackgroundRegistry(maxActive int) *BackgroundRegistry {
	return &BackgroundRegistry{
		tasks:     make(map[string]*BackgroundTask),
		maxActive: maxActive,
	}
}

// StartSubagent dispatchs a child into the background. It returns the task to
// wait on and any immediate error (depth, factory, semaphore).
//
// The child's Run happens on a goroutine; the register entry is added first so
// that a run that fails to even build its child is not recorded as running.
//
// The boundary between "task started" and "task failed to start" is precise
// because a caller that sees "task started" must not collect an entry that was
// never going to produce a result.
func (r *SubagentRunnerImpl) StartSubagent(ctx context.Context, req core.SubagentRequest, registry **BackgroundRegistry) (*BackgroundTask, error) {
	if r == nil {
		return nil, fmt.Errorf("delegation is not available")
	}
	if registry == nil || *registry == nil {
		return nil, fmt.Errorf("delegation in background needs a registry (one per run)")
	}
	taskRegistry := *registry

	// Depth is checked against the CALLER, exactly as the synchronous path does.
	if err := r.checkDepth(req.Depth); err != nil {
		return nil, err
	}

	// Background continuation is not supported: continuing needs the child's
	// session, which by definition can't have been produced yet, and "continue
	// the background task" would mean the child had already been told to stop.
	if req.ChildID != "" {
		return nil, fmt.Errorf("continuation in background is not supported (use RunSubagent)")
	}

	childID := func() string {
		r.mu.Lock()
		r.seq++
		seq := r.seq
		r.mu.Unlock()
		return r.cfg.NewChildID(parentSessionFromContext(ctx), seq)
	}()

	// The register entry is created before the child runs: a runner that fails
	// to build the child must not produce a live entry.
	task := &BackgroundTask{
		ChildID: childID,
		Task:    req.Task,
		done:    make(chan struct{}),
	}

	// The count is maintained whether or not a limit is configured.
	//
	// "How many are running" and "is there a cap" are different questions: a
	// deployment with no cap still needs the first one to report what is
	// outstanding, and tying the counter to the cap made ActiveCount() always
	// read zero in the unlimited case — a register that cannot say what it holds.
	taskRegistry.mu.Lock()
	if taskRegistry.maxActive > 0 && taskRegistry.active >= taskRegistry.maxActive {
		taskRegistry.mu.Unlock()
		return nil, fmt.Errorf("background delegation limit (%d) reached; collect or stop one first", taskRegistry.maxActive)
	}
	taskRegistry.active++
	taskRegistry.tasks[childID] = task
	taskRegistry.mu.Unlock()

	// Concurrency: same semaphore as the synchronous path, held for the whole
	// child run. The semaphore is acquired in the goroutine so StartSubagent
	// itself never blocks on it.
	//
	// The child runs under its OWN context, derived from the caller's, so Cancel
	// can stop this child without touching its siblings or the parent.
	childCtx, cancelChild := context.WithCancel(ctx)
	task.mu.Lock()
	task.cancel = cancelChild
	task.mu.Unlock()

	go func() {
		defer cancelChild()
		defer close(task.done)
		defer func() {
			taskRegistry.mu.Lock()
			taskRegistry.active--
			taskRegistry.mu.Unlock()
		}()

		if r.sem != nil {
			select {
			case r.sem <- struct{}{}:
				defer func() { <-r.sem }()
			case <-childCtx.Done():
				task.mu.Lock()
				task.err = childCtx.Err()
				task.mu.Unlock()
				return
			}
		}

		loop, release, err := r.cfg.Factory(childCtx, childID, r.cfg.Config.Tools)
		task.mu.Lock()
		task.release = release
		task.mu.Unlock()
		if err != nil {
			task.mu.Lock()
			task.err = fmt.Errorf("start child %s: %w", childID, err)
			task.mu.Unlock()
			return
		}
		defer release()

		runCtx := WithSubagentDepth(childCtx, req.Depth+1)
		result, err := loop.Run(runCtx, childID, req.Task)
		task.mu.Lock()
		if err != nil {
			task.err = fmt.Errorf("child %s: %w", childID, err)
		} else {
			task.result = toSubagentResult(r.cfg.Config, childID, result)
		}
		task.mu.Unlock()
	}()

	return task, nil
}

// toSubagentResult shapes a child's run into the reported result, honouring the
// configured report mode.
func toSubagentResult(cfg core.SubagentConfig, childID string, result *RunResult) *core.SubagentResult {
	out := &core.SubagentResult{
		ChildID: childID,
		Answer:  result.Answer,
		Reason:  result.Reason,
	}
	if result.Reason == "paused" {
		out.Paused = true
		out.PauseReason = result.PauseReason
	}
	switch cfg.Report {
	case core.SubagentReportAnswer:
	case core.SubagentReportSteps:
		out.Steps = childSteps(result.Steps, true)
	default:
		out.Steps = childSteps(result.Steps, false)
	}
	return out
}

// Done reports whether the child has finished.
func (b *BackgroundTask) Done() bool {
	select {
	case <-b.done:
		return true
	default:
		return false
	}
}

// Collect waits for the child and returns the result.
func (b *BackgroundTask) Collect(ctx context.Context) (*core.SubagentResult, error) {
	select {
	case <-b.done:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.err != nil {
		return nil, b.err
	}
	return b.result, nil
}

// Cancel stops the child and waits for it to finish.
//
// Stopping takes BOTH halves, and leaving out either one leaves a child running:
//
//   - cancelling the child's context stops the run itself (the model call, the
//     tool call in flight);
//   - calling the factory's release cleans up what the factory created.
//
// The release alone is not enough — it does not interrupt a run — which is what
// an earlier version of this got wrong: a child blocked in its model call stayed
// blocked, and Cancel waited for it forever.
func (b *BackgroundTask) Cancel(ctx context.Context) error {
	b.mu.Lock()
	if b.cancelled {
		b.mu.Unlock()
		return nil // idempotent: cancelling twice is not an error
	}
	b.cancelled = true
	cancel := b.cancel
	release := b.release
	b.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if release != nil {
		release()
	}

	select {
	case <-b.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}

// How the register answers its management questions.

// Tasks returns the outstanding children, sorted by id so consecutive readings
// of the same register give the same picture.
func (r *BackgroundRegistry) Tasks() []*BackgroundTask {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*BackgroundTask, 0, len(r.tasks))
	for _, t := range r.tasks {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ChildID < out[j].ChildID })
	return out
}

// Get returns one outstanding child by id, or nil if it was never started or
// has finished and been removed.
func (r *BackgroundRegistry) Get(childID string) *BackgroundTask {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.tasks[childID]
}

// Remove removes a finished child. A caller collects first, then removes; and a
// run collects-and-removes as it ends.
func (r *BackgroundRegistry) Remove(childID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.tasks, childID)
}

// ActiveCount reports how many are still running, for the limit check.
func (r *BackgroundRegistry) ActiveCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.active
}

// CollectAll drains everything outstanding, collecting each child to completion.
// It is what a run calls as it ends so no child is left standing.
func (r *BackgroundRegistry) CollectAll(ctx context.Context) map[string]*core.SubagentResult {
	tasks := r.Tasks()
	out := make(map[string]*core.SubagentResult, len(tasks))
	for _, t := range tasks {
		if result, err := t.Collect(ctx); err == nil && result != nil {
			out[t.ChildID] = result
		}
		r.Remove(t.ChildID)
	}
	return out
}

// CancelAll cancels everything outstanding, then removes them. A run ending
// early uses this to leave nothing behind.
func (r *BackgroundRegistry) CancelAll(ctx context.Context) {
	for _, t := range r.Tasks() {
		_ = t.Cancel(ctx)
		r.Remove(t.ChildID)
	}
}
