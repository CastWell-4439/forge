package coordinator

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	forgev1 "github.com/castwell/forge/api/proto/gen"
	"github.com/castwell/forge/internal/storage"
)

func cronTestSetup(t *testing.T) (*Coordinator, *CronScheduler) {
	t.Helper()
	store, err := storage.NewBoltStorage(filepath.Join(t.TempDir(), "cron.db"))
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	coord := NewCoordinator(store)
	sched := NewCronScheduler(coord, nil)
	return coord, sched
}

// MaxConcurrent is optional and left as declared. Forcing it to 1 made it a
// field that looked like a limit while nothing read it, so every deployment got
// the same answer whatever it wrote — a setting that could not vary.
func TestMaxConcurrentIsLeftAsDeclared(t *testing.T) {
	_, sched := cronTestSetup(t)

	// Unset (the zero value every existing YAML has) stays unset.
	unset := &CronTrigger{ID: "t-unset", WorkflowName: "wf", CronExpr: "* * * * *"}
	require.NoError(t, sched.AddTrigger(unset))
	assert.Zero(t, unset.MaxConcurrent, "no declaration means no limit, not a silent cap of 1")

	// A declared limit is kept.
	declared := &CronTrigger{ID: "t-declared", WorkflowName: "wf", CronExpr: "* * * * *", MaxConcurrent: 3}
	require.NoError(t, sched.AddTrigger(declared))
	assert.Equal(t, 3, declared.MaxConcurrent, "a declared limit must survive registration")
}

// A trigger with no declared limit fires even while instances are running: the
// historical behaviour, which must not change for workflows that never asked for
// a limit.
func TestUnlimitedTriggerKeepsFiring(t *testing.T) {
	coord, sched := cronTestSetup(t)

	// Two instances of the same workflow already running.
	for i := 0; i < 2; i++ {
		_, err := coord.SubmitWorkflow(context.Background(), &forgev1.SubmitWorkflowRequest{
			DagYaml: "name: wf_unlimited\ntasks:\n  a:\n    handler: nobody\n",
		})
		require.NoError(t, err)
	}

	fired := 0
	trigger := &CronTrigger{
		ID: "t-unlimited", WorkflowName: "wf_unlimited", CronExpr: "* * * * *",
		SubmitFn: func(context.Context) error { fired++; return nil },
	}
	require.NoError(t, sched.AddTrigger(trigger))

	sched.fire(trigger, time.Now())
	assert.Equal(t, 1, fired, "without a declared limit the trigger fires as before")
}

// A declared limit holds the trigger back while enough instances are running.
func TestDeclaredLimitSkipsWhenSaturated(t *testing.T) {
	coord, sched := cronTestSetup(t)

	for i := 0; i < 2; i++ {
		_, err := coord.SubmitWorkflow(context.Background(), &forgev1.SubmitWorkflowRequest{
			DagYaml: "name: wf_limited\ntasks:\n  a:\n    handler: nobody\n",
		})
		require.NoError(t, err)
	}

	fired := 0
	trigger := &CronTrigger{
		ID: "t-limited", WorkflowName: "wf_limited", CronExpr: "* * * * *", MaxConcurrent: 2,
		SubmitFn: func(context.Context) error { fired++; return nil },
	}
	require.NoError(t, sched.AddTrigger(trigger))

	sched.fire(trigger, time.Now())
	assert.Zero(t, fired, "two running instances already reach the declared limit of 2")
}

// Below the limit the trigger still fires: a limit is a ceiling, not a switch.
func TestDeclaredLimitAllowsBelowTheCeiling(t *testing.T) {
	coord, sched := cronTestSetup(t)

	_, err := coord.SubmitWorkflow(context.Background(), &forgev1.SubmitWorkflowRequest{
		DagYaml: "name: wf_below\ntasks:\n  a:\n    handler: nobody\n",
	})
	require.NoError(t, err)

	fired := 0
	trigger := &CronTrigger{
		ID: "t-below", WorkflowName: "wf_below", CronExpr: "* * * * *", MaxConcurrent: 3,
		SubmitFn: func(context.Context) error { fired++; return nil },
	}
	require.NoError(t, sched.AddTrigger(trigger))

	sched.fire(trigger, time.Now())
	assert.Equal(t, 1, fired, "one instance is below a limit of three")
}

