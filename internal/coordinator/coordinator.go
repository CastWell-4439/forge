package coordinator

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"github.com/castwell/forge/internal/discovery"
	forgexruntime "github.com/castwell/forge/internal/forgex/runtime"
	"github.com/castwell/forge/internal/observability"
	"github.com/castwell/forge/internal/saga"
	"github.com/castwell/forge/internal/storage"

	forgev1 "github.com/castwell/forge/api/proto/gen"
)

// WorkerEntry tracks a connected worker's metadata and gRPC connection.
type WorkerEntry struct {
	ID       string
	Addr     string
	Handlers []string
	Capacity int
	Active   int
	Conn     *grpc.ClientConn
	Client   forgev1.WorkerServiceClient
}

// EventPublisher is the notification-side seam of the event pipeline: the
// event log in storage stays the source of truth, and a publisher (NATS
// JetStream or PostgreSQL LISTEN/NOTIFY) fans the same event out to
// interested processes. Deliberately narrow — both bus implementations
// already share this exact Publish signature.
type EventPublisher interface {
	Publish(ctx context.Context, channel, payload string) error
}

// EventChannel is the bus channel every persisted workflow/task event is
// published on.
const EventChannel = "workflow.events"

// ParamRenderer renders a task's params at dispatch time against the
// workflow's inputs and the outputs its dependencies have produced. This is
// the execution-side half of the workflow template contract ({{.name}} /
// {{.inputs.x}} chaining): params are frozen at submit, so without this seam
// every template would reach the worker as literal text.
//
// The rendering implementation is injected from the assembly layer (the
// registry template engine), keeping coordinator independent of how
// templates are spelled.
type ParamRenderer func(params map[string]any, inputs, outputs map[string]any) (map[string]any, error)

// Coordinator orchestrates workflow execution by parsing DAGs,
// creating task instances, scheduling them to workers, and driving
// the workflow state machine to completion.
type Coordinator struct {
	forgev1.UnimplementedCoordinatorServiceServer
	// WorkerService is served on the same listener so out-of-process workers
	// can register themselves (see register_rpc.go); only Register is
	// implemented, the rest stays Unimplemented by design.
	forgev1.UnimplementedWorkerServiceServer

	store           storage.Storage
	workers         map[string]*WorkerEntry
	mu              sync.RWMutex
	seqNum          int64
	seqMu           sync.Mutex
	disco           discovery.Discovery
	leader          *LeaderController
	workerMgr       *WorkerManager
	runtimeObserver forgexruntime.Observer
	eventBus        EventPublisher
	paramRenderer   ParamRenderer
	kueue           *KueueManager

	// dagCache stores parsed DAG definitions by workflow ID (for Saga compensation lookup).
	dagCache   map[string]*DAG
	dagCacheMu sync.RWMutex

	// advanceMu serialises the read-modify-write that moves a workflow forward.
	//
	// Advancing reads every task's status, decides which ones become READY or
	// SKIPPED, and writes the results. Two tasks can finish at the same instant
	// (they are separate goroutines, and a workflow's roots are dispatched
	// together), so without this two advances interleave: both read the same
	// pre-transition picture and both act on it. Observed live — a task was
	// dispatched twice, and a skip decided by one branch was undone by the
	// other's stale read.
	//
	// One lock for all workflows rather than a per-workflow map: the critical
	// section is a handful of database reads and writes, and correctness here
	// is worth more than the contention.
	advanceMu sync.Mutex

	// cel evaluates task conditions. Lazily built on first use because
	// compiling the environment is the expensive part, and the evaluator
	// caches programs so one instance serves every task.
	celOnce sync.Once
	cel     *CELEvaluator
	celErr  error
}

// cachedDAG returns the workflow's parsed DAG, or nil when it is not cached
// (a restart, or a workflow that came in through another coordinator).
//
// Returning nil rather than an error is deliberate: callers that only want a
// definition can skip their work, while callers that must have it check.
func (c *Coordinator) cachedDAG(workflowID string) *DAG {
	c.dagCacheMu.RLock()
	defer c.dagCacheMu.RUnlock()
	return c.dagCache[workflowID]
}

// NewCoordinator creates a new Coordinator with the given storage backend.
func NewCoordinator(store storage.Storage) *Coordinator {
	return &Coordinator{
		store:    store,
		workers:  make(map[string]*WorkerEntry),
		dagCache: make(map[string]*DAG),
	}
}

// RegisterWorker registers a worker with the coordinator so tasks can be dispatched to it.
func (c *Coordinator) RegisterWorker(ctx context.Context, id, addr string, handlers []string, capacity int) error {
	dialOpts := append([]grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())},
		observability.ClientDialOptions()...)
	conn, err := grpc.NewClient(addr, dialOpts...)
	if err != nil {
		return fmt.Errorf("connect to worker %s at %s: %w", id, addr, err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.workers[id] = &WorkerEntry{
		ID:       id,
		Addr:     addr,
		Handlers: handlers,
		Capacity: capacity,
		Conn:     conn,
		Client:   forgev1.NewWorkerServiceClient(conn),
	}
	return nil
}

// DeregisterWorker removes a worker from the registry.
func (c *Coordinator) DeregisterWorker(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if w, ok := c.workers[id]; ok {
		if w.Conn != nil {
			w.Conn.Close()
		}
		delete(c.workers, id)
	}
}

// SetRuntimeObserver configures an optional ForgeX observer for persisted Forge
// runtime events. Observer errors are logged by saveEvent and never alter Forge
// workflow execution semantics.
func (c *Coordinator) SetRuntimeObserver(observer forgexruntime.Observer) {
	c.runtimeObserver = observer
}

// SubmitWorkflow implements the CoordinatorService SubmitWorkflow RPC.
// It parses the DAG, persists the workflow and task instances, and kicks off execution.
func (c *Coordinator) SubmitWorkflow(ctx context.Context, req *forgev1.SubmitWorkflowRequest) (*forgev1.SubmitWorkflowResponse, error) {
	if !c.IsLeader() {
		return nil, status.Error(codes.Unavailable, "not the leader coordinator")
	}

	dagYAML := req.GetDagYaml()
	if dagYAML == "" {
		return nil, status.Error(codes.InvalidArgument, "dag_yaml is required")
	}

	dag, err := ParseDAG([]byte(dagYAML))
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "parse DAG: %v", err)
	}
	if err := dag.Validate(); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "validate DAG: %v", err)
	}

	return c.submitDAG(ctx, dag, req.GetInput())
}

// SubmitDAG submits an already-constructed DAG. It is the entry point for
// registry-compiled workflows bridged into the coordinator dialect (the
// bridge lives at the assembly layer); persistence and execution are shared
// with the YAML path so the two submission routes can never drift apart.
func (c *Coordinator) SubmitDAG(ctx context.Context, dag *DAG, input json.RawMessage) (*forgev1.SubmitWorkflowResponse, error) {
	if !c.IsLeader() {
		return nil, status.Error(codes.Unavailable, "not the leader coordinator")
	}
	if err := dag.Validate(); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "validate DAG: %v", err)
	}
	return c.submitDAG(ctx, dag, input)
}

