package coordinator

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync/atomic"
	"time"

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

// OrphanReclaimDelay is how long the serve layer waits after startup before the
// first orphan pass.
//
// It is a settling window, not a timeout: in-process workers register during
// startup, and a pass that ran before they did would see an empty registry and
// conclude that every RUNNING task is an orphan. Ten seconds is longer than
// registration takes and short enough that a stranded task is noticed promptly.
//
// It is exported because the serve layer schedules the startup pass and must
// wait the same amount.
//
// It lives here rather than in the serve layer so the two places that reason
// about "who is running this" share one definition of how long to wait before
// answering.
const OrphanReclaimDelay = 10 * time.Second

// rescheduleDeadWorkerTasks finds all RUNNING tasks assigned to the dead worker
// and resets them to READY so they can be rescheduled to other workers.
func (c *Coordinator) rescheduleDeadWorkerTasks(workerID string) {
	c.reclaimUnassignedTasks(context.Background(), reclaimReason{
		// Only this worker's tasks are in scope.
		onlyWorker: workerID,
		reason:     fmt.Sprintf("worker %s is no longer alive", workerID),
	})
}

// reclaimReason describes why a reclaim pass is running.
//
// The reason travels into the event log, because "this task was re-dispatched"
// is only half an answer — an operator needs to know WHY, and the three causes
// (a worker died, this process restarted, this process became leader) are
// indistinguishable from the task's own history afterwards.
type reclaimReason struct {
	// onlyWorker limits the pass to one worker's tasks. Empty means every task
	// whose worker this coordinator cannot account for.
	onlyWorker string
	// reason is written to the event payload and the log or workflow history.
	reason string
	// trustedWorkers, when non-nil, is the set of worker ids this pass considers
	// alive. Nil means "ask the coordinator", which is the normal path; a test
	// injects one to describe a state the live registry would not reach.
	trustedWorkers map[string]bool
}

// reclaimUnassignedTasks resets tasks that no known worker is running back to
// READY, and kicks off scheduling for their workflows.
//
// It is the single implementation behind two triggers that were previously one:
//
//	worker failure detector   a specific worker stopped answering
//	startup / gaining office  this process cannot account for ANY worker
//
// The second trigger did not exist, and its absence was a real defect rather
// than a missing feature. A coordinator restart empties its in-memory worker
// registry, so every task that was RUNNING at that moment became a row nobody
// owned — and nothing ever looked for it. The workflow sat in RUNNING forever,
// displaying a state indistinguishable from healthy progress. The same gap
// opened on leader failover: onGain was wired up and did nothing but log.
//
// The judgement that decides what to reclaim is deliberately the CONSERVATIVE
// one: a task is reclaimed only when this coordinator cannot name the worker
// running it. The alternative — resetting every RUNNING task on startup — reads
// as more thorough and is not: in a deployment with more than one coordinator it
// would seize tasks that a peer is actively executing, and re-running a task
// whose first execution already had a side effect is not recoverable. The
// failure of the conservative rule is a task that stays stuck (visible, and
// fixable by the next pass or by hand); the failure of the aggressive rule is
// duplicated work with real consequences. This project has chosen the first
// direction four times already, and the reason is the same each time.
func (c *Coordinator) reclaimUnassignedTasks(ctx context.Context, why reclaimReason) {
	workflows, err := c.store.ListWorkflows(ctx, storage.WorkflowStatusRunning, 1000, 0)
	if err != nil {
		log.Printf("ERROR: list running workflows for reclaim (%s): %v", why.reason, err)
		return
	}

	// Resolved once per pass rather than once per task: the answer changes only
	// if a worker registers while this runs, and re-reading it per task would let
	// a task registered midway be judged by a different rule than its siblings.
	known := why.trustedWorkers
	if known == nil {
		known = c.knownWorkerIDs()
	}

	reclaimed := 0
	for _, wf := range workflows {
		tasks, err := c.store.ListTasksByWorkflow(ctx, wf.ID)
		if err != nil {
			log.Printf("ERROR: list tasks for workflow %s: %v", wf.ID, err)
			continue
		}

		touched := false
		for _, task := range tasks {
			if task.Status != storage.TaskStatusRunning && task.Status != storage.TaskStatusScheduled {
				continue
			}
			if why.onlyWorker != "" && task.WorkerID != why.onlyWorker {
				continue
			}
			// The conservative predicate. A task with no worker recorded is also
			// reclaimed: "nobody was ever assigned" is not a reason to leave it
			// stuck, and it cannot be running anywhere.
			if why.onlyWorker == "" && task.WorkerID != "" && known[task.WorkerID] {
				continue
			}

			log.Printf("INFO: reclaiming task %s (workflow %s, was %s on worker %q): %s",
				task.ID, wf.ID, task.Status, task.WorkerID, why.reason)

			if err := c.store.UpdateTaskStatus(ctx, task.ID, storage.TaskStatusReady); err != nil {
				log.Printf("ERROR: reset task %s to READY: %v", task.ID, err)
				continue
			}

			// The reason is recorded, so "why did this run twice" has an answer
			// that does not depend on correlating timestamps with logs.
			payload, _ := json.Marshal(map[string]string{
				"reason": why.reason,
				"worker": task.WorkerID,
			})
			c.saveEvent(ctx, wf.ID, task.ID, storage.EventTaskRetrying, payload)
			reclaimed++
			touched = true
		}

		if touched {
			go c.scheduleReadyTasks(context.Background(), wf.ID)
		}
	}

	if reclaimed > 0 {
		log.Printf("INFO: reclaimed %d task(s): %s", reclaimed, why.reason)
	}
}