// The count is per workflow: another workflow's instances must not consume this
// one's budget.
func TestLimitCountsOnlyItsOwnWorkflow(t *testing.T) {
	coord, sched := cronTestSetup(t)

	// Two instances of a DIFFERENT workflow.
	for i := 0; i < 2; i++ {
		_, err := coord.SubmitWorkflow(context.Background(), &forgev1.SubmitWorkflowRequest{
			DagYaml: "name: wf_other\ntasks:\n  a:\n    handler: nobody\n",
		})
		require.NoError(t, err)
	}

	fired := 0
	trigger := &CronTrigger{
		ID: "t-mine", WorkflowName: "wf_mine", CronExpr: "* * * * *", MaxConcurrent: 1,
		SubmitFn: func(context.Context) error { fired++; return nil },
	}
	require.NoError(t, sched.AddTrigger(trigger))

	sched.fire(trigger, time.Now())
	assert.Equal(t, 1, fired, "another workflow's instances are not this one's")
}

// A paused workflow still occupies its slot. A workflow waiting on an approval
// is in flight, and counting only RUNNING would let a cron pile instances up
// behind an unanswered question.
func TestPausedWorkflowCountsTowardsTheLimit(t *testing.T) {
	coord, sched := cronTestSetup(t)

	resp, err := coord.SubmitWorkflow(context.Background(), &forgev1.SubmitWorkflowRequest{
		DagYaml: "name: wf_paused\ntasks:\n  a:\n    handler: nobody\n",
	})
	require.NoError(t, err)

	// Park it as an approval would.
	require.NoError(t, coord.store.UpdateWorkflowStatus(context.Background(),
		resp.GetWorkflowId(), storage.WorkflowStatusPaused))

	fired := 0
	trigger := &CronTrigger{
		ID: "t-paused", WorkflowName: "wf_paused", CronExpr: "* * * * *", MaxConcurrent: 1,
		SubmitFn: func(context.Context) error { fired++; return nil },
	}
	require.NoError(t, sched.AddTrigger(trigger))

	sched.fire(trigger, time.Now())
	assert.Zero(t, fired, "a workflow waiting on a human is still in flight")
}

// A skipped fire still records that it was evaluated, so the trigger is not
// immediately due again on the next tick. Without that, a trigger held back by
// its limit would be re-examined every second, logging a skip each time.
//
// The assertion is on LastFireAt rather than NextFireAt: for a */5 expression
// evaluated twice inside the same minute the next fire time is legitimately the
// same instant, so "next changed" would be a false test.
func TestSkippedFireIsRecorded(t *testing.T) {
	coord, sched := cronTestSetup(t)

	_, err := coord.SubmitWorkflow(context.Background(), &forgev1.SubmitWorkflowRequest{
		DagYaml: "name: wf_advance\ntasks:\n  a:\n    handler: nobody\n",
	})
	require.NoError(t, err)

	trigger := &CronTrigger{
		ID: "t-advance", WorkflowName: "wf_advance", CronExpr: "*/5 * * * *", MaxConcurrent: 1,
		SubmitFn: func(context.Context) error { return nil },
	}
	require.NoError(t, sched.AddTrigger(trigger))
	require.Nil(t, trigger.LastFireAt, "nothing has fired yet")

	now := time.Now()
	sched.fire(trigger, now)

	require.NotNil(t, trigger.LastFireAt,
		"a skipped fire must still record that it was considered")
	assert.WithinDuration(t, now, *trigger.LastFireAt, time.Second)
	require.NotNil(t, trigger.NextFireAt, "and must leave a next fire time behind")
}