// taskDeadline computes when a task should be considered timed out, or nil when
// nothing declares a deadline.
//
// The task's own timeout wins; the workflow's is the fallback. Both were parsed
// into the DAG long before this, but nothing ever wrote the resulting instant
// onto the task — so the timeout scanner had no row to find, and a task that
// hung stayed RUNNING forever. Filling the field is what makes the existing
// scanner (and the "fail on deadline" behaviour its comments describe) true.
//
// nil means "no deadline", which the scanner already understands: it skips
// tasks whose TimeoutAt is nil rather than treating a zero time as "expired".
func taskDeadline(taskTimeout, workflowTimeout time.Duration, createdAt time.Time) *time.Time {
	d := taskTimeout
	if d <= 0 {
		d = workflowTimeout
	}
	if d <= 0 {
		return nil
	}
	at := createdAt.Add(d)
	return &at
}

// submitDAG persists a validated DAG as a workflow instance with its tasks
// and starts execution. Callers have already checked leadership.
func (c *Coordinator) submitDAG(ctx context.Context, dag *DAG, input json.RawMessage) (*forgev1.SubmitWorkflowResponse, error) {
	workflowID := uuid.New().String()
	now := time.Now()

	// Persist workflow instance
	wf := &storage.Workflow{
		ID:        workflowID,
		Name:      dag.Name,
		Status:    storage.WorkflowStatusPending,
		Input:     input,
		CreatedAt: now,
	}
	if err := c.store.SaveWorkflow(ctx, wf); err != nil {
		return nil, status.Errorf(codes.Internal, "save workflow: %v", err)
	}

	// Record submission event
	c.saveEvent(ctx, workflowID, "", storage.EventWorkflowSubmitted, nil)

	// Cache DAG for Saga compensation lookup.
	c.dagCacheMu.Lock()
	c.dagCache[workflowID] = dag
	c.dagCacheMu.Unlock()

	// Create task instances from DAG
	taskIDs := make(map[string]string) // task_name -> task_id
	for name, taskDef := range dag.Tasks {
		taskID := uuid.New().String()
		taskIDs[name] = taskID

		inputJSON, _ := json.Marshal(taskDef.Params)
		maxAttempts := 1
		if taskDef.Retry.MaxAttempts > 0 {
			maxAttempts = taskDef.Retry.MaxAttempts
		}

		// Determine initial status: tasks with no dependencies start as READY
		taskStatus := storage.TaskStatusPending
		if len(taskDef.DependsOn) == 0 {
			taskStatus = storage.TaskStatusReady
		}

		task := &storage.Task{
			ID:          taskID,
			WorkflowID:  workflowID,
			TaskName:    name,
			Handler:     taskDef.Handler,
			Status:      taskStatus,
			Input:       inputJSON,
			MaxAttempts: maxAttempts,
			CreatedAt:   now,
			DependsOn:   taskDef.DependsOn,
			TimeoutAt:   taskDeadline(taskDef.Timeout, dag.Timeout, now),
		}
		if err := c.store.SaveTask(ctx, task); err != nil {
			return nil, status.Errorf(codes.Internal, "save task %s: %v", name, err)
		}
	}

	// Transition workflow to RUNNING
	if err := c.store.UpdateWorkflowStatus(ctx, workflowID, storage.WorkflowStatusRunning); err != nil {
		return nil, status.Errorf(codes.Internal, "update workflow status: %v", err)
	}
	c.saveEvent(ctx, workflowID, "", storage.EventWorkflowStarted, nil)

	// Schedule ready tasks (those with in-degree 0)
	go c.scheduleReadyTasks(context.Background(), workflowID)

	return &forgev1.SubmitWorkflowResponse{WorkflowId: workflowID}, nil
}

// GetWorkflow implements the CoordinatorService GetWorkflow RPC.
func (c *Coordinator) GetWorkflow(ctx context.Context, req *forgev1.GetWorkflowRequest) (*forgev1.GetWorkflowResponse, error) {
	wf, err := c.store.GetWorkflow(ctx, req.GetWorkflowId())
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "workflow not found: %v", err)
	}

	tasks, err := c.store.ListTasksByWorkflow(ctx, req.GetWorkflowId())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list tasks: %v", err)
	}

	protoTasks := make([]*forgev1.TaskInstance, 0, len(tasks))
	for _, t := range tasks {
		protoTasks = append(protoTasks, taskToProto(t))
	}

	return &forgev1.GetWorkflowResponse{
		Workflow: &forgev1.WorkflowInstance{
			Id:       wf.ID,
			Name:     wf.Name,
			Status:   workflowStatusToProto(wf.Status),
			Input:    wf.Input,
			Output:   wf.Output,
			ErrorMsg: wf.ErrorMsg,
			Tasks:    protoTasks,
		},
	}, nil
}

// ListWorkflows implements the CoordinatorService ListWorkflows RPC.
func (c *Coordinator) ListWorkflows(ctx context.Context, req *forgev1.ListWorkflowsRequest) (*forgev1.ListWorkflowsResponse, error) {
	statusFilter := protoToWorkflowStatus(req.GetStatus())
	pageSize := int(req.GetPageSize())
	if pageSize <= 0 {
		pageSize = 20
	}

	workflows, err := c.store.ListWorkflows(ctx, statusFilter, pageSize, 0)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list workflows: %v", err)
	}

	protoWFs := make([]*forgev1.WorkflowInstance, 0, len(workflows))
	for _, wf := range workflows {
		protoWFs = append(protoWFs, &forgev1.WorkflowInstance{
			Id:       wf.ID,
			Name:     wf.Name,
			Status:   workflowStatusToProto(wf.Status),
			ErrorMsg: wf.ErrorMsg,
		})
	}

	return &forgev1.ListWorkflowsResponse{Workflows: protoWFs}, nil
}

// CancelWorkflow implements the CoordinatorService CancelWorkflow RPC.
func (c *Coordinator) CancelWorkflow(ctx context.Context, req *forgev1.CancelWorkflowRequest) (*forgev1.CancelWorkflowResponse, error) {
	err := c.store.UpdateWorkflowStatus(ctx, req.GetWorkflowId(), storage.WorkflowStatusCancelled)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "cancel workflow: %v", err)
	}
	return &forgev1.CancelWorkflowResponse{}, nil
}

