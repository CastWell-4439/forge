package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	agentcore "github.com/castwell/forge/internal/agent/core"
	"github.com/castwell/forge/internal/forgex/storage"
)

// forgex memory — lifecycle management for long-term memory and lessons.
//
// Subcommands:
//
//	memory archive    evaluate observations and mark unused entries archived
//	memory list       show what is archived, and why
//	memory recover    return an entry to the recall pool
//	memory prune      permanently delete entries archived long enough
//
// Two rules shape every one of them:
//
//   - The agent never runs these. It appends observations; the decisions happen
//     here, offline, where they can be reviewed. Memory is shared and its
//     contents are model-generated, so a single run must not be able to destroy
//     what other runs depend on.
//   - Anything irreversible defaults to a dry run. `prune` prints what it would
//     delete and changes nothing until --execute. Archiving is reversible and
//     runs directly; deleting is not and must be asked for.
const memoryUsage = `forgex memory — manage long-term memory and lesson lifecycle

Usage:
  forgex memory archive [--kind memory|lesson] [--min-known N] [--execute]
  forgex memory list    [--kind memory|lesson] [--recovered]
  forgex memory recover --id ID
  forgex memory prune   [--archive-before DURATION] [--kind memory|lesson] [--execute]

Flags:
  --root PATH          ForgeX root directory (default .forgex)
  --index PATH         SQLite index path (default <root>/index.db)
  --kind KIND          memory (default) or lesson
  --min-known N        known usage signals needed before "never used" counts
  --archive-before D   prune entries archived longer than this (default 720h)
  --execute            actually make the change (default: dry run)
  --recovered          include recovered entries in the listing

Why the default is a dry run: archiving is a decision that can be undone, so it
runs; deletion cannot be undone, so it asks.`

// runMemory dispatches the memory subcommands.
func runMemory(args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, memoryUsage+"\n")
		return fmt.Errorf("memory: a subcommand is required")
	}
	switch args[0] {
	case "archive":
		return memoryArchive(args[1:])
	case "list":
		return memoryList(args[1:])
	case "recover":
		return memoryRecover(args[1:])
	case "prune":
		return memoryPrune(args[1:])
	case "help", "-h", "--help":
		fmt.Fprint(os.Stdout, memoryUsage+"\n")
		return nil
	default:
		fmt.Fprint(os.Stderr, memoryUsage+"\n")
		return fmt.Errorf("memory: unknown subcommand %q", args[0])
	}
}

// openIndex resolves --root/--index and opens the store.
func openIndex(fs *flag.FlagSet, root, indexPath *string) (*storage.SQLiteIndex, error) {
	path := *indexPath
	if path == "" {
		path = defaultIndexPath(*root)
	}
	return storage.OpenSQLiteIndex(path)
}

// defaultIndexPath is the index both planes default to.
func defaultIndexPath(root string) string {
	if root == "" {
		root = ".forgex"
	}
	return root + string(os.PathSeparator) + "index.db"
}

// observationKind resolves --kind, defaulting to memory.
func observationKind(raw string) (storage.ObservationKind, error) {
	switch raw {
	case "", "memory":
		return storage.ObservationKindMemory, nil
	case "lesson":
		return storage.ObservationKindLesson, nil
	default:
		return "", fmt.Errorf("unknown --kind %q (want memory or lesson)", raw)
	}
}

// memoryArchive evaluates observations and archives what has been offered many
// times and never used.
//
// Archiving is reversible, so it runs without --execute. What it does NOT do is
// delete: an archived entry leaves the recall pool and stays in storage, which
// is the correct response to an aggregate that can be wrong.
func memoryArchive(args []string) error {
	fs := flag.NewFlagSet("memory archive", flag.ContinueOnError)
	root := fs.String("root", ".forgex", "ForgeX root directory")
	indexPath := fs.String("index", "", "SQLite index path (default <root>/index.db)")
	kindFlag := fs.String("kind", "memory", "memory or lesson")
	minKnown := fs.Int("min-known", 0, "known usage signals needed before \"never used\" counts")
	execute := fs.Bool("execute", false, "actually archive (default: dry run)")
	persistent := fs.String("persistent", "", "comma-separated entry ids to treat as persistent")
	if err := fs.Parse(args); err != nil {
		return err
	}
	kind, err := observationKind(*kindFlag)
	if err != nil {
		return err
	}
	idx, err := openIndex(fs, root, indexPath)
	if err != nil {
		return err
	}
	defer idx.Close()

	ctx := context.Background()

	// Types come from an explicit list rather than being inferred, because on a
	// first evaluation every usage count is zero — there is no evidence yet to
	// classify from, and guessing would either protect everything or nothing.
	types := parseTypeList(*persistent, agentcore.MemoryPersistent)

	decisions, err := evaluateArchive(ctx, idx, kind, types, *minKnown)
	if err != nil {
		return err
	}

	toArchive := 0
	for _, d := range decisions {
		if d.Archive {
			toArchive++
		}
	}

	fmt.Fprintf(os.Stdout, "evaluated %d entries, %d to archive (%s)\n", len(decisions), toArchive, modeLabel(*execute))
	for _, d := range decisions {
		if !d.Archive {
			continue
		}
		fmt.Fprintf(os.Stdout, "  archive %s: %s\n", d.EntryID, d.Reason)
	}
	if !*execute {
		fmt.Fprintln(os.Stdout, "\ndry run: nothing changed. Re-run with --execute to archive.")
		return nil
	}

	for _, d := range decisions {
		if !d.Archive {
			continue
		}
		if err := idx.ArchiveEntry(ctx, d.EntryID, kind, d.Reason, time.Now().UTC()); err != nil {
			return err
		}
	}
	fmt.Fprintf(os.Stdout, "archived %d entries (recover with: forgex memory recover --id ID)\n", toArchive)
	return nil
}

