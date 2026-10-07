package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/castwell/forge/internal/storage"
)

// ErrJobNotFound reports that a submitted Job no longer exists. It is declared
// here, next to the interface that returns it, because the implementation
// (internal/kubesubmit) already imports this package — the sentinel cannot live
// there without an import cycle.
//
// The distinction it draws is the difference between "retry" and "give up": a
// transient read failure must be retried, while a missing Job will never appear
// however often it is polled.
var ErrJobNotFound = errors.New("job not found")

// Kueue dispatch, part two: results.
//
// Submission alone is a half-wire, and the dangerous half. A GPU task handed
// to Kueue does not run on a worker, so nothing ever calls the worker RPC that
// would normally report completion — the task would sit in RUNNING until a
// human noticed. The design note on #8b said exactly this ("半接线会让任务永久
// 吊死"), which is why routing and back-fill ship together.
//
// The shape of the fix follows the pieces that already exist:
//
//   - the deadline lives in Task.TimeoutAt, which the timeout manager already
//     scans, so Kueue tasks inherit ordinary timeout handling instead of
//     getting a second, Kueue-only timer;
//   - the job name is derived deterministically (KueueManager does this), so
//     nothing extra needs persisting to find the job again after a restart;
//   - "this task belongs to Kueue" is recorded in Task.WorkerID as a sentinel,
//     so recovery can find in-flight GPU tasks from storage alone.
//
// This reconciler therefore only does what is Kueue-specific: read job status
// and report it back.

// kueueWorkerPrefix marks a task whose execution lives in Kubernetes rather
// than on a worker. The full value is "kueue:<namespace>/<job>".
const kueueWorkerPrefix = "kueue:"

// defaultKueuePollInterval is how often job status is polled. Chosen to match
// the timeout manager's cadence: both watch the same tasks, and a slower
// reconciler would only delay a completion the timeout scanner is already
// counting down.
const defaultKueuePollInterval = 5 * time.Second

// DefaultKueuePollInterval exposes the default so the assembly layer can fall
// back to it without restating the number.
func DefaultKueuePollInterval() time.Duration { return defaultKueuePollInterval }

// KueueReconciler reports Kueue job outcomes back into the task state machine.
type KueueReconciler struct {
	store    storage.Storage
	kueue    *KueueManager
	onDone   func(ctx context.Context, taskID string, output []byte) error
	onFail   func(ctx context.Context, taskID string, msg string) error
	isLeader func() bool
	interval time.Duration
	// failStreak counts consecutive status-read failures per job, so a
	// Kubernetes API that is down produces an escalating log rather than one
	// line every interval forever.
	failStreak map[string]int
}

// NewKueueReconciler builds the reconciler. The callbacks are the
// coordinator's own OnTaskCompleted/OnTaskFailed, passed in rather than
// reached for, so the state machine stays the single owner of task outcomes.
func NewKueueReconciler(
	store storage.Storage,
	kueue *KueueManager,
	onDone func(ctx context.Context, taskID string, output []byte) error,
	onFail func(ctx context.Context, taskID string, msg string) error,
	isLeader func() bool,
	interval time.Duration,
) *KueueReconciler {
	if interval <= 0 {
		interval = defaultKueuePollInterval
	}
	if isLeader == nil {
		isLeader = func() bool { return true }
	}
	return &KueueReconciler{
		store:      store,
		kueue:      kueue,
		onDone:     onDone,
		onFail:     onFail,
		isLeader:   isLeader,
		interval:   interval,
		failStreak: map[string]int{},
	}
}

// Run polls until ctx is cancelled. It blocks; callers start it on its own
// goroutine.
//
// Only the leader reconciles. Every replica runs this loop, and two copies
// reporting the same completion would double-fire OnTaskCompleted — the state
// machine tolerates a repeated completion, but "tolerates" is not a reason to
// rely on it.
func (r *KueueReconciler) Run(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !r.isLeader() {
				continue
			}
			r.reconcileOnce(ctx)
		}
	}
}

// reconcileOnce finds in-flight Kueue tasks and reports their outcome.
func (r *KueueReconciler) reconcileOnce(ctx context.Context) {
	tasks, err := r.inFlight(ctx)
	if err != nil {
		log.Printf("ERROR: kueue reconciler list in-flight tasks: %v", err)
		return
	}
	for _, task := range tasks {
		r.reconcileTask(ctx, task)
	}
}

// inFlight returns RUNNING tasks whose execution is in Kubernetes.
//
// The scan goes through running workflows because that is the only listing the
// storage interface offers (the timeout manager scans the same way); the
// sentinel in WorkerID is what distinguishes a Kueue task from a worker one.
func (r *KueueReconciler) inFlight(ctx context.Context) ([]*storage.Task, error) {
	workflows, err := r.store.ListWorkflows(ctx, storage.WorkflowStatusRunning, 1000, 0)
	if err != nil {
		return nil, err
	}
	var out []*storage.Task
	for _, wf := range workflows {
		tasks, err := r.store.ListTasksByWorkflow(ctx, wf.ID)
		if err != nil {
			log.Printf("ERROR: kueue reconciler list tasks for workflow %s: %v", wf.ID, err)
			continue
		}
		for _, task := range tasks {
			if task.Status != storage.TaskStatusRunning {
				continue
			}
			if _, _, ok := parseKueueWorker(task.WorkerID); ok {
				out = append(out, task)
			}
		}
	}
	return out, nil
}