// OnTaskCompleted is called when a worker reports task completion.
//
// The task's on_result rules decide what happens next. Three of the four
// actions are handled here — continue, abort and skip — because none of them
// needs to move a task backwards. goto is rejected loudly rather than ignored:
// re-running part of a DAG has no defined treatment for the work already done
// in the nodes it jumps over, and pretending otherwise would silently produce
// a wrong result instead of an unhandled one.
func (c *Coordinator) OnTaskCompleted(ctx context.Context, taskID string, output json.RawMessage) error {
	task, err := c.store.GetTask(ctx, taskID)
	if err != nil {
		return fmt.Errorf("get task %s: %w", taskID, err)
	}

	if err := c.store.CompleteTask(ctx, taskID, output); err != nil {
		return fmt.Errorf("complete task %s: %w", taskID, err)
	}
	c.saveEvent(ctx, task.WorkflowID, taskID, storage.EventTaskCompleted, output)

	route := c.taskDef(task).OnResult.Resolve("success")
	switch route.Action {
	case RouteActionAbort:
		// A successful task that aborts the run is a deliberate early exit, not
		// a failure of this task: the message says which task asked for it.
		payload, _ := json.Marshal(map[string]string{
			"error":  fmt.Sprintf("task %s routed to abort", task.TaskName),
			"reason": "on_result.abort",
		})
		if err := c.store.UpdateWorkflowStatus(ctx, task.WorkflowID, storage.WorkflowStatusFailed); err != nil {
			return fmt.Errorf("abort workflow %s: %w", task.WorkflowID, err)
		}
		c.saveEvent(ctx, task.WorkflowID, "", storage.EventWorkflowFailed, payload)
		c.evictDAGCache(task.WorkflowID)
		return nil

	case RouteActionSkip:
		// "Skip the downstream": this task is done, its successors are not run.
		// advanceWorkflow reads that from the completed task's route, so the
		// branch stops whether or not other tasks finish at the same moment.
		return c.advanceWorkflow(ctx, task.WorkflowID, task.ID)

	case RouteActionGoto:
		return fmt.Errorf("task %s: on_result.goto is not supported yet", task.TaskName)
	}

	return c.advanceWorkflow(ctx, task.WorkflowID, task.ID)
}

// advanceWorkflow moves a workflow forward from whatever its tasks currently
// say, and reports whether it is finished.
//
// Everything here is derived from persisted task rows plus the DAG, never from
// "which completion handler ran first". That matters because two tasks can
// finish concurrently: an earlier version marked a task's successors READY as a
// side effect of that task finishing, so a `skip` decided by one branch could
// be undone by an unrelated task finishing at the same moment — whether the
// skip survived depended on goroutine scheduling. Recomputing eligibility from
// state instead of mutating it incrementally makes the outcome order-independent.
//
// The rule for a PENDING task is:
//
//	blocked   — some ancestor is COMPLETED with an on_result route of skip, so
//	            the author asked for this branch to stop. Settle it SKIPPED.
//	ready     — every dependency is settled (COMPLETED or SKIPPED) and nothing
//	            blocked it. Mark it READY.
//	otherwise — leave it alone.
//
// A task skipped by its own CONDITION is settled too, but it does not block:
// "this task does not apply" and "do not run my downstream" are different
// statements, and only on_result says the second.
func (c *Coordinator) advanceWorkflow(ctx context.Context, workflowID, justFinishedID string) error {
	// Serialise the read-decide-write below (see advanceMu).
	c.advanceMu.Lock()
	defer c.advanceMu.Unlock()

	dag := c.cachedDAG(workflowID)
	if dag == nil {
		// Without the definitions nothing can be decided; the workflow keeps
		// whatever state it has rather than being advanced on a guess.
		return fmt.Errorf("advance workflow %s: DAG is not cached", workflowID)
	}

	tasks, err := c.store.ListTasksByWorkflow(ctx, workflowID)
	if err != nil {
		return fmt.Errorf("list tasks for workflow %s: %w", workflowID, err)
	}

	settled := make(map[string]bool, len(tasks))     // COMPLETED or SKIPPED
	blockedBy := make(map[string]string, len(tasks)) // task -> ancestor that skipped this branch
	byName := make(map[string]*storage.Task, len(tasks))

	for _, t := range tasks {
		byName[t.TaskName] = t
		if t.Status == storage.TaskStatusCompleted || t.Status == storage.TaskStatusSkipped {
			settled[t.TaskName] = true
		}
		if t.ID == justFinishedID {
			// The just-finished task may not be readable as settled yet
			// (read-after-write). Its handler wrote a terminal state before
			// calling us, so treating it as settled is what the caller means.
			settled[t.TaskName] = true
		}
	}

	// Which branch-stopping tasks actually ran to completion? Only those block
	// their downstream. A task that declared a skip route but is still pending
	// has decided nothing yet.
	for name, t := range byName {
		if t.Status != storage.TaskStatusCompleted {
			continue
		}
		def := dag.Tasks[name]
		if def == nil || def.OnResult.Resolve("success").Action != RouteActionSkip {
			continue
		}
		for _, downstream := range reachableFrom(dag, name) {
			if _, already := blockedBy[downstream]; !already {
				blockedBy[downstream] = name
			}
		}
	}

	progressed := true
	for progressed {
		progressed = false

		for _, t := range tasks {
			if settled[t.TaskName] {
				continue
			}

			// A task blocked by a branch-stop is settled as skipped. It is
			// only settled once it has not started: work already dispatched is
			// not ours to retract.
			if blocker, blocked := blockedBy[t.TaskName]; blocked {
				if t.Status == storage.TaskStatusPending || t.Status == storage.TaskStatusReady {
					if err := c.settleSkipped(ctx, workflowID, t, fmt.Sprintf("upstream task %s routed to skip", blocker)); err != nil {
						return err
					}
					settled[t.TaskName] = true
					progressed = true
				}
				continue
			}

			if t.Status != storage.TaskStatusPending {
				continue
			}
			ready := true
			for _, dep := range t.DependsOn {
				if !settled[dep] {
					ready = false
					break
				}
			}
			if !ready {
				continue
			}
			if err := c.store.UpdateTaskStatus(ctx, t.ID, storage.TaskStatusReady); err != nil {
				return fmt.Errorf("mark task %s ready: %w", t.ID, err)
			}
			t.Status = storage.TaskStatusReady
			progressed = true
		}
	}

	// Nothing left to run means the workflow is done. A task still holding a
	// non-terminal status keeps it open.
	for _, t := range tasks {
		if settled[t.TaskName] {
			continue
		}
		if t.Status != storage.TaskStatusCompleted && t.Status != storage.TaskStatusSkipped {
			go c.scheduleReadyTasks(context.Background(), workflowID)
			return nil
		}
	}

	if err := c.store.UpdateWorkflowStatus(ctx, workflowID, storage.WorkflowStatusCompleted); err != nil {
		return fmt.Errorf("complete workflow %s: %w", workflowID, err)
	}
	c.saveEvent(ctx, workflowID, "", storage.EventWorkflowCompleted, nil)
	c.evictDAGCache(workflowID)
	return nil
}

// settleSkipped marks one task SKIPPED and records why.
func (c *Coordinator) settleSkipped(ctx context.Context, workflowID string, t *storage.Task, reason string) error {
	if err := c.store.UpdateTaskStatus(ctx, t.ID, storage.TaskStatusSkipped); err != nil {
		return fmt.Errorf("skip task %s: %w", t.ID, err)
	}
	payload, _ := json.Marshal(map[string]string{"reason": reason})
	c.saveEvent(ctx, workflowID, t.ID, storage.EventTaskSkipped, payload)
	return nil
}

// reachableFrom returns every task name that transitively depends on root,
// excluding root itself.
//
// The DAG map key is the task name: TaskDef.Name is yaml:"-" and stays empty
// after parsing, so reading it here would collect nothing and quietly turn a
// skip into a continue.
func reachableFrom(dag *DAG, root string) []string {
	var out []string
	seen := map[string]bool{}
	queue := []string{root}
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		for taskName, def := range dag.Tasks {
			if taskName == root || seen[taskName] {
				continue
			}
			for _, dep := range def.DependsOn {
				if dep == name {
					seen[taskName] = true
					out = append(out, taskName)
					queue = append(queue, taskName)
					break
				}
			}
		}
	}
	return out
}

