package harness

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"
)

// Durable journal defaults. They are deliberately small: the buffer only
// exists to ride out transient write failures, and the switch threshold only
// exists to stop writing at a target that has proven it is broken.
const (
	defaultJournalBufferSize = 64
	defaultJournalFailStreak = 3
)

// durableSession is the per-session part of the journal state. Seq, buffer
// and gap are per session because rebuild judges contiguity per session: a
// shared counter would put holes in one session's file that another session
// legitimately produced, and rebuild would refuse a perfectly good journal.
type durableSession struct {
	seq        int64
	inited     bool
	buffer     []RunEvent
	gap        *[2]int64 // dropped seq range awaiting a journal_gap marker
	failStreak int
	onFallback bool
}

// DurableJournal wraps a primary and an optional fallback Journal with the
// three-tier write-side fallback of the durable-run design:
//
//  1. a failed append goes to an in-memory buffer and is retried on that
//     session's next append (rides out transient failures);
//  2. after defaultJournalFailStreak consecutive failures the session's
//     active target switches to the fallback (rides out a broken primary);
//  3. when the buffer overflows the oldest events are dropped and their seq
//     range is remembered, so the next successful append writes an explicit
//     journal_gap marker before anything else.
//
// The contract it guarantees is not "writes always succeed" — it is "lost
// accounting is never silent". AppendEvent returns nil only when the event is
// durable right now; a buffered or dropped event returns an error so the
// caller's failure policy (CheckpointFailurePolicy) can decide what that
// means.
type DurableJournal struct {
	primary  Journal
	fallback Journal

	mu       sync.Mutex
	sessions map[string]*durableSession
	bufSize  int
	switchAt int
}

// DurableJournalOptions tunes the buffer and the switch threshold.
type DurableJournalOptions struct {
	BufferSize  int // events held while the target is failing; default 64
	SwitchAfter int // consecutive failures before switching to fallback; default 3
}

// NewDurableJournal wraps primary (and optionally fallback). With no fallback
// the tiers still work: buffer first, then honest loss through the gap marker.
func NewDurableJournal(primary, fallback Journal, opts DurableJournalOptions) *DurableJournal {
	d := &DurableJournal{primary: primary, fallback: fallback, sessions: map[string]*durableSession{}}
	if opts.BufferSize > 0 {
		d.bufSize = opts.BufferSize
	} else {
		d.bufSize = defaultJournalBufferSize
	}
	if opts.SwitchAfter > 0 {
		d.switchAt = opts.SwitchAfter
	} else {
		d.switchAt = defaultJournalFailStreak
	}
	return d
}

// session returns (creating on first use) the state for one session.
func (d *DurableJournal) session(runID string) *durableSession {
	s := d.sessions[runID]
	if s == nil {
		s = &durableSession{}
		d.sessions[runID] = s
	}
	return s
}

// initSeq continues the session's sequence after whatever is already on
// disk, so a restarted process does not renumber events it never saw. A read
// failure leaves the counter at 0 (first event gets seq 1): a duplicate seq
// shows up to rebuild as a discontinuity, which refuses — the conservative
// direction.
func (d *DurableJournal) initSeq(ctx context.Context, runID string, s *durableSession) {
	if s.inited {
		return
	}
	s.inited = true
	var max int64
	for _, target := range []Journal{d.primary, d.fallback} {
		if target == nil {
			continue
		}
		events, err := target.ReadEvents(ctx, runID)
		if err != nil {
			log.Printf("[harness] journal seq init for %s failed (continuing from 1): %v", runID, err)
			continue
		}
		for _, ev := range events {
			if ev.Seq > max {
				max = ev.Seq
			}
		}
	}
	s.seq = max
}