// knownWorkerIDs is the set of workers this coordinator can currently account
// for, from BOTH registries.
//
// There are two — the direct map and the discovery-backed manager — and a
// deployment uses one or the other. Asking only one of them would report every
// worker as unknown in the other deployment shape, and the reclaim pass would
// then reset tasks that are actively running. That is the aggressive failure
// this function exists to avoid, arrived at by accident.
func (c *Coordinator) knownWorkerIDs() map[string]bool {
	out := map[string]bool{}

	c.mu.RLock()
	for id := range c.workers {
		out[id] = true
	}
	c.mu.RUnlock()

	if c.workerMgr != nil {
		for _, w := range c.workerMgr.ActiveWorkers() {
			if w == nil || w.Registration.ID == "" {
				continue
			}
			out[w.Registration.ID] = true
		}
	}
	return out
}

// StartLeaderElection starts leader election in the background.
// Only the leader will process new workflow scheduling.
func (c *Coordinator) StartLeaderElection(ctx context.Context) error {
	if c.leader == nil {
		return fmt.Errorf("discovery not configured; call SetDiscovery first")
	}

	c.leader.OnGain(func() {
		log.Printf("INFO: this coordinator is now the leader, will process workflows")

		// Becoming leader is the moment this process inherits responsibility for
		// work its predecessor was running.
		//
		// The predecessor's in-memory worker registry died with it, but the task
		// rows did not: they still say RUNNING, and on the new leader's registry
		// their worker is unknown. That is exactly the condition the conservative
		// predicate reclaims, so failover recovers in-flight work instead of
		// leaving it stranded.
		//
		// It runs in the background because gaining office must not block on a
		// database scan: a slow store would otherwise look like an election that
		// never completed.
		go c.reclaimUnassignedTasks(context.Background(), reclaimReason{
			reason: "this coordinator became leader and cannot account for the task's worker",
		})
	})
	c.leader.OnLose(func() {
		log.Printf("INFO: this coordinator lost leadership, stopping workflow processing")
	})

	go c.leader.Run(ctx)
	return nil
}

// ReclaimOrphanedTasks recovers tasks left behind by a previous process.
//
// It is the startup path, for deployments that have no leader election at all —
// which is the common one, and the one where the defect was easiest to hit: a
// single `forge standalone` restart used to strand every in-flight task with
// nothing to notice.
//
// It is exported so the serve layer can call it once the coordinator is fully
// wired, after workers have had a chance to register. Calling it before the
// registries are populated would be harmless (everything looks unaccounted for)
// but wasteful, and calling it after is also harmless — the pass is idempotent,
// because a task it already reset is no longer RUNNING.
func (c *Coordinator) ReclaimOrphanedTasks(ctx context.Context) {
	c.reclaimUnassignedTasks(ctx, reclaimReason{
		reason: "this coordinator restarted and cannot account for the task's worker",
	})
}

// IsLeader reports whether this coordinator is the current leader.
func (c *Coordinator) IsLeader() bool {
	if c.leader == nil {
		return true // standalone mode: always leader
	}
	return c.leader.IsLeader()
}