// OnTaskFailed is called when a worker reports task failure.
//
// A task with attempts left is rescheduled rather than failed: the retry policy
// declares how many attempts it gets and how long to wait between them, and
// those declarations were parsed into the DAG long before anything acted on
// them. Only once the budget is spent does the failure reach the workflow.
//
// If the failed task's on_failure policy is COMPENSATE, it triggers Saga
// compensation. Otherwise, it marks the workflow as failed.
func (c *Coordinator) OnTaskFailed(ctx context.Context, taskID string, errMsg string) error {
	task, err := c.store.GetTask(ctx, taskID)
	if err != nil {
		return fmt.Errorf("get task %s: %w", taskID, err)
	}

	// Retry first: a task that still has attempts left has not failed in the
	// sense the workflow cares about, and failing it here would make
	// max_attempts meaningless.
	if c.tryReschedule(ctx, task, errMsg) {
		return nil
	}

	if err := c.store.FailTask(ctx, taskID, errMsg); err != nil {
		return fmt.Errorf("fail task %s: %w", taskID, err)
	}
	payload, _ := json.Marshal(map[string]string{"error": errMsg})
	c.saveEvent(ctx, task.WorkflowID, taskID, storage.EventTaskFailed, payload)

	// Check if this task has on_failure: COMPENSATE — if so, trigger Saga.
	// We need the DAG definition to check. Store it on workflow submission
	// so we can retrieve it here.
	if c.shouldCompensate(task.WorkflowID, task.TaskName) {
		// Transition workflow to COMPENSATING state.
		if err := c.store.UpdateWorkflowStatus(ctx, task.WorkflowID, storage.WorkflowStatusCompensating); err != nil {
			return fmt.Errorf("set workflow %s compensating: %w", task.WorkflowID, err)
		}
		go c.runCompensation(context.Background(), task.WorkflowID, task.TaskName)
		return nil
	}

	// Default: mark workflow as failed
	if err := c.store.UpdateWorkflowStatus(ctx, task.WorkflowID, storage.WorkflowStatusFailed); err != nil {
		return fmt.Errorf("fail workflow %s: %w", task.WorkflowID, err)
	}
	c.saveEvent(ctx, task.WorkflowID, "", storage.EventWorkflowFailed, payload)
	c.evictDAGCache(task.WorkflowID)
	return nil
}

// tryReschedule returns a failed task to the queue when its retry budget allows
// it, reporting whether it did.
//
// The decision uses EvaluateRetry, which already knows the policy: attempts
// left, the backoff shape, and the full-jitter delay. Nothing new is invented
// here — the piece that was missing was a caller.
//
// A zero MaxAttempts means one attempt (the default the DAG loader applies), so
// a task nobody configured retries zero times and behaves exactly as before.
func (c *Coordinator) tryReschedule(ctx context.Context, task *storage.Task, errMsg string) bool {
	if task.MaxAttempts <= 1 {
		return false
	}

	// Attempt counts starts, and MarkTaskRunning advanced it when this run
	// began — so the stored value already includes the attempt that just
	// failed. That makes max_attempts the total number of runs rather than one
	// more than it.
	decision := EvaluateRetry(&RetryableTask{
		ID:          task.ID,
		TaskName:    task.TaskName,
		WorkflowID:  task.WorkflowID,
		Handler:     task.Handler,
		Attempt:     task.Attempt,
		MaxAttempts: task.MaxAttempts,
		RetryPolicy: c.taskDef(task).Retry,
	})
	if !decision.ShouldRetry {
		// Out of attempts. Say so once here, where we still have the error and
		// the counts in hand, rather than leaving an operator to reconstruct it.
		LogDeadLetter(&RetryableTask{
			ID:          task.ID,
			TaskName:    task.TaskName,
			WorkflowID:  task.WorkflowID,
			Handler:     task.Handler,
			Attempt:     task.Attempt,
			MaxAttempts: task.MaxAttempts,
		}, errMsg)
		return false
	}

	notBefore := time.Now().Add(decision.Delay)
	if err := c.store.ScheduleTaskRetry(ctx, task.ID, notBefore); err != nil {
		// Could not requeue: fall through to the ordinary failure path rather
		// than leaving the task stuck in a state nothing will advance.
		log.Printf("ERROR: schedule retry for task %s: %v", task.ID, err)
		return false
	}

	payload, _ := json.Marshal(map[string]any{
		"error":        errMsg,
		"attempt":      task.Attempt,
		"max_attempts": task.MaxAttempts,
		"retry_after":  notBefore.UTC().Format(time.RFC3339Nano),
	})
	c.saveEvent(ctx, task.WorkflowID, task.ID, storage.EventTaskRetrying, payload)
	log.Printf("INFO: task %s retry %d/%d scheduled in %s",
		task.ID, task.Attempt, task.MaxAttempts, decision.Delay)

	// Wake the scheduler when the delay elapses. Without this the task would be
	// READY but only picked up the next time some other event triggered a scan,
	// which for a quiet workflow means never.
	time.AfterFunc(decision.Delay, func() {
		c.scheduleReadyTasks(context.Background(), task.WorkflowID)
	})
	return true
}

// scheduleReadyTasks finds READY tasks for a workflow and dispatches them to workers.
func (c *Coordinator) scheduleReadyTasks(ctx context.Context, workflowID string) {
	tasks, err := c.store.ListTasksByWorkflow(ctx, workflowID)
	if err != nil {
		return
	}

	now := time.Now()
	for _, task := range tasks {
		if task.Status != storage.TaskStatusReady {
			continue
		}

		// A retry waits out its backoff before it is eligible again. READY plus
		// a future ScheduledAt is how that wait is expressed, so a task that
		// is READY but not yet due is skipped rather than dispatched early.
		if task.ScheduledAt != nil && task.ScheduledAt.After(now) {
			continue
		}

		// A task's own condition decides whether it runs at all. It is checked
		// before findWorker: whether this task should run has nothing to do
		// with whether a worker happens to be free, and a skipped task must not
		// hold a capacity slot while we look for one.
		skip, err := c.shouldSkip(ctx, workflowID, task)
		if err != nil {
			// A condition that cannot be evaluated is a configuration error,
			// and failing closed is the only safe direction: treating it as
			// "run anyway" would execute work the author asked to be guarded.
			log.Printf("ERROR: evaluate condition for task %s: %v", task.ID, err)
			if failErr := c.OnTaskFailed(ctx, task.ID, fmt.Sprintf("condition: %v", err)); failErr != nil {
				log.Printf("ERROR: handle task %s condition failure: %v", task.ID, failErr)
			}
			continue
		}
		if skip {
			c.markTaskSkipped(ctx, workflowID, task)
			continue
		}

		worker := c.findWorker(task.Handler)
		if worker == nil {
			continue
		}

		// Claim the transition before dispatching. Several scanners can run at
		// once (submit, every workflow advance, a retry timer), so two of them
		// can see this task as READY; only the one that wins READY→SCHEDULED
		// may dispatch it. Without this the task reached a worker twice.
		won, err := c.store.MarkTaskScheduled(ctx, task.ID)
		if err != nil {
			log.Printf("ERROR: claim task %s for scheduling: %v", task.ID, err)
			continue
		}
		if !won {
			continue
		}

		c.saveEvent(ctx, workflowID, task.ID, storage.EventTaskScheduled, nil)

		// Dispatch to worker via gRPC
		go c.dispatchTask(context.Background(), worker, task)
	}
}

