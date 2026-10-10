package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/castwell/forge/internal/event"
	"github.com/castwell/forge/internal/storage"
)

// Replaying a workflow's history.
//
// The event log has been written since the beginning — every state change lands
// in it, and the project's own rule calls it "the only credential" for audit and
// time travel. Nothing read it back: `internal/event` had zero importers outside
// its own tests, so a workflow's past was recorded faithfully and unreadable.
//
// This is the entry point. It answers three questions an operator actually has:
// what happened (the events), what state did that leave (the reconstruction),
// and what did it look like at a point in time (replay up to a sequence number).
//
//	FORGE_PG_DSN      the same switch every other entry point uses; unset means
//	                  the embedded BoltDB at FORGE_BOLT_PATH (default forge.db)
//	FORGE_BOLT_PATH   embedded database path
func runHistory(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		// Say what is missing before the usage text. A bare usage dump leaves the
		// reader to work out which part they got wrong.
		fmt.Fprintln(stderr, "forge history: a workflow id is required")
		fmt.Fprintln(stderr)
		historyUsage(stderr)
		return 1
	}

	// Parse the small surface by hand, matching the rest of this CLI: no flags.
	var (
		workflowID string
		until      int64
		asJSON     bool
		eventsOnly bool
	)
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			asJSON = true
		case "--events":
			eventsOnly = true
		case "--until":
			if i+1 >= len(args) {
				fmt.Fprintln(stderr, "forge history: --until needs a sequence number")
				return 1
			}
			n, err := parseSeq(args[i+1])
			if err != nil {
				fmt.Fprintf(stderr, "forge history: --until: %v\n", err)
				return 1
			}
			until = n
			i++
		default:
			if strings.HasPrefix(args[i], "-") {
				fmt.Fprintf(stderr, "forge history: unknown option %q\n\n", args[i])
				historyUsage(stderr)
				return 1
			}
			if workflowID != "" {
				fmt.Fprintf(stderr, "forge history: unexpected extra argument %q\n\n", args[i])
				historyUsage(stderr)
				return 1
			}
			workflowID = args[i]
		}
	}

	if workflowID == "" {
		fmt.Fprintln(stderr, "forge history: a workflow id is required")
		historyUsage(stderr)
		return 1
	}

	store, err := openHistoryStorage()
	if err != nil {
		fmt.Fprintf(stderr, "forge history: %v\n", err)
		return 1
	}
	defer store.Close()

	ctx := context.Background()
	history, err := store.GetWorkflowHistory(ctx, workflowID)
	if err != nil {
		fmt.Fprintf(stderr, "forge history: read events for %s: %v\n", workflowID, err)
		return 1
	}
	if len(history) == 0 {
		// An empty history is reported as such rather than as an empty success:
		// "this workflow has no events" and "this id is wrong" look the same in
		// an empty table, and only one of them is worth investigating.
		fmt.Fprintf(stderr, "forge history: workflow %s has no recorded events\n", workflowID)
		return 1
	}

	// Reconstruct the state. A failure here is reported but does not stop the
	// event listing: the events are the primary record, and a reconstruction bug
	// should not hide them.
	var state *event.WorkflowState
	var replayErr error
	if until > 0 {
		state, replayErr = event.ReplayUntil(history, until)
	} else {
		state, replayErr = event.Replay(history)
	}

	if asJSON {
		return writeHistoryJSON(workflowID, history, state, until, stdout, stderr)
	}

	if eventsOnly {
		writeEventTable(history, stdout)
		return 0
	}

	if replayErr != nil {
		fmt.Fprintf(stderr, "WARN: reconstruct state: %v\n\n", replayErr)
	}

	// With a cutoff, the printed log is truncated to match the reconstruction.
	// Printing events the state does not include would make the report
	// self-contradictory: the summary would say RUNNING while the log below it
	// showed a failure.
	printed := history
	if until > 0 {
		printed = eventsUpTo(history, until)
	}

	writeStateSummary(workflowID, state, until, stdout)
	fmt.Fprintln(stdout)
	writeEventTable(printed, stdout)
	return 0
}

// eventsUpTo returns the events at or before a sequence number.
func eventsUpTo(history []*storage.Event, maxSeq int64) []*storage.Event {
	out := make([]*storage.Event, 0, len(history))
	for _, e := range history {
		if e.SequenceNum <= maxSeq {
			out = append(out, e)
		}
	}
	return out
}

