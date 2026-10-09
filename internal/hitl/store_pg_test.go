package hitl

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/castwell/forge/internal/storage"
)

// requirePGStore connects to the CI-provided PostgreSQL and makes sure the HITL
// table exists. Without FORGE_PG_DSN every test here skips — the same gate the
// other PostgreSQL-backed tests use.
//
// The table comes from the migrations directory, which is the schema a
// deployment gets: building it from a hand-written CREATE here would let the
// store pass against a table no deployment has.
func requirePGStore(t *testing.T) *PGStore {
	t.Helper()
	dsn := os.Getenv("FORGE_PG_DSN")
	if dsn == "" {
		t.Skip("FORGE_PG_DSN not set; runs in CI with the postgres service")
	}

	ctx := context.Background()
	store, err := storage.NewPGStorage(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	require.NoError(t, storage.MigrateUp(ctx, store, "../../deploy/migrations"))
	return NewPGStore(store.Pool())
}

// A request must survive the process that created it: that is the whole point of
// persisting it, and without it a restart leaves the waiting task with nothing
// left to answer.
func TestPGStoreRoundTrip(t *testing.T) {
	pg := requirePGStore(t)
	ctx := context.Background()
	id := "test-roundtrip-" + time.Now().Format("150405.000000000")

	req := &Request{
		ID:         id,
		WorkflowID: "wf-pg",
		TaskID:     "task-pg",
		Message:    "Deploy to production?",
		Options:    []string{"approve", "reject"},
		CreatedAt:  time.Now().UTC().Truncate(time.Microsecond),
		TimeoutAt:  time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond),
	}
	require.NoError(t, pg.Save(ctx, req))

	got, err := pg.Get(ctx, id)
	require.NoError(t, err)

	assert.Equal(t, id, got.ID)
	assert.Equal(t, "wf-pg", got.WorkflowID)
	assert.Equal(t, "task-pg", got.TaskID, "the task id is what ties an answer back to the waiting task")
	assert.Equal(t, "Deploy to production?", got.Message)
	assert.Equal(t, []string{"approve", "reject"}, got.Options)
	assert.Equal(t, StatusPending, got.Status, "a saved request starts pending")

	// A response persists and is readable back.
	got.Status = StatusResponded
	got.Response = &Response{Decision: "approve", Feedback: "LGTM"}
	require.NoError(t, pg.Update(ctx, got))

	again, err := pg.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, StatusResponded, again.Status)
	require.NotNil(t, again.Response)
	assert.Equal(t, "approve", again.Response.Decision)
	assert.Equal(t, "LGTM", again.Response.Feedback)
}

// Only pending requests are listed as outstanding; an answered one is not work
// anyone still has to do.
func TestPGStoreListPendingExcludesAnswered(t *testing.T) {
	pg := requirePGStore(t)
	ctx := context.Background()
	suffix := time.Now().Format("150405.000000000")

	pending := &Request{
		ID: "test-pending-" + suffix, WorkflowID: "wf-a", TaskID: "t-a",
		Message: "waiting", Options: []string{},
		CreatedAt: time.Now().UTC(), TimeoutAt: time.Now().UTC().Add(time.Hour),
	}
	answered := &Request{
		ID: "test-answered-" + suffix, WorkflowID: "wf-b", TaskID: "t-b",
		Message: "done", Options: []string{},
		CreatedAt: time.Now().UTC(), TimeoutAt: time.Now().UTC().Add(time.Hour),
	}
	require.NoError(t, pg.Save(ctx, pending))
	require.NoError(t, pg.Save(ctx, answered))

	answered.Status = StatusResponded
	answered.Response = &Response{Decision: "approve"}
	require.NoError(t, pg.Update(ctx, answered))

	listed, err := pg.ListPending(ctx)
	require.NoError(t, err)

	ids := make(map[string]bool, len(listed))
	for _, r := range listed {
		ids[r.ID] = true
	}
	assert.True(t, ids[pending.ID], "an unanswered request is outstanding")
	assert.False(t, ids[answered.ID], "an answered one is not")
}