// shouldSkip evaluates a task's condition against what the run knows so far.
//
// An empty condition means "always run" — that is how every task that predates
// this feature keeps behaving exactly as before.
//
// The context exposes the DAG's declared outputs: a task named `plan` that
// declares `output: plan` becomes `results.plan`. That is the same naming the
// template renderer uses for `{{.plan}}`, so a condition and the params of the
// tasks around it read from one vocabulary rather than two.
func (c *Coordinator) shouldSkip(ctx context.Context, workflowID string, task *storage.Task) (bool, error) {
	def := c.taskDef(task)
	if def.Condition == "" {
		return false, nil
	}

	evaluator, err := c.celEvaluator()
	if err != nil {
		return false, err
	}

	results, err := c.completedOutputs(ctx, workflowID, task.ID)
	if err != nil {
		return false, err
	}

	ok, err := evaluator.Eval(def.Condition, map[string]any{
		"results":     results,
		"workflow_id": workflowID,
	})
	if err != nil {
		return false, err
	}
	return !ok, nil
}

// completedOutputs gathers the workflow's declared outputs so far, keyed by the
// name each task declared in `output`.
//
// Only COMPLETED tasks contribute, and only when they declared a name: a task
// with no `output` has nothing to refer to, and a skipped one never produced a
// value. Both cases are simply absent from the map, which is what an expression
// reading them should see.
func (c *Coordinator) completedOutputs(ctx context.Context, workflowID, excludeTaskID string) (map[string]any, error) {
	tasks, err := c.store.ListTasksByWorkflow(ctx, workflowID)
	if err != nil {
		return nil, err
	}

	// Resolve output names through the DAG, since the name lives on the
	// definition and not on the stored task row.
	dag := c.cachedDAG(workflowID)

	out := make(map[string]any, len(tasks))
	for _, t := range tasks {
		if t.ID == excludeTaskID || t.Status != storage.TaskStatusCompleted || len(t.Output) == 0 {
			continue
		}
		if dag == nil {
			continue
		}
		def, ok := dag.Tasks[t.TaskName]
		if !ok || def.Output == "" {
			continue
		}
		var value any
		if err := json.Unmarshal(t.Output, &value); err != nil {
			// Store the raw text rather than dropping the value: a condition
			// comparing against it should see what the task actually produced.
			value = string(t.Output)
		}
		out[def.Output] = value
	}
	return out, nil
}

// celEvaluator returns the process-wide CEL evaluator, creating it on first use.
//
// It is cached on the Coordinator because compiling expressions is the
// expensive part and the evaluator caches programs: one instance means a
// condition compiled on the first task is reused by every later one.
func (c *Coordinator) celEvaluator() (*CELEvaluator, error) {
	c.celOnce.Do(func() {
		c.cel, c.celErr = NewCELEvaluator()
	})
	return c.cel, c.celErr
}

// markTaskSkipped records that a task's condition excluded it, then advances the
// workflow past it.
//
// A condition-skipped task is a satisfied dependency: "this task does not apply
// here" is not the same statement as "do not run my downstream" (which is what
// on_result: skip says). So it settles as SKIPPED and blocks nothing.
func (c *Coordinator) markTaskSkipped(ctx context.Context, workflowID string, task *storage.Task) {
	if err := c.store.UpdateTaskStatus(ctx, task.ID, storage.TaskStatusSkipped); err != nil {
		log.Printf("ERROR: mark task %s skipped: %v", task.ID, err)
		return
	}

	payload, _ := json.Marshal(map[string]string{
		"reason":    "condition evaluated to false",
		"condition": c.conditionOf(task),
	})
	c.saveEvent(ctx, workflowID, task.ID, storage.EventTaskSkipped, payload)

	if err := c.advanceWorkflow(ctx, workflowID, task.ID); err != nil {
		log.Printf("ERROR: advance workflow past skipped task %s: %v", task.ID, err)
	}
}

// conditionOf reports a task's declared condition, for the audit payload.
func (c *Coordinator) conditionOf(task *storage.Task) string {
	return c.taskDef(task).Condition
}

// findWorker selects a worker that supports the given handler.
// When WorkerManager is configured (distributed mode), it searches active workers
// from the WorkerManager. Otherwise, falls back to the legacy c.workers map
// (standalone/test mode).
func (c *Coordinator) findWorker(handler string) *WorkerEntry {
	// Distributed mode: use WorkerManager
	if c.workerMgr != nil {
		for _, w := range c.workerMgr.ActiveWorkers() {
			if w.ActiveTasks >= w.Capacity {
				continue
			}
			for _, h := range w.Handlers {
				if h == handler {
					return &WorkerEntry{
						ID:       w.Registration.ID,
						Addr:     w.Registration.Addr,
						Handlers: w.Handlers,
						Capacity: w.Capacity,
						Active:   w.ActiveTasks,
						Conn:     w.Conn,
						Client:   w.Client,
					}
				}
			}
		}
		return nil
	}

	// Standalone/test mode: use legacy workers map
	c.mu.RLock()
	defer c.mu.RUnlock()

	for _, w := range c.workers {
		if w.Active >= w.Capacity {
			continue
		}
		for _, h := range w.Handlers {
			if h == handler {
				return w
			}
		}
	}
	return nil
}

// renderInputForTask renders a task's frozen params against the workflow's
// inputs and its completed dependencies' named outputs. It runs at dispatch
// time because outputs only exist then.
func (c *Coordinator) renderInputForTask(ctx context.Context, task *storage.Task) (json.RawMessage, error) {
	var params map[string]any
	if len(task.Input) > 0 {
		if err := json.Unmarshal(task.Input, &params); err != nil {
			return nil, fmt.Errorf("parse task input: %w", err)
		}
	}

	inputs := map[string]any{}
	wf, err := c.store.GetWorkflow(ctx, task.WorkflowID)
	if err != nil {
		return nil, fmt.Errorf("load workflow %s: %w", task.WorkflowID, err)
	}
	if len(wf.Input) > 0 {
		if err := json.Unmarshal(wf.Input, &inputs); err != nil {
			return nil, fmt.Errorf("parse workflow input: %w", err)
		}
	}

	outputs := map[string]any{}
	c.dagCacheMu.RLock()
	dag := c.dagCache[task.WorkflowID]
	c.dagCacheMu.RUnlock()
	if dag != nil {
		tasks, err := c.store.ListTasksByWorkflow(ctx, task.WorkflowID)
		if err != nil {
			return nil, fmt.Errorf("list tasks: %w", err)
		}
		for _, t := range tasks {
			if t.TaskName == task.TaskName || t.Status != storage.TaskStatusCompleted || len(t.Output) == 0 {
				continue
			}
			def, ok := dag.Tasks[t.TaskName]
			if !ok || def.Output == "" {
				continue
			}
			var value any
			if err := json.Unmarshal(t.Output, &value); err != nil {
				return nil, fmt.Errorf("parse output of task %s: %w", t.TaskName, err)
			}
			outputs[def.Output] = value
		}
	}

	rendered, err := c.paramRenderer(params, inputs, outputs)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(rendered)
	if err != nil {
		return nil, fmt.Errorf("marshal rendered params: %w", err)
	}
	return body, nil
}

