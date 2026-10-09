package coordinator

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/castwell/forge/internal/coordinator"
	"github.com/castwell/forge/internal/hitl"
	"github.com/castwell/forge/internal/storage"
)

// hitlSweepInterval is how often pending human requests are checked for expiry.
//
// It matches the timeout manager's scan interval: both exist to notice that
// something has been sitting too long, and there is no reason for one to notice
// faster than the other.
const hitlSweepInterval = 5 * time.Second

// setupHITL builds the HITL manager the coordinator side needs and returns the
// sweep it should run.
//
// The coordinator does NOT create human requests — the worker's `hitl` handler
// does that, in its own process. What the coordinator supplies is the other three
// halves:
//
//	the store    — so a request survives a restart and is visible to both sides
//	the resolver — so answering one releases the task it was blocking
//	the sweep    — so a request nobody answers times out instead of blocking forever
//
// All three degrade rather than fail: with no PostgreSQL there is no shared
// store, so the manager keeps its own memory and the endpoint still serves
// requests filed in this process.
func setupHITL(coord *coordinator.Coordinator, store storage.Storage, mux *http.ServeMux) func(context.Context) {
	cfg := hitl.ManagerConfig{Timeout: 24 * time.Hour}

	if pg, ok := store.(*storage.PGStorage); ok {
		cfg.Store = hitl.NewPGStore(pg.Pool())
		log.Printf("INFO: hitl: requests are persisted (shared with workers via hitl_requests)")
	} else {
		// Say it plainly: without a shared store, a request filed by another
		// process is invisible here, and an operator should know why the pending
		// queue looks empty.
		log.Printf("INFO: hitl: no PostgreSQL, so requests are in-memory only; " +
			"requests filed by a worker in another process will not be visible")
	}

	manager := hitl.NewManager(cfg)

	handler := hitl.NewHandler(manager, nil)
	handler.SetResolveFunc(hitlResolver(coord))
	handler.RegisterRoutes(mux)
	log.Printf("INFO: hitl: endpoints mounted at /api/hitl/pending and /api/hitl/respond")

	// The sweep covers requests this process never filed.
	sweep := func(ctx context.Context) {
		ticker := time.NewTicker(hitlSweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				for _, req := range manager.CheckTimeouts(ctx) {
					releaseTimedOutRequest(ctx, coord, req)
				}
			}
		}
	}
	return sweep
}

// hitlResolver turns a recorded decision into a task state change.
//
// This is the bridge between the two halves of the system: the HITL package owns
// the question and the coordinator owns the task, and neither may import the
// other. The assembly layer is the only place that knows both.
//
// Only an approval resumes; anything else rejects, because a task held for
// approval must not proceed on an answer that is not an approval. The decision
// vocabulary is open (a workflow may declare its own options), so the test is
// "is this the approving option" rather than an enumeration of refusals — erring
// toward stopping, which is the safe direction for an approval gate.
func hitlResolver(coord *coordinator.Coordinator) hitl.ResolveFunc {
	return func(ctx context.Context, req *hitl.Request, resp *hitl.Response) error {
		if req.TaskID == "" {
			// Nothing to release. Not an error the reviewer caused or can fix,
			// but it has to be visible: the decision is recorded and no task moved.
			return errNoTaskOnRequest
		}

		if approves(resp.Decision, req.Options) {
			return coord.ResumePausedTask(ctx, req.TaskID, "human approved via /api/hitl/respond")
		}
		return coord.RejectPausedTask(ctx, req.TaskID,
			"human rejected via /api/hitl/respond: "+resp.Decision)
	}
}

// releaseTimedOutRequest fails the task whose request expired.
//
// A timed-out request must land somewhere. Leaving the task parked would mean a
// workflow waiting on an approval that can no longer arrive, with nothing in the
// system willing to say so.
func releaseTimedOutRequest(ctx context.Context, coord *coordinator.Coordinator, req *hitl.Request) {
	if req.TaskID == "" {
		return
	}
	log.Printf("WARN: hitl: request %s timed out; failing its task %s", req.ID, req.TaskID)
	if err := coord.FailPausedTaskOnTimeout(ctx, req.TaskID); err != nil {
		// The task may have moved on (answered at the last moment, or already
		// failed). Recording it is enough — the sweep will not see the request
		// again because its status is no longer pending.
		log.Printf("WARN: hitl: could not fail task %s after request timeout: %v", req.TaskID, err)
	}
}

// approves reports whether a decision is an approval.
//
// The workflow declares the options, so the set is open. Rather than enumerate
// refusals — which would silently resume a task on any word nobody thought of —
// this recognises the approving words and treats everything else as not-approved.
func approves(decision string, options []string) bool {
	switch decision {
	case "approve", "approved", "ack", "yes", "ok", "accept":
		return true
	}
	// A workflow that declares its own single approving option is honoured: the
	// first option is the affirmative one by construction (the worker builds
	// ["approve","reject"], and a custom list reads the same way).
	if len(options) > 0 && decision == options[0] {
		return true
	}
	return false
}

// errNoTaskOnRequest is returned when a request names no task to release.
var errNoTaskOnRequest = errHITLNoTask{}

type errHITLNoTask struct{}

func (errHITLNoTask) Error() string {
	return "the request names no task, so nothing could be resumed"
}