// writeStateSummary prints what the events add up to.
func writeStateSummary(workflowID string, state *event.WorkflowState, until int64, w io.Writer) {
	if state == nil {
		fmt.Fprintf(w, "workflow %s: state could not be reconstructed\n", workflowID)
		return
	}

	if until > 0 {
		fmt.Fprintf(w, "workflow %s (reconstructed up to seq %d)\n", workflowID, until)
	} else {
		fmt.Fprintf(w, "workflow %s\n", workflowID)
	}
	fmt.Fprintf(w, "  status: %s\n", state.Status)
	if state.StartedAt != nil {
		fmt.Fprintf(w, "  started: %s\n", state.StartedAt.Format(time.RFC3339))
	}
	if state.FinishedAt != nil {
		fmt.Fprintf(w, "  finished: %s\n", state.FinishedAt.Format(time.RFC3339))
	}
	if state.ErrorMsg != "" {
		fmt.Fprintf(w, "  error: %s\n", state.ErrorMsg)
	}
	fmt.Fprintf(w, "  events: %d\n", len(state.Events))

	if len(state.Tasks) == 0 {
		return
	}

	// Tasks are sorted by name so two runs of the same history read the same.
	// The map's iteration order is randomised, and a report that reshuffles
	// itself is a report nobody can diff.
	names := make([]string, 0, len(state.Tasks))
	for name := range state.Tasks {
		names = append(names, name)
	}
	sort.Strings(names)

	fmt.Fprintf(w, "  tasks: %d\n", len(names))
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "    NAME\tSTATUS\tATTEMPTS\tERROR")
	for _, name := range names {
		task := state.Tasks[name]
		fmt.Fprintf(tw, "    %s\t%s\t%d\t%s\n", name, task.Status, task.Attempts, truncate(task.ErrorMsg, 40))
	}
	tw.Flush()
}

// writeEventTable prints the raw log.
func writeEventTable(history []*storage.Event, w io.Writer) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SEQ\tTIME\tTYPE\tTASK\tPAYLOAD")
	for _, e := range history {
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\n",
			e.SequenceNum,
			e.Timestamp.Format("15:04:05.000"),
			e.Type,
			orDash(e.TaskID),
			truncate(string(e.Payload), 60),
		)
	}
	tw.Flush()
}

// writeHistoryJSON emits the same information for a program to read.
func writeHistoryJSON(workflowID string, history []*storage.Event, state *event.WorkflowState, until int64, stdout, stderr io.Writer) int {
	out := map[string]any{
		"workflow_id": workflowID,
		"event_count": len(history),
	}
	if until > 0 {
		out["until"] = until
	}
	if state != nil {
		out["state"] = state
	}

	body, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		fmt.Fprintf(stderr, "forge history: marshal: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, string(body))
	return 0
}

// openHistoryStorage opens the store the same way every other entry point does.
//
// It duplicates a few lines of the coordinator's own opener rather than
// exporting it: this is a read-only reporting path, and giving the CLI a way to
// open the serving storage would invite it to write.
func openHistoryStorage() (storage.Storage, error) {
	ctx := context.Background()

	if dsn := strings.TrimSpace(os.Getenv("FORGE_PG_DSN")); dsn != "" {
		pg, err := storage.NewPGStorage(ctx, dsn)
		if err != nil {
			return nil, fmt.Errorf("connect postgres: %w", err)
		}
		return pg, nil
	}

	path := strings.TrimSpace(os.Getenv("FORGE_BOLT_PATH"))
	if path == "" {
		path = "forge.db"
	}
	store, err := storage.NewBoltStorage(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	return store, nil
}

// parseSeq reads a sequence number.
func parseSeq(s string) (int64, error) {
	var n int64
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		return 0, fmt.Errorf("%q is not a sequence number", s)
	}
	if n <= 0 {
		return 0, fmt.Errorf("a sequence number must be positive, got %d", n)
	}
	return n, nil
}

// orDash renders an empty field as a dash so a table column does not look broken.
func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// truncate shortens a field for a table, marking that it was cut.
func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func historyUsage(w io.Writer) {
	fmt.Fprint(w, `forge history — replay a workflow's recorded events

Usage:
  forge history <workflow-id> [options]

Options:
  --events        print only the event log
  --until <seq>   reconstruct the state as of a sequence number (time travel)
  --json          machine-readable output

Environment:
  FORGE_PG_DSN      PostgreSQL connection; unset uses the embedded BoltDB
  FORGE_BOLT_PATH   embedded database path (default forge.db)

The event log is the record of what happened: every state change was already
written there. This is the reader that makes it usable.
`)
}