// AppendEvent assigns a session seq, applies the three tiers, and reports
// whether the event is durable at return time.
func (d *DurableJournal) AppendEvent(ctx context.Context, ev RunEvent) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if d.primary == nil {
		return fmt.Errorf("durable journal has no primary target")
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	s := d.session(ev.RunID)
	d.initSeq(ctx, ev.RunID, s)

	// The gap marker is written first so that, whatever happens next, the
	// loss is on the record before any surviving event. It consumes its own
	// seq before the event does, so marker.seq < event.seq holds and a seq
	// scan orders them correctly.
	var pending []RunEvent
	if s.gap != nil {
		pending = append(pending, d.gapMarker(s, ev.RunID))
	}
	s.seq++
	ev.Seq = s.seq
	if len(s.buffer) > 0 {
		pending = append(pending, s.buffer...)
	}
	pending = append(pending, ev)

	active := d.activeTarget(s)
	var remaining []RunEvent
	var lastErr error
	for i, e := range pending {
		if err := active.AppendEvent(ctx, e); err != nil {
			remaining = append(remaining, pending[i:]...)
			lastErr = err
			break
		}
		if e.Type == EventJournalGap {
			s.gap = nil
		}
	}
	if lastErr == nil {
		s.buffer = s.buffer[:0]
		s.failStreak = 0
		return nil
	}

	// Write failed from here on: the unwritten tail goes back to the buffer,
	// oldest first — except a gap marker, which never enters the buffer. The
	// marker is regenerated from s.gap on every attempt, so keeping a copy
	// would only risk its seq drifting into the very range it describes (and
	// evicting it would silently discard the loss record). s.gap stays set
	// until a marker is actually written above.
	s.buffer = s.buffer[:0]
	for _, e := range remaining {
		if e.Type == EventJournalGap {
			continue
		}
		s.buffer = d.dropOldestToFit(s, e)
	}
	s.failStreak++
	if d.fallback != nil && !s.onFallback && s.failStreak >= d.switchAt {
		log.Printf("[harness] journal primary failing for %s (%d in a row), switching to fallback target",
			ev.RunID, s.failStreak)
		s.onFallback = true
	}
	return fmt.Errorf("journal append not durable (event buffered or dropped): %w", lastErr)
}

// activeTarget is the session's current write target.
func (d *DurableJournal) activeTarget(s *durableSession) Journal {
	if s.onFallback && d.fallback != nil {
		return d.fallback
	}
	return d.primary
}

// dropOldestToFit places e in the buffer, evicting the oldest event into the
// pending gap range when the buffer is full.
func (d *DurableJournal) dropOldestToFit(s *durableSession, e RunEvent) []RunEvent {
	if len(s.buffer) < d.bufSize {
		return append(s.buffer, e)
	}
	evicted := s.buffer[0]
	if s.gap == nil {
		s.gap = &[2]int64{evicted.Seq, evicted.Seq}
	} else {
		if evicted.Seq < s.gap[0] {
			s.gap[0] = evicted.Seq
		}
		if evicted.Seq > s.gap[1] {
			s.gap[1] = evicted.Seq
		}
	}
	return append(s.buffer[1:], e)
}

// gapMarker is the explicit "events N..M were lost" record. Its own seq is
// fresh; the lost ranges live in Data, because they belong to seq numbers
// that will never appear in any file.
func (d *DurableJournal) gapMarker(s *durableSession, runID string) RunEvent {
	s.seq++
	return RunEvent{
		Seq:   s.seq,
		RunID: runID,
		Type:  EventJournalGap,
		TS:    time.Now().UTC(),
		Data:  map[string]any{"gap_from": s.gap[0], "gap_to": s.gap[1]},
	}
}

// ReadEvents merges primary and fallback by seq. On a duplicate seq the
// primary copy wins, as documented for the merge.
func (d *DurableJournal) ReadEvents(ctx context.Context, sessionID string) ([]RunEvent, error) {
	if d.primary == nil {
		return nil, fmt.Errorf("durable journal has no primary target")
	}
	primary, err := d.primary.ReadEvents(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	merged := primary
	if d.fallback != nil {
		seen := make(map[int64]bool, len(primary))
		for _, ev := range primary {
			seen[ev.Seq] = true
		}
		secondary, err := d.fallback.ReadEvents(ctx, sessionID)
		if err != nil {
			return nil, err
		}
		for _, ev := range secondary {
			if !seen[ev.Seq] {
				merged = append(merged, ev)
			}
		}
	}
	sortEvents(merged)
	return merged, nil
}

var _ Journal = (*DurableJournal)(nil)
