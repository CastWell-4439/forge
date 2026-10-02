package harness

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Journal event types. The vocabulary mirrors what the loop actually does;
// run_paused/run_resumed pause semantics arrive with the HITL round, but
// run_resumed already exists because a crash-recovery resume also restarts
// the logical run record.
const (
	EventRunStarted       = "run_started"       // a fresh logical run begins
	EventRunResumed       = "run_resumed"       // a resume continues a logical run (checkpoint or journal rebuild)
	EventStepStarted      = "step_started"      // one ReAct iteration begins
	EventLLMCall          = "llm_call"          // an LLM call returned with usage
	EventContextCompacted = "context_compacted" // messages were replaced; carries a full snapshot
	EventToolStarted      = "tool_started"      // intent recorded before the tool runs
	EventToolCompleted    = "tool_completed"    // tool returned (result or error)
	EventStepCompleted    = "step_completed"    // step finished; carries the messages it appended
	EventRunCompleted     = "run_completed"     // final answer recorded
	EventRunEnded         = "run_ended"         // run ended without completing (max_steps, error, ...)
	EventJournalGap       = "journal_gap"       // marker: events with the listed seq range were lost
)

// ErrNoJournalEvents reports that the journal has nothing for this session.
// It is the journal-side twin of core.ErrNoCheckpoint: "nothing to rebuild
// from" is a normal first call, not a read failure.
var ErrNoJournalEvents = errors.New("no journal events")

// ErrJournalGap reports that the journal cannot be trusted to rebuild state:
// it carries an explicit gap marker, a sequence discontinuity, or events
// without a run-start scope. Rebuilding anyway could silently drop a tool
// that already ran, so the caller must refuse to run rather than guess.
var ErrJournalGap = errors.New("journal has a gap; refusing to rebuild")

// RunEvent is one append-only entry of a run journal.
//
// The journal is the run's source of truth: it answers "what happened", while
// a checkpoint is only a materialised view for fast resume. Fields that are
// meaningful to one event type live in Data, so the struct stays a stable
// envelope across the vocabulary above.
type RunEvent struct {
	// Seq is a session-monotonic sequence number assigned by the journal.
	// Rebuild trusts seq, never file line order.
	Seq   int64          `json:"seq"`
	RunID string         `json:"run_id"` // the agent session id
	Type  string         `json:"type"`
	Step  int            `json:"step,omitempty"`
	Tool  string         `json:"tool,omitempty"`
	TS    time.Time      `json:"ts"`
	Data  map[string]any `json:"data,omitempty"`
}

// Journal is the write and read seam for run journals. The loop defines it;
// implementations are injected from the outside so internal/agent never
// imports internal/forgex. A nil Journal disables everything (no-op).
type Journal interface {
	// AppendEvent records one event. Returning nil means the event is durable
	// at return time; a non-nil error means it is only buffered or was dropped,
	// and the caller's checkpoint failure policy decides whether that is fatal.
	AppendEvent(ctx context.Context, ev RunEvent) error
	// ReadEvents returns all events for a session, merged and sorted by seq.
	// A session with no journal returns (nil, nil).
	ReadEvents(ctx context.Context, sessionID string) ([]RunEvent, error)
}

// PathResolver maps a session id onto the journal file for that session.
type PathResolver func(sessionID string) string

// FileJournal is the standard file-backed Journal: one JSONL file per
// session, one event per line.
//
// Seq assignment: events arrive with their seq already assigned when they
// come from DurableJournal; an event with Seq 0 (a bare FileJournal used
// directly) gets the next free seq for its session, continuing from whatever
// the file already holds. Without that, a direct user would write all-zero
// seqs and every rebuild would read them as corruption.
//
// Read tolerance mirrors what an append-only file can actually produce: a
// crash mid-write may leave a half-written *last* line, and skipping it is
// the difference between "tail broken" (safe, see the design note on the two
// kinds of loss) and a journal that refuses to read at all. A parse failure
// anywhere else means real corruption and is reported as ErrJournalGap.
type FileJournal struct {
	resolve PathResolver
	mu      sync.Mutex
	seqs    map[string]int64
	inited  map[string]bool
}

// NewFileJournal creates a FileJournal writing to the paths resolve returns.
func NewFileJournal(resolve PathResolver) *FileJournal {
	return &FileJournal{
		resolve: resolve,
		seqs:    map[string]int64{},
		inited:  map[string]bool{},
	}
}