// dispatchTask sends a task to a worker for execution via the ExecuteTask RPC.
func (c *Coordinator) dispatchTask(ctx context.Context, worker *WorkerEntry, task *storage.Task) {
	// Render templates before the task starts, so a rendering failure fails
	// the task from READY instead of dispatching literal templates.
	input := task.Input
	if c.paramRenderer != nil {
		rendered, err := c.renderInputForTask(ctx, task)
		if err != nil {
			log.Printf("ERROR: render params for task %s: %v", task.ID, err)
			if failErr := c.OnTaskFailed(ctx, task.ID, fmt.Sprintf("render params: %v", err)); failErr != nil {
				log.Printf("ERROR: handle task %s render failure: %v", task.ID, failErr)
			}
			return
		}
		input = rendered
	}

	// GPU tasks run in Kubernetes under Kueue, not on a worker. They are
	// submitted and then REPORTED BACK asynchronously by the reconciler —
	// waiting here would block the dispatch goroutine for the whole job, and
	// nothing on a worker would ever answer. Everything below this branch is
	// the existing worker RPC path, unchanged.
	if c.kueue != nil && c.kueue.Enabled() && IsGPUTask(c.taskDef(task)) {
		c.dispatchKueueTask(ctx, task, input)
		return
	}

	// Track active task count on the appropriate data structure.
	if c.workerMgr != nil {
		c.workerMgr.mu.Lock()
		if wi, ok := c.workerMgr.workers[worker.ID]; ok {
			wi.ActiveTasks++
		}
		c.workerMgr.mu.Unlock()
		defer func() {
			c.workerMgr.mu.Lock()
			if wi, ok := c.workerMgr.workers[worker.ID]; ok {
				wi.ActiveTasks--
			}
			c.workerMgr.mu.Unlock()
		}()
	} else {
		c.mu.Lock()
		worker.Active++
		c.mu.Unlock()
		defer func() {
			c.mu.Lock()
			worker.Active--
			c.mu.Unlock()
		}()
	}

	// Update task to RUNNING, record its owner, and advance the attempt
	// counter — one write, because they are one fact: this attempt started.
	if err := c.store.MarkTaskRunning(ctx, task.ID, worker.ID); err != nil {
		log.Printf("ERROR: mark task %s running: %v", task.ID, err)
		return
	}
	c.saveEvent(ctx, task.WorkflowID, task.ID, storage.EventTaskStarted, nil)

	callStartedAt := time.Now().UTC()
	resp, err := worker.Client.ExecuteTask(ctx, &forgev1.TaskRequest{
		TaskId:     task.ID,
		WorkflowId: task.WorkflowID,
		TaskName:   task.TaskName,
		Handler:    task.Handler,
		Input:      input,
	})
	callEndedAt := time.Now().UTC()
	if err != nil {
		c.observeTaskCall(ctx, forgexruntime.TaskCall{
			WorkflowID: task.WorkflowID,
			TaskID:     task.ID,
			TaskName:   task.TaskName,
			Handler:    task.Handler,
			WorkerID:   worker.ID,
			Input:      input,
			Error:      err.Error(),
			Success:    false,
			StartedAt:  callStartedAt,
			EndedAt:    callEndedAt,
		})
		if failErr := c.OnTaskFailed(ctx, task.ID, err.Error()); failErr != nil {
			log.Printf("ERROR: handle task %s failure: %v", task.ID, failErr)
		}
		return
	}

	c.observeTaskCall(ctx, forgexruntime.TaskCall{
		WorkflowID: task.WorkflowID,
		TaskID:     task.ID,
		TaskName:   task.TaskName,
		Handler:    task.Handler,
		WorkerID:   worker.ID,
		Input:      input,
		Output:     resp.GetOutput(),
		Error:      resp.GetErrorMsg(),
		Success:    resp.GetSuccess(),
		StartedAt:  callStartedAt,
		EndedAt:    callEndedAt,
	})

	if resp.GetPaused() {
		// Held for a human. This is NOT a failure and NOT a completion: park
		// the task so the worker is released, and let an approval move it back
		// to READY for normal re-dispatch.
		if pauseErr := c.OnTaskPaused(ctx, task.ID, resp.GetPauseReason()); pauseErr != nil {
			log.Printf("ERROR: handle task %s pause: %v", task.ID, pauseErr)
		}
		return
	}

	if resp.GetSuccess() {
		if err := c.OnTaskCompleted(ctx, task.ID, resp.GetOutput()); err != nil {
			log.Printf("ERROR: handle task %s completion: %v", task.ID, err)
		}
	} else {
		if err := c.OnTaskFailed(ctx, task.ID, resp.GetErrorMsg()); err != nil {
			log.Printf("ERROR: handle task %s failure: %v", task.ID, err)
		}
	}
}

func (c *Coordinator) observeTaskCall(ctx context.Context, call forgexruntime.TaskCall) {
	observer, ok := c.runtimeObserver.(forgexruntime.TaskCallObserver)
	if !ok || observer == nil {
		return
	}
	if err := observer.ObserveTaskCall(ctx, call); err != nil {
		log.Printf("WARN: forgex runtime observer task_call workflow=%s task=%s handler=%s: %v", call.WorkflowID, call.TaskID, call.Handler, err)
	}
}

// saveEvent is a helper that persists an event without blocking on errors.
func (c *Coordinator) saveEvent(ctx context.Context, workflowID, taskID string, eventType storage.EventType, payload json.RawMessage) {
	c.seqMu.Lock()
	c.seqNum++
	seq := c.seqNum
	c.seqMu.Unlock()

	event := &storage.Event{
		WorkflowID:  workflowID,
		TaskID:      taskID,
		Type:        eventType,
		Payload:     payload,
		SequenceNum: seq,
	}
	if err := c.store.SaveEvent(ctx, event); err != nil {
		log.Printf("ERROR: save event %s for workflow %s: %v", eventType, workflowID, err)
		return
	}
	if c.runtimeObserver != nil {
		if err := c.runtimeObserver.ObserveEvent(ctx, event); err != nil {
			log.Printf("WARN: forgex runtime observer event=%s workflow=%s task=%s: %v", eventType, workflowID, taskID, err)
		}
	}
	c.publishEvent(ctx, event)
}

// SetEventBus installs the notification publisher. Nil keeps events
// storage-only, which is the behaviour without any bus configured.
func (c *Coordinator) SetEventBus(p EventPublisher) { c.eventBus = p }

// SetParamRenderer installs dispatch-time parameter rendering. Nil (the
// default) keeps the historical behaviour: params are sent exactly as
// submitted, templates untouched.
func (c *Coordinator) SetParamRenderer(fn ParamRenderer) { c.paramRenderer = fn }