// evaluateArchive is the shared evaluation used by archive and prune reporting.
func evaluateArchive(ctx context.Context, idx *storage.SQLiteIndex, kind storage.ObservationKind, types map[string]agentcore.MemoryType, minKnown int) ([]agentcore.ArchiveDecision, error) {
	obs, err := idx.LoadObservations(ctx, kind, 0)
	if err != nil {
		return nil, err
	}
	if len(obs) == 0 {
		return nil, nil
	}

	// Convert the storage shape into the core shape. The two packages share the
	// vocabulary but not the types, deliberately: the storage layer is usable
	// without the agent layer.
	coreObs := make([]agentcore.MemoryObservation, 0, len(obs))
	for _, o := range obs {
		coreObs = append(coreObs, agentcore.MemoryObservation{
			RunID:      o.RunID,
			EntryID:    o.EntryID,
			Kind:       agentcore.ObservationKind(o.Kind),
			Used:       o.Used,
			UsageKnown: o.UsageKnown,
			At:         o.At,
		})
	}
	stats := agentcore.AggregateObservations(coreObs)

	policy := agentcore.DefaultArchivePolicy()
	if minKnown > 0 {
		policy.MinKnown = minKnown
	}
	policy.Now = time.Now().UTC()

	decisions := make([]agentcore.ArchiveDecision, 0, len(stats))
	for entryID, stat := range stats {
		memType := types[entryID]
		decisions = append(decisions, agentcore.ShouldArchive(entryID, memType, stat, policy))
	}
	return agentcore.SortedDecisions(decisions), nil
}

// memoryList shows the archive, including why each entry left the pool.
func memoryList(args []string) error {
	fs := flag.NewFlagSet("memory list", flag.ContinueOnError)
	root := fs.String("root", ".forgex", "ForgeX root directory")
	indexPath := fs.String("index", "", "SQLite index path (default <root>/index.db)")
	kindFlag := fs.String("kind", "memory", "memory or lesson")
	recovered := fs.Bool("recovered", false, "include recovered entries")
	if err := fs.Parse(args); err != nil {
		return err
	}
	kind, err := observationKind(*kindFlag)
	if err != nil {
		return err
	}
	idx, err := openIndex(fs, root, indexPath)
	if err != nil {
		return err
	}
	defer idx.Close()

	records, err := idx.ListArchive(context.Background(), kind, *recovered)
	if err != nil {
		return err
	}
	if len(records) == 0 {
		fmt.Fprintf(os.Stdout, "no archived %s entries\n", kind)
		return nil
	}
	for _, r := range records {
		state := "archived"
		if r.Recovered {
			state = "recovered"
		}
		fmt.Fprintf(os.Stdout, "%s  %-9s  %s  %s\n",
			r.EntryID, state, r.ArchivedAt.Format(time.RFC3339), r.Reason)
	}
	return nil
}

// memoryRecover returns an entry to the recall pool.
func memoryRecover(args []string) error {
	fs := flag.NewFlagSet("memory recover", flag.ContinueOnError)
	root := fs.String("root", ".forgex", "ForgeX root directory")
	indexPath := fs.String("index", "", "SQLite index path (default <root>/index.db)")
	id := fs.String("id", "", "entry id to recover")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" {
		return fmt.Errorf("--id is required")
	}
	idx, err := openIndex(fs, root, indexPath)
	if err != nil {
		return err
	}
	defer idx.Close()

	if err := idx.RecoverEntry(context.Background(), *id, time.Now().UTC()); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "recovered %s: it will be recalled again\n", *id)
	return nil
}