// AppendEvent writes one JSONL line, creating parent directories as needed.
func (j *FileJournal) AppendEvent(ctx context.Context, ev RunEvent) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if j.resolve == nil {
		return fmt.Errorf("journal has no path resolver")
	}
	path := j.resolve(ev.RunID)

	j.mu.Lock()
	defer j.mu.Unlock()

	ev.Seq = j.seqFor(path, ev.RunID, ev.Seq)

	data, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("marshal journal event %s: %w", ev.Type, err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create journal dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open journal %s: %w", path, err)
	}
	defer f.Close()
	// Remove a torn tail before appending. Without this, a half-written line
	// from a crashed write would concatenate with the next event's line and
	// hide a possibly *completed* event inside garbage — which the read side
	// would then skip as a torn tail. Silent loss. Truncating to the last
	// newline makes every line either fully written or the single torn last
	// one, which is exactly the loss shape the read side can judge safely.
	if err := truncateTornTail(f, path); err != nil {
		return err
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("write journal %s: %w", path, err)
	}
	return nil
}

// seqFor returns the seq to write: an explicit one is kept and folded into
// the session counter; a zero one gets the next number. The counter is
// initialised once per session from what is already on disk, so restarts and
// mixed usage never renumber existing events. Read failures during init log
// and continue from zero — a duplicate that causes would surface to rebuild
// as a discontinuity, which refuses.
func (j *FileJournal) seqFor(path, runID string, explicit int64) int64 {
	if !j.inited[runID] {
		j.inited[runID] = true
		var max int64
		events, err := readJournalFile(path)
		if err != nil {
			log.Printf("[harness] journal seq init for %s failed (continuing from 0): %v", runID, err)
		}
		for _, ev := range events {
			if ev.Seq > max {
				max = ev.Seq
			}
		}
		j.seqs[runID] = max
	}
	if explicit > 0 {
		if explicit > j.seqs[runID] {
			j.seqs[runID] = explicit
		}
		return explicit
	}
	j.seqs[runID]++
	return j.seqs[runID]
}

// truncateTornTail drops bytes after the file's last newline. Called with the
// file opened for append; when the tail is already clean this costs one
// backward probe and no truncation.
func truncateTornTail(f *os.File, path string) error {
	size, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return fmt.Errorf("journal %s: stat: %w", path, err)
	}
	if size == 0 {
		return nil
	}
	if size > 0 {
		// Find the offset of the last newline by probing backwards.
		const block = 4096
		buf := make([]byte, block)
		pos := size
		lastNewline := int64(-1)
		for pos > 0 {
			start := pos - block
			if start < 0 {
				start = 0
			}
			n := int(pos - start)
			if _, err := f.ReadAt(buf[:n], start); err != nil {
				return fmt.Errorf("journal %s: probe tail: %w", path, err)
			}
			for i := n - 1; i >= 0; i-- {
				if buf[i] == '\n' {
					lastNewline = start + int64(i)
					break
				}
			}
			if lastNewline >= 0 || start == 0 {
				break
			}
			pos = start
		}
		clean := lastNewline + 1 // -1 (no newline at all) means truncate to 0
		if clean < size {
			// Truncate by path, not through the append handle: Windows opens
			// O_APPEND handles without the right to resize the file.
			if err := os.Truncate(path, clean); err != nil {
				return fmt.Errorf("journal %s: drop torn tail: %w", path, err)
			}
		}
	}
	return nil
}

// ReadEvents reads the session journal. A missing file is an empty journal,
// not an error. A corrupt line anywhere except the tail refuses the whole
// read (ErrJournalGap); a truncated last line is tolerated.
func (j *FileJournal) ReadEvents(_ context.Context, sessionID string) ([]RunEvent, error) {
	if j.resolve == nil {
		return nil, fmt.Errorf("journal has no path resolver")
	}
	return readJournalFile(j.resolve(sessionID))
}

// readJournalFile parses one journal file. It takes no journal state, so it
// is safe to call while an append is in flight (a reader sees a prefix of
// what the writer has landed).
func readJournalFile(path string) ([]RunEvent, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open journal %s: %w", path, err)
	}
	defer f.Close()

	var events []RunEvent
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		text := strings.TrimSpace(scanner.Text())
		if text == "" {
			continue
		}
		var ev RunEvent
		if err := json.Unmarshal([]byte(text), &ev); err != nil {
			// The last scanner line being unparseable is the classic torn
			// tail; anything earlier is corruption a rebuild must not trust.
			if scanner.Scan() {
				return nil, fmt.Errorf("%w: corrupt journal %s line %d", ErrJournalGap, path, line)
			}
			break
		}
		events = append(events, ev)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read journal %s: %w", path, err)
	}
	return events, nil
}

var _ Journal = (*FileJournal)(nil)

// sortEvents orders by seq; equal seqs keep insertion order (primary before
// fallback) so the primary copy wins a duplicate.
func sortEvents(events []RunEvent) {
	sort.SliceStable(events, func(i, k int) bool { return events[i].Seq < events[k].Seq })
}
