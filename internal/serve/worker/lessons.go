package worker

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	agentcore "github.com/castwell/forge/internal/agent"
	"github.com/castwell/forge/internal/agent/core"
	"github.com/castwell/forge/internal/forgex/model"
	"github.com/castwell/forge/internal/forgex/storage"
)

// F3: the lessons feedback channel, assembled here because this is the only
// layer allowed to know both planes. internal/agent must not import
// internal/forgex, so the agent sees a narrow core.LessonSource and this file
// is the adapter that satisfies it from the control plane's index.
//
// Direction is one-way on purpose (F3-2). ForgeX derives lessons from finished
// run snapshots — it has the error envelopes, the stop decisions and the
// validation outcomes that justify a lesson. The agent has none of that for
// itself, so letting the agent write lessons would manufacture claims with no
// run behind them.

// Environment variables for the lessons channel.
//
//	FORGE_LESSONS_FEED   on enables lesson recall for agent runs (default off:
//	                     a cross-plane bridge is opt-in, like every other one)
//	FORGEX_INDEX_DB      sqlite index holding the cross-run lessons table.
//	                     Unset means <FORGEX_RUNTIME_ROOT>/index.db — the SAME
//	                     file the observer's AutoIndex writes. The two defaults
//	                     MUST agree: a different default on each side would let
//	                     an operator turn both switches on and still read an
//	                     empty index, which is the worst kind of
//	                     misconfiguration (everything looks on, nothing works).
const (
	envLessonsFeed = "FORGE_LESSONS_FEED"
	envIndexDB     = "FORGEX_INDEX_DB"
)

// defaultIndexFileName is the index file both planes default to, relative to
// FORGEX_RUNTIME_ROOT. The observer's AutoIndex writes it (see its indexPath);
// this side reads it.
const defaultIndexFileName = "index.db"

// The observer's own switches, read here only to warn when the feed is on but
// nothing will ever put a lesson into the index.
const (
	autoIndexEnv       = "FORGEX_RUNTIME_AUTO_INDEX"
	observerEnabledEnv = "FORGEX_RUNTIME_OBSERVER_ENABLED"
)

// lessonsEnabled resolves FORGE_LESSONS_FEED. Unset and unrecognised values
// both mean OFF: turning on a new cross-plane feed is a deliberate act, so a
// typo must not start feeding the agent lessons.
func lessonsEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(envLessonsFeed))) {
	case "on", "1", "true", "yes":
		return true
	case "", "off", "0", "false", "no":
		return false
	default:
		log.Printf("WARN: unknown %s %q (want on|off); keeping lesson recall off", envLessonsFeed, os.Getenv(envLessonsFeed))
		return false
	}
}

// forgexLessons adapts the control plane's lesson index to core.LessonSource.
type forgexLessons struct {
	index *storage.SQLiteIndex
	// observer records what was recalled, and nothing else. It is the agent
	// plane's ONLY write into the control plane's index — see
	// recordObservation for why the write is deliberately this narrow.
	observer *recallObserver
}

// Recall implements core.LessonSource by querying the cross-run lessons table.
//
// It also records that these lessons were offered. That record is not a
// judgement: a recall says the entry was surfaced, not whether it helped. The
// distinction is carried on the observation itself (UsageKnown).
func (f *forgexLessons) Recall(ctx context.Context, query string, topK int) ([]core.RecallItem, error) {
	lessons, err := f.index.SearchLessons(ctx, query, topK)
	if err != nil {
		return nil, fmt.Errorf("search lessons: %w", err)
	}
	items := make([]core.RecallItem, 0, len(lessons))
	for _, lesson := range lessons {
		items = append(items, recallItemFromLesson(lesson))
	}
	if f.observer != nil {
		f.observer.recall(ctx, items)
	}
	return items, nil
}

// recallObserver appends recall observations to the control plane's index.
//
// It exists so the agent plane can supply EVIDENCE for the control plane's
// lifecycle decisions without ever making one. A run never archives, never
// deletes, and never edits a memory: it says "these were offered to me", which
// is a fact, and the aggregate across runs is the control plane's business.
//
// Failure is never fatal. Observation is bookkeeping for a later offline
// decision; a run that cannot write bookkeeping should still do its work.
type recallObserver struct {
	index *storage.SQLiteIndex
	kind  storage.ObservationKind
}

