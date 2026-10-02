package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"github.com/castwell/forge/internal/agent/checkpoint"
	"github.com/castwell/forge/internal/agent/harness"
	"github.com/castwell/forge/internal/forgex/model"
	"github.com/castwell/forge/internal/forgex/storage"
)

// Environment variables that configure the run journal.
//
// FORGE_JOURNAL=off disables journaling entirely (the loop treats a nil
// journal as "no journaling"). The roots default to the same trees the rest
// of the assembly already uses: ForgeX runs for the primary copy, the
// checkpoint store for the fallback.
const (
	envJournalEnabled = "FORGE_JOURNAL"
	envRunsRoot       = "FORGE_RUNS_ROOT"
	envCheckpointRoot = "FORGE_CHECKPOINT_DIR"
	defaultRunsRoot   = ".forgex"
)

// spanProjector sits between the durable journal and its primary file target,
// so every event that becomes durable — including buffered ones flushed after
// a write failure — passes through here exactly once. That ordering is what
// keeps spans from drifting from what the journal actually holds.
//
// Spans are observability, not recovery state: a projection failure is logged
// and never fails the append that already succeeded.
type spanProjector struct {
	inner harness.Journal
	store *storage.FileStore

	mu          sync.Mutex
	stepStarted map[string]time.Time // run:step  -> first step_started seen
	toolStarted map[string]time.Time // tool id   -> tool_started seen
}

func newSpanProjector(inner harness.Journal, store *storage.FileStore) *spanProjector {
	return &spanProjector{
		inner:       inner,
		store:       store,
		stepStarted: map[string]time.Time{},
		toolStarted: map[string]time.Time{},
	}
}

// AppendEvent writes the journal first, then projects observability spans.
func (p *spanProjector) AppendEvent(ctx context.Context, ev harness.RunEvent) error {
	if err := p.inner.AppendEvent(ctx, ev); err != nil {
		return err
	}
	if err := p.project(ctx, ev); err != nil {
		log.Printf("[journal] span projection for %s seq %d failed: %v", ev.Type, ev.Seq, err)
	}
	return nil
}

// ReadEvents passes reads through; the projector never changes the stream.
func (p *spanProjector) ReadEvents(ctx context.Context, sessionID string) ([]harness.RunEvent, error) {
	return p.inner.ReadEvents(ctx, sessionID)
}

// stepSpanID is the parent id every tool span of that step points at.
func stepSpanID(runID string, step int) string {
	return fmt.Sprintf("%s-step-%d", runID, step)
}

func (p *spanProjector) project(ctx context.Context, ev harness.RunEvent) error {
	switch ev.Type {
	case harness.EventStepStarted:
		key := stepSpanID(ev.RunID, ev.Step)
		p.mu.Lock()
		if _, ok := p.stepStarted[key]; !ok {
			p.stepStarted[key] = ev.TS
		}
		p.mu.Unlock()
		return nil

	case harness.EventToolStarted:
		p.mu.Lock()
		p.toolStarted[asStringKey(ev.Data["id"])] = ev.TS
		p.mu.Unlock()
		return nil

	case harness.EventToolCompleted:
		id := asStringKey(ev.Data["id"])
		p.mu.Lock()
		started, ok := p.toolStarted[id]
		delete(p.toolStarted, id)
		p.mu.Unlock()
		if !ok {
			started = ev.TS
		}
		status := "ok"
		if errText, _ := ev.Data["error"].(string); errText != "" {
			status = "error"
		}
		return p.store.AppendSpan(ctx, model.Span{
			ID:        id,
			RunID:     ev.RunID,
			ParentID:  stepSpanID(ev.RunID, ev.Step),
			Name:      ev.Tool,
			StartedAt: started,
			EndedAt:   ev.TS,
			Status:    status,
			Attrs:     map[string]any{"step": ev.Step, "seq": ev.Seq},
		})

	case harness.EventStepCompleted:
		key := stepSpanID(ev.RunID, ev.Step)
		p.mu.Lock()
		started, ok := p.stepStarted[key]
		delete(p.stepStarted, key)
		p.mu.Unlock()
		if !ok {
			started = ev.TS
		}
		return p.store.AppendSpan(ctx, model.Span{
			ID:        key,
			RunID:     ev.RunID,
			Name:      fmt.Sprintf("step %d", ev.Step),
			StartedAt: started,
			EndedAt:   ev.TS,
			Status:    "ok",
			Attrs:     map[string]any{"step": ev.Step, "seq": ev.Seq},
		})
	}
	return nil
}

// asStringKey reads a string Data field; a missing id would make every tool
// span collide, so an empty one is reported by the span's caller-visible
// attributes instead of silently sharing a key.
func asStringKey(v any) string {
	s, _ := v.(string)
	if s == "" {
		return "unknown"
	}
	return s
}

// buildRunJournal assembles the production journal: primary in the ForgeX
// run tree (journal.jsonl, projected to spans.jsonl) with the checkpoint tree
// as the second-tier target. It returns nil when journaling is disabled, and
// the loop treats nil as off.
func buildRunJournal() harness.Journal {
	if os.Getenv(envJournalEnabled) == "off" {
		log.Printf("INFO: run journal disabled (%s=off)", envJournalEnabled)
		return nil
	}
	runsRoot := envOrDefault(envRunsRoot, defaultRunsRoot)
	cpRoot := envOrDefault(envCheckpointRoot, checkpoint.DefaultFileStoreRoot)

	layout := storage.NewLayout(runsRoot)
	primary := harness.NewFileJournal(func(sessionID string) string {
		return layout.JournalFile(sessionID)
	})
	projected := newSpanProjector(primary, storage.NewFileStore(runsRoot))
	fallback := harness.NewFileJournal(func(sessionID string) string {
		return checkpoint.JournalFallbackPath(cpRoot, sessionID)
	})

	log.Printf("INFO: run journal enabled (runs=%s fallback=%s)", runsRoot, cpRoot)
	return harness.NewDurableJournal(projected, fallback, harness.DurableJournalOptions{})
}