// An expired request is found by reading the table, which is what lets a process
// that did NOT create the request still time it out.
func TestPGStoreListExpired(t *testing.T) {
	pg := requirePGStore(t)
	ctx := context.Background()
	suffix := time.Now().Format("150405.000000000")

	expired := &Request{
		ID: "test-expired-" + suffix, WorkflowID: "wf-e", TaskID: "t-e",
		Message: "old", Options: []string{},
		CreatedAt: time.Now().UTC().Add(-2 * time.Hour),
		TimeoutAt: time.Now().UTC().Add(-time.Hour),
	}
	fresh := &Request{
		ID: "test-fresh-" + suffix, WorkflowID: "wf-f", TaskID: "t-f",
		Message: "new", Options: []string{},
		CreatedAt: time.Now().UTC(), TimeoutAt: time.Now().UTC().Add(time.Hour),
	}
	require.NoError(t, pg.Save(ctx, expired))
	require.NoError(t, pg.Save(ctx, fresh))

	listed, err := pg.ListExpired(ctx, time.Now().UTC())
	require.NoError(t, err)

	ids := make(map[string]bool, len(listed))
	for _, r := range listed {
		ids[r.ID] = true
	}
	assert.True(t, ids[expired.ID], "a request past its deadline is expired")
	assert.False(t, ids[fresh.ID], "one still inside its window is not")
}

// A Manager sweeping a store it did not fill finds the requests anyway. This is
// the two-process case: the worker files, the coordinator sweeps.
func TestManagerSweepsStoreItDidNotFill(t *testing.T) {
	pg := requirePGStore(t)
	ctx := context.Background()
	suffix := time.Now().Format("150405.000000000")

	// Filed "by another process": written straight to the store, never to a
	// manager's memory.
	id := "test-crossproc-" + suffix
	require.NoError(t, pg.Save(ctx, &Request{
		ID: id, WorkflowID: "wf-x", TaskID: "t-x", Message: "unanswered",
		Options:   []string{},
		CreatedAt: time.Now().UTC().Add(-2 * time.Hour),
		TimeoutAt: time.Now().UTC().Add(-time.Hour),
	}))

	// A different manager, with its own empty memory, sharing the store.
	mgr := NewManager(ManagerConfig{Store: pg, Timeout: time.Hour})
	timedOut := mgr.CheckTimeouts(ctx)

	var found *Request
	for _, r := range timedOut {
		if r.ID == id {
			found = r
		}
	}
	require.NotNil(t, found, "the sweep must see requests filed by another process")
	assert.Equal(t, "t-x", found.TaskID, "and must carry the task id so it can be released")

	// The timeout is persisted, not only reported.
	after, err := pg.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, StatusTimeout, after.Status)
}

// Updating a request that is not there is an error, not a silent no-op: losing
// the write would leave a request pending forever while its task sits parked.
func TestPGStoreUpdateMissingIsAnError(t *testing.T) {
	pg := requirePGStore(t)
	err := pg.Update(context.Background(), &Request{
		ID:     "test-missing-" + time.Now().Format("150405.000000000"),
		Status: StatusResponded,
	})
	require.Error(t, err, "a lost update must be reported")
}

// Saving the same ID twice converges instead of failing on the primary key, so a
// retry after a partial failure is safe.
func TestPGStoreSaveIsUpsert(t *testing.T) {
	pg := requirePGStore(t)
	ctx := context.Background()
	id := "test-upsert-" + time.Now().Format("150405.000000000")

	req := &Request{
		ID: id, WorkflowID: "wf-u", TaskID: "t-u", Message: "first",
		Options:   []string{"a"},
		CreatedAt: time.Now().UTC().Add(-time.Hour),
		TimeoutAt: time.Now().UTC().Add(-30 * time.Minute),
	}
	require.NoError(t, pg.Save(ctx, req))

	// Same id, moved deadline: a retry that recomputed the timeout.
	req.TimeoutAt = time.Now().UTC().Add(2 * time.Hour)
	require.NoError(t, pg.Save(ctx, req))

	got, err := pg.Get(ctx, id)
	require.NoError(t, err)
	assert.WithinDuration(t, req.TimeoutAt, got.TimeoutAt, time.Second,
		"the re-save must update the deadline rather than be rejected")
}

// A pool that is not there is a programming error, guarded so the store does not
// panic somewhere less obvious.
func TestNewPGStoreAcceptsPool(t *testing.T) {
	var pool *pgxpool.Pool
	store := NewPGStore(pool)
	require.NotNil(t, store)
}