// SetKueue installs the GPU-queue manager. Nil (the default) keeps dispatch
// unchanged: tasks go to workers exactly as before. The dispatch routing
// that consumes this is tracked as #8b; installing it now means a
// configured-but-broken cluster fails at startup, not at first submit.
// SetKueue installs the Kueue manager: GPU tasks are then submitted to
// Kubernetes instead of a worker, with results reported back by the reconciler.
func (c *Coordinator) SetKueue(m *KueueManager) { c.kueue = m }

// Store exposes the storage backend. Assembly layers need it to build
// background workers (the Kueue reconciler, the timeout manager) that scan the
// same task tables the coordinator owns.
func (c *Coordinator) Store() storage.Storage { return c.store }

// Kueue returns the installed GPU-queue manager, or nil when unset — the
// assembly-layer counterpart of SetKueue (used by tests and diagnostics).
func (c *Coordinator) Kueue() *KueueManager { return c.kueue }

// publishEvent fans a persisted event out on the bus. Notification semantics:
// the event is already durable in storage, so a publish failure is logged
// and swallowed — the bus being down must not fail the workflow that
// produced the event.
func (c *Coordinator) publishEvent(ctx context.Context, event *storage.Event) {
	if c.eventBus == nil {
		return
	}
	body, err := json.Marshal(map[string]any{
		"workflow_id":  event.WorkflowID,
		"task_id":      event.TaskID,
		"type":         string(event.Type),
		"sequence_num": event.SequenceNum,
		"payload":      event.Payload,
		"timestamp":    event.Timestamp,
	})
	if err != nil {
		log.Printf("WARN: marshal event %s workflow %s for publish: %v", event.Type, event.WorkflowID, err)
		return
	}
	if err := c.eventBus.Publish(ctx, EventChannel, string(body)); err != nil {
		log.Printf("WARN: publish event %s workflow %s: %v", event.Type, event.WorkflowID, err)
	}
}

// GetOverview implements the CoordinatorService GetOverview RPC.
// It aggregates workflow and worker statistics for the admin dashboard.
func (c *Coordinator) GetOverview(ctx context.Context, _ *forgev1.GetOverviewRequest) (*forgev1.GetOverviewResponse, error) {
	// Use efficient COUNT query instead of loading all workflow rows.
	counts, err := c.store.CountWorkflows(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "count workflows: %v", err)
	}

	var total int32
	for _, cnt := range counts {
		total += cnt
	}
	active := counts[storage.WorkflowStatusRunning]
	completed := counts[storage.WorkflowStatusCompleted]
	failed := counts[storage.WorkflowStatusFailed]

	var successRate float64
	if completed+failed > 0 {
		successRate = float64(completed) / float64(completed+failed)
	}

	var totalWorkers, healthyWorkers int32
	if c.workerMgr != nil {
		for _, w := range c.workerMgr.AllWorkers() {
			totalWorkers++
			if w.Status == WorkerStatusActive {
				healthyWorkers++
			}
		}
	} else {
		c.mu.RLock()
		totalWorkers = int32(len(c.workers))
		healthyWorkers = totalWorkers
		c.mu.RUnlock()
	}

	// Queue depth: count READY tasks across RUNNING workflows.
	// Only load the running workflows (bounded by active count).
	var queueDepth int32
	if active > 0 {
		runningWFs, err := c.store.ListWorkflows(ctx, storage.WorkflowStatusRunning, int(active), 0)
		if err == nil {
			for _, wf := range runningWFs {
				tasks, err := c.store.ListTasksByWorkflow(ctx, wf.ID)
				if err != nil {
					continue
				}
				for _, t := range tasks {
					if t.Status == storage.TaskStatusReady {
						queueDepth++
					}
				}
			}
		}
	}

	return &forgev1.GetOverviewResponse{
		ActiveWorkflows: active,
		TotalWorkflows:  total,
		TotalWorkers:    totalWorkers,
		HealthyWorkers:  healthyWorkers,
		SuccessRate:     successRate,
		QueueDepth:      queueDepth,
		FailedWorkflows: failed,
	}, nil
}

// ListWorkers implements the CoordinatorService ListWorkers RPC.
// It returns the current set of registered workers with health status.
// Supports offset-based pagination via page_token (stringified integer offset).
func (c *Coordinator) ListWorkers(ctx context.Context, req *forgev1.ListWorkersRequest) (*forgev1.ListWorkersResponse, error) {
	langFilter := req.GetLanguageFilter()
	pageSize := int(req.GetPageSize())
	if pageSize <= 0 {
		pageSize = 50
	}

	// Parse offset from page_token (empty = 0).
	offset := 0
	if pt := req.GetPageToken(); pt != "" {
		if n, err := strconv.Atoi(pt); err == nil && n > 0 {
			offset = n
		}
	}

	var all []*forgev1.DashboardWorkerInfo

	if c.workerMgr != nil {
		for _, w := range c.workerMgr.AllWorkers() {
			lang := ""
			if w.Registration.Metadata != nil {
				lang = w.Registration.Metadata["language"]
			}
			if langFilter != "" && lang != langFilter {
				continue
			}

			wStatus := "healthy"
			switch w.Status {
			case WorkerStatusSuspect:
				wStatus = "unhealthy"
			case WorkerStatusDead:
				wStatus = "offline"
			}

			labels := make(map[string]string)
			if w.Registration.Metadata != nil {
				for k, v := range w.Registration.Metadata {
					labels[k] = v
				}
			}

			all = append(all, &forgev1.DashboardWorkerInfo{
				Id:          w.Registration.ID,
				Addr:        w.Registration.Addr,
				Labels:      labels,
				Capacity:    int32(w.Capacity),
				ActiveTasks: int32(w.ActiveTasks),
				Status:      wStatus,
				Handlers:    w.Handlers,
			})
		}
	} else {
		c.mu.RLock()
		for _, w := range c.workers {
			all = append(all, &forgev1.DashboardWorkerInfo{
				Id:          w.ID,
				Addr:        w.Addr,
				Capacity:    int32(w.Capacity),
				ActiveTasks: int32(w.Active),
				Status:      "healthy",
				Handlers:    w.Handlers,
			})
		}
		c.mu.RUnlock()
	}

	// Apply offset-based pagination.
	var nextPageToken string
	if offset >= len(all) {
		all = nil
	} else {
		all = all[offset:]
		if len(all) > pageSize {
			all = all[:pageSize]
			nextPageToken = strconv.Itoa(offset + pageSize)
		}
	}

	return &forgev1.ListWorkersResponse{Workers: all, NextPageToken: nextPageToken}, nil
}

// Storage returns the coordinator's storage backend (used by tests).
func (c *Coordinator) Storage() storage.Storage {
	return c.store
}

// GetDAG returns the cached DAG for a workflow (used by tests and saga).
func (c *Coordinator) GetDAG(workflowID string) *DAG {
	c.dagCacheMu.RLock()
	defer c.dagCacheMu.RUnlock()
	return c.dagCache[workflowID]
}

// evictDAGCache removes a workflow's DAG from the cache (called on workflow completion/failure).
func (c *Coordinator) evictDAGCache(workflowID string) {
	c.dagCacheMu.Lock()
	delete(c.dagCache, workflowID)
	c.dagCacheMu.Unlock()
}

