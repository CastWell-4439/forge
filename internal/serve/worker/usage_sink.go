package worker

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/castwell/forge/internal/agent/core"
	"github.com/castwell/forge/internal/forgex/storage"
)

// The agent plane's half of the citation contract.
//
// It implements core.ObservationSink: the harness hands over the memory ids a
// run verifiably relied on, and this writes them into the control plane's index
// as usage observations.
//
// What it deliberately does NOT do: decide anything. It appends "this run used
// this memory" and stops. Archiving, recovery and deletion are the control
// plane's business (cmd/forgex memory), because a run cannot see the aggregate
// and must not be able to act on its own partial view.

// usageRecorder writes verified citations to the cross-run index.
type usageRecorder struct {
	index *storage.SQLiteIndex
}

// RecordUsage implements core.ObservationSink.
//
// A failure is logged and swallowed. The observation is bookkeeping for a later
// offline decision; a run that already produced its answer must not fail
// because the bookkeeping could not be written. The alternative — surfacing the
// error — would make an unwritable index a run-breaking condition, which is a
// far worse failure than a missing observation.
func (r *usageRecorder) RecordUsage(ctx context.Context, runID string, entryIDs []string) error {
	if r == nil || r.index == nil || runID == "" || len(entryIDs) == 0 {
		return nil
	}
	now := time.Now().UTC()
	for _, id := range entryIDs {
		if id == "" {
			continue
		}
		// Used=true together with UsageKnown=true: this is the ONE combination
		// the contract can establish, because the id was checked against what
		// the run was actually shown before it reached here. See citation.go
		// for why the opposite combination is never produced.
		err := r.index.RecordObservation(ctx, storage.MemoryObservation{
			RunID:      runID,
			EntryID:    id,
			Kind:       storage.ObservationKindMemory,
			Used:       true,
			UsageKnown: true,
			At:         now,
		})
		if err != nil {
			return fmt.Errorf("record usage for %s: %w", id, err)
		}
	}
	return nil
}

// buildUsageSink opens the index and returns the sink, or nil when the channel
// is unavailable.
//
// It shares the index path with the lessons channel (FORGEX_INDEX_DB, default
// <root>/index.db) because the observations live in the same control-plane
// database: one file, one owner, one set of lifecycle commands.
//
// A nil result means citations are not recorded. That is the historical
// behaviour and it is not an error: the run loses nothing, and the lifecycle
// simply keeps deciding on the observations it has.
func buildUsageSink() core.ObservationSink {
	path := indexDBPath()
	index, err := storage.OpenSQLiteIndex(path)
	if err != nil {
		log.Printf("INFO: memory usage observations are off (%s is unreadable: %v); "+
			"runs will not record which memories they used", path, err)
		return nil
	}
	log.Printf("INFO: memory usage observations enabled (index=%s)", path)
	return &usageRecorder{index: index}
}