// recall records that the given entries were offered to the run in ctx.
//
// UsageKnown is FALSE here, and that is the point. This run has no way to tell
// whether the model used what it was shown — it did not ask, and the model did
// not say. Recording Used=false with UsageKnown=true instead would be a claim
// the run cannot support, and the control plane would read it as "offered and
// declined" for every memory the agent ever saw, archiving the whole store.
//
// When a run CAN tell (today: nothing yet; tomorrow: an explicit citation),
// the observation carries UsageKnown=true and only then informs a decision.
func (o *recallObserver) recall(ctx context.Context, items []core.RecallItem) {
	if o == nil || o.index == nil || len(items) == 0 {
		return
	}
	// No run id means no run to attribute the evidence to. A recall from a
	// test, a CLI query or a probe is not evidence about anything.
	runID := core.ObservedRunFrom(ctx)
	if runID == "" {
		return
	}
	for _, item := range items {
		if item.ID == "" {
			continue
		}
		err := o.index.RecordObservation(ctx, storage.MemoryObservation{
			RunID:      runID,
			EntryID:    item.ID,
			Kind:       o.kind,
			Used:       false,
			UsageKnown: false,
			At:         time.Now().UTC(),
		})
		if err != nil {
			// One line, not one per entry: a failing index would otherwise
			// produce as many log lines as there were recalled items.
			log.Printf("INFO: could not record memory observation (index unwritable?): %v", err)
			return
		}
	}
}

// Close releases the index handle. The source owns the connection it opened,
// so an owner that finishes with it must be able to close it — without this a
// long-lived worker holds the database file open (and on Windows the file
// cannot even be removed while it does).
func (f *forgexLessons) Close() error {
	if f == nil || f.index == nil {
		return nil
	}
	return f.index.Close()
}

// recallItemFromLesson maps the control plane's lesson onto the agent plane's
// plain shape. Pure and table-testable: no storage, no context.
func recallItemFromLesson(lesson model.Lesson) core.RecallItem {
	return core.RecallItem{
		ID:          lesson.ID,
		Title:       lesson.Title,
		Category:    lesson.Category,
		Content:     lesson.Content,
		SourceRunID: lesson.SourceRunID,
		CreatedAt:   lesson.CreatedAt,
	}
}

// buildLessonSource opens the lessons index and returns the agent-side source.
//
// Returns nil (and says why) when the channel is off or the index cannot be
// opened: the caller then builds an agent with no lesson recall, which is the
// historical behaviour. A missing index must never fail worker registration —
// the lessons channel is an enhancement (F3-8).
func buildLessonSource() core.LessonSource {
	if !lessonsEnabled() {
		return nil
	}

	path := strings.TrimSpace(os.Getenv(envIndexDB))
	if path == "" {
		root := envOrDefault(envRuntimeRoot, defaultRuntimeRoot)
		path = filepath.Join(root, defaultIndexFileName)
	}

	// A missing file is the normal first-run state, not a failure: the index
	// is created empty and simply has no lessons to recall yet.
	index, err := storage.OpenSQLiteIndex(path)
	if err != nil {
		log.Printf("INFO: %s is on but the lessons index at %s is unreadable (%v); agent runs will start without lessons",
			envLessonsFeed, path, err)
		return nil
	}
	warnIfNothingWillIndex(path)
	log.Printf("INFO: lessons feedback enabled (index=%s)", path)
	return &forgexLessons{
		index: index,
		// Observations are recorded under the run id the agent passes to
		// Recall's caller; the observer is filled in per run by the loop. Until
		// then it holds the index and stays disabled, so a recall made outside
		// a run (a test, a CLI query) records nothing rather than filing
		// evidence under a fabricated run id.
		observer: &recallObserver{index: index, kind: storage.ObservationKindLesson},
	}
}

// warnIfNothingWillIndex names the two switches that feed this index when they
// are off.
//
// The feed reads a file the OBSERVER writes, so enabling the feed alone can
// look successful while every recall returns nothing. "Everything looks on,
// nothing works" is the most expensive kind of misconfiguration to diagnose,
// and it is cheap to say out loud here.
func warnIfNothingWillIndex(path string) {
	var missing []string
	if !envTruthy(observerEnabledEnv) {
		missing = append(missing, observerEnabledEnv+"=on")
	}
	if !envTruthy(autoIndexEnv) {
		missing = append(missing, autoIndexEnv+"=on")
	}
	if len(missing) == 0 {
		return
	}
	log.Printf("WARN: %s is on and reading %s, but nothing will write lessons into it: set %s "+
		"(the runtime observer derives and indexes lessons for finished runs)",
		envLessonsFeed, path, strings.Join(missing, " and "))
}

// envTruthy reports whether an env var holds an affirmative value. It mirrors
// the coordinator's own parser rather than importing it: the two planes each
// own their configuration reading.
func envTruthy(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}

// applyLessonsFeed installs the channel on an agent when it is available.
func applyLessonsFeed(opts []agentcore.Option) []agentcore.Option {
	source := buildLessonSource()
	if source == nil {
		return opts
	}
	return append(opts, agentcore.WithLessonSource(source))
}
