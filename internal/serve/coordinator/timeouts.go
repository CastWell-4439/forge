package coordinator

import (
	"context"
	"log"

	"github.com/castwell/forge/internal/coordinator"
)

// Task and workflow timeouts.
//
// The TimeoutManager has existed since the scheduler round, complete with a
// scan loop, a FailTask write and an OnTaskTimeout hook — and no assembly.
// The practical effect was that no task has ever been failed for exceeding
// its deadline: a stuck task stayed RUNNING forever, which is precisely the
// failure mode the Kueue write-back design relies on this manager to catch.
//
// Wiring it is therefore two fixes in one: ordinary tasks gain the timeout
// they were always documented to have, and GPU tasks gain the backstop their
// deadline (written into TimeoutAt at submission) was waiting for.
//
// What is deliberately NOT wired here is retry-on-timeout. The manager's hook
// doc says "trigger retry or permanent failure"; which one belongs to the
// retry policy and the task state machine, and deciding it as a side effect of
// plumbing would change scheduling semantics silently. Today a timed-out task
// fails and its workflow's own failure handling takes over — the same
// treatment as any other task failure.
func startTimeoutManager(appCtx context.Context, coord *coordinator.Coordinator) {
	mgr := coordinator.NewTimeoutManager(coord.Store())
	go mgr.Run(appCtx)
	log.Printf("INFO: timeout manager started (task/workflow deadlines are enforced)")
}