// memoryPrune permanently deletes entries that have been archived long enough.
//
// This is the only destructive operation in the lifecycle, and it is the reason
// the design has a retention window at all: archiving fixes recall quality but
// the store still grows, and the memory store is a single file rewritten whole
// on every write — so unbounded growth is a real cost, not a tidiness concern.
//
// It defaults to a dry run. The threshold is an aggregate judgement that can be
// wrong, and deletion cannot be undone, so the operator sees the list before it
// happens.
func memoryPrune(args []string) error {
	fs := flag.NewFlagSet("memory prune", flag.ContinueOnError)
	root := fs.String("root", ".forgex", "ForgeX root directory")
	indexPath := fs.String("index", "", "SQLite index path (default <root>/index.db)")
	kindFlag := fs.String("kind", "memory", "memory or lesson")
	archiveBefore := fs.Duration("archive-before", 720*time.Hour, "delete entries archived longer than this")
	execute := fs.Bool("execute", false, "actually delete (default: dry run)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	kind, err := observationKind(*kindFlag)
	if err != nil {
		return err
	}
	idx, err := openIndex(fs, root, indexPath)
	if err != nil {
		return err
	}
	defer idx.Close()

	ctx := context.Background()
	records, err := idx.ListArchive(ctx, kind, false)
	if err != nil {
		return err
	}

	cutoff := time.Now().UTC().Add(-*archiveBefore)
	var eligible []storage.ArchiveRecord
	for _, r := range records {
		if r.ArchivedAt.Before(cutoff) {
			eligible = append(eligible, r)
		}
	}

	fmt.Fprintf(os.Stdout, "archived %s entries: %d total, %d older than %s\n",
		kind, len(records), len(eligible), archiveBefore.String())
	for _, r := range eligible {
		fmt.Fprintf(os.Stdout, "  delete %s (archived %s: %s)\n",
			r.EntryID, r.ArchivedAt.Format(time.RFC3339), r.Reason)
	}
	if len(eligible) == 0 {
		return nil
	}
	if !*execute {
		fmt.Fprintln(os.Stdout, "\ndry run: nothing deleted. Re-run with --execute to delete permanently.")
		return nil
	}

	deleted := 0
	for _, r := range eligible {
		if err := deleteEntry(ctx, idx, kind, r.EntryID); err != nil {
			// One failure must not abort the rest: the others are equally
			// eligible, and stopping would leave the store in a half-pruned
			// state with no record of which half.
			fmt.Fprintf(os.Stderr, "prune %s: %v\n", r.EntryID, err)
			continue
		}
		deleted++
	}
	fmt.Fprintf(os.Stdout, "deleted %d %s entries\n", deleted, kind)
	return nil
}

// deleteEntry removes an entry from its store and forgets its archive row.
//
// The archive row goes too: leaving it would make the next archive pass see an
// entry that no longer exists, and a stale row that says "archived" for a
// deleted record is exactly the kind of inconsistency that makes an operator
// distrust the listing.
func deleteEntry(ctx context.Context, idx *storage.SQLiteIndex, kind storage.ObservationKind, entryID string) error {
	switch kind {
	case storage.ObservationKindLesson:
		if err := idx.DeleteLesson(ctx, entryID); err != nil {
			return err
		}
	default:
		// Long-term memories live in the agent plane's document store. This
		// command reports them as eligible but does not reach into that store:
		// the store belongs to the agent plane, and a control-plane command
		// deleting rows from it would cross the same boundary the whole design
		// keeps. The report is the deliverable here; the deletion of memory
		// records is done by the component that owns them.
		return fmt.Errorf("memory records are owned by the agent plane's store; " +
			"this command archives and reports them, and deletion requires the store's owner")
	}
	return idx.ForgetEntry(ctx, entryID)
}

// parseTypeList turns "id,id" into a type map for the named entries.
func parseTypeList(raw string, memType agentcore.MemoryType) map[string]agentcore.MemoryType {
	out := map[string]agentcore.MemoryType{}
	for _, part := range splitAndTrim(raw) {
		out[part] = memType
	}
	return out
}

// splitAndTrim splits on commas and drops empty parts.
func splitAndTrim(raw string) []string {
	var out []string
	for _, part := range splitComma(raw) {
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// splitComma is a small helper kept separate so the trimming is testable.
func splitComma(raw string) []string {
	var (
		out  []string
		cur  []rune
		trim = func(r rune) bool { return r == ' ' || r == '\t' || r == '\n' || r == '\r' }
	)
	flush := func() {
		s := string(cur)
		for len(s) > 0 && trim(rune(s[0])) {
			s = s[1:]
		}
		for len(s) > 0 && trim(rune(s[len(s)-1])) {
			s = s[:len(s)-1]
		}
		out = append(out, s)
		cur = cur[:0]
	}
	for _, r := range raw {
		if r == ',' {
			flush()
			continue
		}
		cur = append(cur, r)
	}
	flush()
	return out
}

// modeLabel names whether a command will act.
func modeLabel(execute bool) string {
	if execute {
		return "executing"
	}
	return "dry run"
}
