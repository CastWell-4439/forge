package coordinator

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/castwell/forge/internal/discovery"
	"github.com/castwell/forge/internal/storage"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

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

// Coordinator orchestrates workflow execution by parsing DAGs,
// creating task instances, scheduling them to workers, and driving
// the workflow state machine to completion.
type Coordinator struct {
	forgev1.UnimplementedCoordinatorServiceServer

	store        storage.Storage
	workers      map[string]*WorkerEntry
	mu           sync.RWMutex
	seqNum       int64
	seqMu        sync.Mutex
	disco        discovery.Discovery
	leader       *LeaderController
	workerMgr    *WorkerManager
}

// NewCoordinator creates a new Coordinator with the given storage backend.
func NewCoordinator(store storage.Storage) *Coordinator {
	return &Coordinator{
		store:   store,
		workers: make(map[string]*WorkerEntry),
	}
}

// RegisterWorker registers a worker with the coordinator so tasks can be dispatched to it.
func (c *Coordinator) RegisterWorker(ctx context.Context, id, addr string, handlers []string, capacity int) error {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
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

	workflowID := uuid.New().String()
	now := time.Now()

	// Persist workflow instance
	wf := &storage.Workflow{
		ID:        workflowID,
		Name:      dag.Name,
		Status:    storage.WorkflowStatusPending,
		Input:     req.GetInput(),
		CreatedAt: now,
	}
	if err := c.store.SaveWorkflow(ctx, wf); err != nil {
		return nil, status.Errorf(codes.Internal, "save workflow: %v", err)
	}

	// Record submission event
	c.saveEvent(ctx, workflowID, "", storage.EventWorkflowSubmitted, nil)

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
// It updates the task, checks if successor tasks are now ready, and
// completes the workflow if all tasks are done.
func (c *Coordinator) OnTaskCompleted(ctx context.Context, taskID string, output json.RawMessage) error {
	task, err := c.store.GetTask(ctx, taskID)
	if err != nil {
		return fmt.Errorf("get task %s: %w", taskID, err)
	}

	if err := c.store.CompleteTask(ctx, taskID, output); err != nil {
		return fmt.Errorf("complete task %s: %w", taskID, err)
	}
	c.saveEvent(ctx, task.WorkflowID, taskID, storage.EventTaskCompleted, output)

	// Check if successors are now ready
	tasks, err := c.store.ListTasksByWorkflow(ctx, task.WorkflowID)
	if err != nil {
		return fmt.Errorf("list tasks for workflow %s: %w", task.WorkflowID, err)
	}

	completedTasks := make(map[string]bool)
	allDone := true
	for _, t := range tasks {
		if t.ID == taskID {
			completedTasks[t.TaskName] = true
			continue
		}
		if t.Status == storage.TaskStatusCompleted {
			completedTasks[t.TaskName] = true
		}
		if t.Status != storage.TaskStatusCompleted && t.Status != storage.TaskStatusSkipped {
			allDone = false
		}
	}

	// If all tasks completed, mark workflow as completed
	if allDone {
		if err := c.store.UpdateWorkflowStatus(ctx, task.WorkflowID, storage.WorkflowStatusCompleted); err != nil {
			return fmt.Errorf("complete workflow %s: %w", task.WorkflowID, err)
		}
		c.saveEvent(ctx, task.WorkflowID, "", storage.EventWorkflowCompleted, nil)
		return nil
	}

	// Find successor tasks that are now ready
	for _, t := range tasks {
		if t.Status != storage.TaskStatusPending {
			continue
		}
		ready := true
		for _, dep := range t.DependsOn {
			if !completedTasks[dep] {
				ready = false
				break
			}
		}
		if ready {
			if err := c.store.UpdateTaskStatus(ctx, t.ID, storage.TaskStatusReady); err != nil {
				return fmt.Errorf("mark task %s ready: %w", t.ID, err)
			}
		}
	}

	// Schedule newly ready tasks
	go c.scheduleReadyTasks(context.Background(), task.WorkflowID)
	return nil
}

// OnTaskFailed is called when a worker reports task failure.
func (c *Coordinator) OnTaskFailed(ctx context.Context, taskID string, errMsg string) error {
	task, err := c.store.GetTask(ctx, taskID)
	if err != nil {
		return fmt.Errorf("get task %s: %w", taskID, err)
	}

	if err := c.store.FailTask(ctx, taskID, errMsg); err != nil {
		return fmt.Errorf("fail task %s: %w", taskID, err)
	}
	payload, _ := json.Marshal(map[string]string{"error": errMsg})
	c.saveEvent(ctx, task.WorkflowID, taskID, storage.EventTaskFailed, payload)

	// Mark workflow as failed
	if err := c.store.UpdateWorkflowStatus(ctx, task.WorkflowID, storage.WorkflowStatusFailed); err != nil {
		return fmt.Errorf("fail workflow %s: %w", task.WorkflowID, err)
	}
	c.saveEvent(ctx, task.WorkflowID, "", storage.EventWorkflowFailed, payload)
	return nil
}

// scheduleReadyTasks finds READY tasks for a workflow and dispatches them to workers.
func (c *Coordinator) scheduleReadyTasks(ctx context.Context, workflowID string) {
	tasks, err := c.store.ListTasksByWorkflow(ctx, workflowID)
	if err != nil {
		return
	}

	for _, task := range tasks {
		if task.Status != storage.TaskStatusReady {
			continue
		}

		worker := c.findWorker(task.Handler)
		if worker == nil {
			continue
		}

		// Mark task as scheduled
		if err := c.store.UpdateTaskStatus(ctx, task.ID, storage.TaskStatusScheduled); err != nil {
			continue
		}

		c.saveEvent(ctx, workflowID, task.ID, storage.EventTaskScheduled, nil)

		// Dispatch to worker via gRPC
		go c.dispatchTask(context.Background(), worker, task)
	}
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

// dispatchTask sends a task to a worker for execution via the ExecuteTask RPC.
func (c *Coordinator) dispatchTask(ctx context.Context, worker *WorkerEntry, task *storage.Task) {
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

	// Update task to RUNNING
	if err := c.store.UpdateTaskStatus(ctx, task.ID, storage.TaskStatusRunning); err != nil {
		log.Printf("ERROR: update task %s to running: %v", task.ID, err)
		return
	}
	c.saveEvent(ctx, task.WorkflowID, task.ID, storage.EventTaskStarted, nil)

	resp, err := worker.Client.ExecuteTask(ctx, &forgev1.TaskRequest{
		TaskId:     task.ID,
		WorkflowId: task.WorkflowID,
		TaskName:   task.TaskName,
		Handler:    task.Handler,
		Input:      task.Input,
	})
	if err != nil {
		if failErr := c.OnTaskFailed(ctx, task.ID, err.Error()); failErr != nil {
			log.Printf("ERROR: handle task %s failure: %v", task.ID, failErr)
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

// saveEvent is a helper that persists an event without blocking on errors.
func (c *Coordinator) saveEvent(ctx context.Context, workflowID, taskID string, eventType storage.EventType, payload json.RawMessage) {
	c.seqMu.Lock()
	c.seqNum++
	seq := c.seqNum
	c.seqMu.Unlock()

	if err := c.store.SaveEvent(ctx, &storage.Event{
		WorkflowID:  workflowID,
		TaskID:      taskID,
		Type:        eventType,
		Payload:     payload,
		SequenceNum: seq,
	}); err != nil {
		log.Printf("ERROR: save event %s for workflow %s: %v", eventType, workflowID, err)
	}
}

// Storage returns the coordinator's storage backend (used by tests).
func (c *Coordinator) Storage() storage.Storage {
	return c.store
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
	default:
		return forgev1.TaskStatus_TASK_STATUS_UNSPECIFIED
	}
}