// reconcileTask reports one task's job outcome, if it has reached one.
func (r *KueueReconciler) reconcileTask(ctx context.Context, task *storage.Task) {
	namespace, jobName, ok := parseKueueWorker(task.WorkerID)
	if !ok {
		return
	}

	status, err := r.kueue.submitter.GetJobStatus(ctx, namespace, jobName)
	if err != nil {
		// An orphan is not a read failure: the Job is gone (deleted by hand, or
		// swept by a TTL, or never created because the coordinator died between
		// submitting and recording). Polling it forever would leave the task in
		// RUNNING with nothing that can ever complete it — the "task hangs"
		// failure the write-back design exists to prevent. Fail it instead, and
		// say why, so a human can decide whether to re-run.
		if errors.Is(err, ErrJobNotFound) {
			reason := fmt.Sprintf("kueue job %s/%s no longer exists; the task cannot complete on its own", namespace, jobName)
			log.Printf("WARN: kueue: %s (task %s)", reason, task.ID)
			delete(r.failStreak, task.ID)
			if ferr := r.onFail(ctx, task.ID, reason); ferr != nil {
				log.Printf("ERROR: kueue reconciler fail orphaned task %s: %v", task.ID, ferr)
			}
			return
		}
		r.noteFailure(task, namespace, jobName, err)
		return
	}
	delete(r.failStreak, task.ID)

	switch status.Phase {
	case "Succeeded":
		log.Printf("INFO: kueue: job %s/%s succeeded, completing task %s", namespace, jobName, task.ID)
		// The output must be valid JSON: CompleteTask stores it as
		// json.RawMessage and every downstream reader (renderer, successors,
		// observer) unmarshals it. A job's message is prose, so it is wrapped
		// rather than passed through raw — the alternative is a task that
		// completes with output nobody can parse.
		output, err := json.Marshal(map[string]any{
			"kueue_job": jobName,
			"namespace": namespace,
			"message":   status.Message,
		})
		if err != nil {
			log.Printf("ERROR: kueue reconciler encode output for task %s: %v", task.ID, err)
			return
		}
		if err := r.onDone(ctx, task.ID, output); err != nil {
			log.Printf("ERROR: kueue reconciler complete task %s: %v", task.ID, err)
		}

	case "Failed":
		msg := status.Message
		if msg == "" {
			msg = fmt.Sprintf("kueue job %s/%s failed", namespace, jobName)
		}
		log.Printf("INFO: kueue: job %s/%s failed, failing task %s", namespace, jobName, task.ID)
		if err := r.onFail(ctx, task.ID, msg); err != nil {
			log.Printf("ERROR: kueue reconciler fail task %s: %v", task.ID, err)
		}

	default:
		// Pending / Admitted / Running: still in flight. The deadline is the
		// timeout manager's business — it is already scanning this task's
		// TimeoutAt, and a second timer here would be a second opinion about
		// the same question.
	}
}

// noteFailure logs a status-read failure with escalating severity. A cluster
// that is briefly unreachable is normal; one that stays unreachable while jobs
// are in flight is not, and the difference should be visible in the log rather
// than buried under one identical line per interval.
func (r *KueueReconciler) noteFailure(task *storage.Task, namespace, jobName string, err error) {
	r.failStreak[task.ID]++
	streak := r.failStreak[task.ID]
	if streak == 1 || streak%10 == 0 {
		log.Printf("WARN: kueue reconciler status %s/%s for task %s failed %d time(s): %v",
			namespace, jobName, task.ID, streak, err)
	}
}

// CancelJob cancels a submitted job. Used when a task is failed or abandoned
// while its job may still be running: leaving the job behind would keep
// consuming the GPU quota the queue exists to ration.
//
// Failures are logged, never returned as fatal: the task outcome has already
// been decided, and a cluster that refuses the deletion must not turn that
// decision into an error.
func (r *KueueReconciler) CancelJob(ctx context.Context, namespace, jobName string) {
	if err := r.kueue.submitter.CancelJob(ctx, namespace, jobName); err != nil {
		log.Printf("WARN: kueue: cancel job %s/%s: %v", namespace, jobName, err)
	}
}

// kueueWorkerID builds the sentinel recorded in Task.WorkerID.
func kueueWorkerID(namespace, jobName string) string {
	return kueueWorkerPrefix + namespace + "/" + jobName
}

// parseKueueWorker reads a sentinel back. ok is false for worker-owned tasks,
// which is what keeps ordinary dispatch untouched.
func parseKueueWorker(workerID string) (namespace, jobName string, ok bool) {
	if !strings.HasPrefix(workerID, kueueWorkerPrefix) {
		return "", "", false
	}
	rest := strings.TrimPrefix(workerID, kueueWorkerPrefix)
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}
