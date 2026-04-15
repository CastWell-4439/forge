package coordinator

import (
	"context"
	"fmt"
	"log"
	"sync/atomic"

	"github.com/castwell/forge/internal/discovery"
	"github.com/castwell/forge/internal/storage"
)

// LeaderController manages leader election for a Coordinator instance.
// Only the leader Coordinator processes workflows; followers are hot standby.
type LeaderController struct {
	disco    discovery.Discovery
	nodeID   string
	isLeader atomic.Bool
	onGain   func() // called when this node becomes leader
	onLose   func() // called when this node loses leadership
}

// NewLeaderController creates a new LeaderController.
func NewLeaderController(disco discovery.Discovery, nodeID string) *LeaderController {
	return &LeaderController{
		disco:  disco,
		nodeID: nodeID,
	}
}

// OnGain sets the callback invoked when this node becomes the leader.
func (lc *LeaderController) OnGain(fn func()) {
	lc.onGain = fn
}

// OnLose sets the callback invoked when this node loses leadership.
func (lc *LeaderController) OnLose(fn func()) {
	lc.onLose = fn
}

// IsLeader reports whether this node is currently the leader.
func (lc *LeaderController) IsLeader() bool {
	return lc.isLeader.Load()
}

// Run starts the leader election loop. It blocks until ctx is cancelled.
// When leadership is gained, onGain is called. When lost, onLose is called
// and the node re-enters the election.
func (lc *LeaderController) Run(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			lc.isLeader.Store(false)
			return ctx.Err()
		default:
		}

		ch, err := lc.disco.LeaderElect(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			log.Printf("ERROR: leader election failed for %s: %v", lc.nodeID, err)
			continue
		}

		// Wait for election result.
		for val := range ch {
			if val {
				lc.isLeader.Store(true)
				log.Printf("INFO: node %s became leader", lc.nodeID)
				if lc.onGain != nil {
					lc.onGain()
				}
			} else {
				lc.isLeader.Store(false)
				log.Printf("INFO: node %s lost leadership", lc.nodeID)
				if lc.onLose != nil {
					lc.onLose()
				}
			}
		}

		// Channel closed — session expired or context cancelled. Re-enter election.
		lc.isLeader.Store(false)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		log.Printf("INFO: node %s re-entering leader election", lc.nodeID)
	}
}

// SetDiscovery sets the Discovery backend for the Coordinator and creates a LeaderController.
func (c *Coordinator) SetDiscovery(disco discovery.Discovery, nodeID string) {
	c.leader = NewLeaderController(disco, nodeID)
	c.disco = disco
}

// SetWorkerManager configures the WorkerManager for distributed worker tracking.
// It also wires up the dead-worker callback to reschedule running tasks.
func (c *Coordinator) SetWorkerManager(wm *WorkerManager) {
	c.workerMgr = wm
	wm.OnWorkerDead(func(workerID string) {
		c.rescheduleDeadWorkerTasks(workerID)
	})
}

// rescheduleDeadWorkerTasks finds all RUNNING tasks assigned to the dead worker
// and resets them to READY so they can be rescheduled to other workers.
func (c *Coordinator) rescheduleDeadWorkerTasks(workerID string) {
	ctx := context.Background()

	// Get all workflows that are currently running.
	workflows, err := c.store.ListWorkflows(ctx, storage.WorkflowStatusRunning, 1000, 0)
	if err != nil {
		log.Printf("ERROR: list running workflows for dead worker %s: %v", workerID, err)
		return
	}

	for _, wf := range workflows {
		tasks, err := c.store.ListTasksByWorkflow(ctx, wf.ID)
		if err != nil {
			log.Printf("ERROR: list tasks for workflow %s: %v", wf.ID, err)
			continue
		}

		for _, task := range tasks {
			if task.WorkerID != workerID {
				continue
			}
			if task.Status != storage.TaskStatusRunning && task.Status != storage.TaskStatusScheduled {
				continue
			}

			log.Printf("INFO: rescheduling task %s (workflow %s) from dead worker %s",
				task.ID, wf.ID, workerID)

			if err := c.store.UpdateTaskStatus(ctx, task.ID, storage.TaskStatusReady); err != nil {
				log.Printf("ERROR: reset task %s to READY: %v", task.ID, err)
				continue
			}

			c.saveEvent(ctx, wf.ID, task.ID, storage.EventTaskRetrying, nil)
		}

		// Kick off scheduling for this workflow.
		go c.scheduleReadyTasks(context.Background(), wf.ID)
	}
}

// StartLeaderElection starts leader election in the background.
// Only the leader will process new workflow scheduling.
func (c *Coordinator) StartLeaderElection(ctx context.Context) error {
	if c.leader == nil {
		return fmt.Errorf("discovery not configured; call SetDiscovery first")
	}

	c.leader.OnGain(func() {
		log.Printf("INFO: this coordinator is now the leader, will process workflows")
	})
	c.leader.OnLose(func() {
		log.Printf("INFO: this coordinator lost leadership, stopping workflow processing")
	})

	go c.leader.Run(ctx)
	return nil
}

// IsLeader reports whether this coordinator is the current leader.
func (c *Coordinator) IsLeader() bool {
	if c.leader == nil {
		return true // standalone mode: always leader
	}
	return c.leader.IsLeader()
}