// shouldCompensate checks if the failed task's on_failure policy is COMPENSATE.
func (c *Coordinator) shouldCompensate(workflowID, taskName string) bool {
	c.dagCacheMu.RLock()
	dag, ok := c.dagCache[workflowID]
	c.dagCacheMu.RUnlock()
	if !ok {
		return false
	}
	taskDef, ok := dag.Tasks[taskName]
	if !ok {
		return false
	}
	return taskDef.OnFailure == FailureActionCompensate
}

// runCompensation executes Saga compensation for a failed workflow.
// It delegates to saga.Compensator.BuildPlan + saga.Execute for a single source of truth.
func (c *Coordinator) runCompensation(ctx context.Context, workflowID, failedTaskName string) {
	c.dagCacheMu.RLock()
	dag, ok := c.dagCache[workflowID]
	c.dagCacheMu.RUnlock()
	if !ok {
		log.Printf("ERROR: saga: no DAG cached for workflow %s", workflowID)
		return
	}

	compensator := saga.NewCompensator(c.store)
	plan, err := compensator.BuildPlan(ctx, dag, workflowID, failedTaskName)
	if err != nil {
		log.Printf("ERROR: saga: build plan for workflow %s: %v", workflowID, err)
		return
	}

	if len(plan.Steps) == 0 {
		log.Printf("INFO: saga: no compensation steps for workflow %s", workflowID)
	}

	results := saga.Execute(plan, func(step saga.CompensationStep) error {
		worker := c.findWorker(step.CompensateHandler)
		if worker == nil {
			return fmt.Errorf("no worker for handler %q", step.CompensateHandler)
		}

		c.saveEvent(ctx, workflowID, step.OriginalTaskID, storage.EventTaskCompensating, nil)

		resp, err := worker.Client.ExecuteTask(ctx, &forgev1.TaskRequest{
			TaskId:     step.OriginalTaskID + "-compensate",
			WorkflowId: workflowID,
			TaskName:   step.TaskName + ".compensate",
			Handler:    step.CompensateHandler,
			Input:      step.OriginalInput,
		})
		if err != nil {
			return err
		}
		if !resp.GetSuccess() {
			return fmt.Errorf("compensation returned failure: %s", resp.GetErrorMsg())
		}
		return nil
	})

	if !saga.AllSucceeded(results) {
		log.Printf("WARN: saga: some compensation steps failed for workflow %s", workflowID)
	}

	// After all compensations, mark workflow as failed.
	if err := c.store.UpdateWorkflowStatus(ctx, workflowID, storage.WorkflowStatusFailed); err != nil {
		log.Printf("ERROR: saga: fail workflow %s after compensation: %v", workflowID, err)
	}
	payload, _ := json.Marshal(map[string]string{"error": "saga compensation completed", "failed_task": failedTaskName})
	c.saveEvent(ctx, workflowID, "", storage.EventWorkflowFailed, payload)

	log.Printf("INFO: saga: compensation complete for workflow %s", workflowID)
	c.evictDAGCache(workflowID)
}

// workflowStatusToProto converts internal status to proto enum.
func workflowStatusToProto(s storage.WorkflowStatus) forgev1.WorkflowStatus {
	switch s {
	case storage.WorkflowStatusPending:
		return forgev1.WorkflowStatus_WORKFLOW_STATUS_PENDING
	case storage.WorkflowStatusRunning:
		return forgev1.WorkflowStatus_WORKFLOW_STATUS_RUNNING
	case storage.WorkflowStatusCompleted:
		return forgev1.WorkflowStatus_WORKFLOW_STATUS_COMPLETED
	case storage.WorkflowStatusFailed:
		return forgev1.WorkflowStatus_WORKFLOW_STATUS_FAILED
	case storage.WorkflowStatusCancelled:
		return forgev1.WorkflowStatus_WORKFLOW_STATUS_CANCELLED
	case storage.WorkflowStatusCompensating:
		return forgev1.WorkflowStatus_WORKFLOW_STATUS_COMPENSATING
	case storage.WorkflowStatusPaused:
		return forgev1.WorkflowStatus_WORKFLOW_STATUS_PAUSED
	default:
		return forgev1.WorkflowStatus_WORKFLOW_STATUS_UNSPECIFIED
	}
}

// protoToWorkflowStatus converts proto enum to internal status.
func protoToWorkflowStatus(s forgev1.WorkflowStatus) storage.WorkflowStatus {
	switch s {
	case forgev1.WorkflowStatus_WORKFLOW_STATUS_PENDING:
		return storage.WorkflowStatusPending
	case forgev1.WorkflowStatus_WORKFLOW_STATUS_RUNNING:
		return storage.WorkflowStatusRunning
	case forgev1.WorkflowStatus_WORKFLOW_STATUS_COMPLETED:
		return storage.WorkflowStatusCompleted
	case forgev1.WorkflowStatus_WORKFLOW_STATUS_FAILED:
		return storage.WorkflowStatusFailed
	case forgev1.WorkflowStatus_WORKFLOW_STATUS_CANCELLED:
		return storage.WorkflowStatusCancelled
	case forgev1.WorkflowStatus_WORKFLOW_STATUS_COMPENSATING:
		return storage.WorkflowStatusCompensating
	case forgev1.WorkflowStatus_WORKFLOW_STATUS_PAUSED:
		return storage.WorkflowStatusPaused
	default:
		return ""
	}
}

// taskToProto converts an internal Task to a proto TaskInstance.
func taskToProto(t *storage.Task) *forgev1.TaskInstance {
	return &forgev1.TaskInstance{
		Id:          t.ID,
		TaskName:    t.TaskName,
		Handler:     t.Handler,
		Status:      taskStatusToProto(t.Status),
		WorkerId:    t.WorkerID,
		Input:       t.Input,
		Output:      t.Output,
		ErrorMsg:    t.ErrorMsg,
		Attempt:     int32(t.Attempt),
		MaxAttempts: int32(t.MaxAttempts),
	}
}

// taskStatusToProto converts internal task status to proto enum.
func taskStatusToProto(s storage.TaskStatus) forgev1.TaskStatus {
	switch s {
	case storage.TaskStatusPending:
		return forgev1.TaskStatus_TASK_STATUS_PENDING
	case storage.TaskStatusReady:
		return forgev1.TaskStatus_TASK_STATUS_READY
	case storage.TaskStatusScheduled:
		return forgev1.TaskStatus_TASK_STATUS_SCHEDULED
	case storage.TaskStatusRunning:
		return forgev1.TaskStatus_TASK_STATUS_RUNNING
	case storage.TaskStatusCompleted:
		return forgev1.TaskStatus_TASK_STATUS_COMPLETED
	case storage.TaskStatusFailed:
		return forgev1.TaskStatus_TASK_STATUS_FAILED
	case storage.TaskStatusSkipped:
		return forgev1.TaskStatus_TASK_STATUS_SKIPPED
	case storage.TaskStatusCompensating:
		return forgev1.TaskStatus_TASK_STATUS_COMPENSATING
	case storage.TaskStatusPaused:
		return forgev1.TaskStatus_TASK_STATUS_PAUSED
	default:
		return forgev1.TaskStatus_TASK_STATUS_UNSPECIFIED
	}
}
