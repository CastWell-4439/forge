package storage

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Memory lifecycle storage: observations (append-only) and archive state.
//
// This file is the control plane's half of the lifecycle design. The agent half
// (internal/serve/worker) may only APPEND observations; every decision — what
// counts as unused, what gets archived, what may finally be deleted — is made
// here and executed through the CLI, where it is auditable and reversible.
//
// The split is not ceremony. The memory store is shared and its contents are
// model-generated, so a run that could edit it would give untrusted, partial
// evidence the power to destroy other runs' knowledge. Append-only observations
// remove that power while keeping the evidence.

// RecordObservation appends one run's view of one recalled entry.
//
// Idempotent per (run, entry): a retried run must not inflate its own evidence.
// Without the guard, a run that recalls the same entry in ten steps would look
// like ten independent confirmations of disuse, and the aggregate is the whole
// basis for archiving.
func (idx *SQLiteIndex) RecordObservation(ctx context.Context, obs MemoryObservation) error {
	if idx == nil || idx.db == nil {
		return fmt.Errorf("sqlite index: not open")
	}
	if strings.TrimSpace(obs.RunID) == "" || strings.TrimSpace(obs.EntryID) == "" {
		return fmt.Errorf("sqlite index: observation needs run_id and entry_id")
	}
	kind := obs.Kind
	if kind == "" {
		kind = ObservationKindMemory
	}
	at := obs.At
	if at.IsZero() {
		at = time.Now().UTC()
	}

	// The run's own view of an entry is one row: a repeat replaces the earlier
	// one, so "used at least once in this run" is what is recorded rather than
	// a count that grows with the number of recall calls.
	_, err := idx.db.ExecContext(ctx,
		`INSERT INTO memory_observations (run_id, entry_id, kind, used, usage_known, observed_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(run_id, entry_id) DO UPDATE SET
		   used = MAX(used, excluded.used),
		   usage_known = MAX(usage_known, excluded.usage_known),
		   observed_at = MAX(observed_at, excluded.observed_at)`,
		obs.RunID, obs.EntryID, string(kind), boolToInt(obs.Used), boolToInt(obs.UsageKnown), formatTime(at))
	if err != nil {
		return fmt.Errorf("sqlite index: record observation: %w", err)
	}
	return nil
}

// LoadObservations returns the observations for a kind, newest first.
//
// A limit of zero means "all", which is what the aggregate needs: a partial
// history would make "recalled twelve times, used zero" depend on how many rows
// happened to be read.
func (idx *SQLiteIndex) LoadObservations(ctx context.Context, kind ObservationKind, limit int) ([]MemoryObservation, error) {
	if idx == nil || idx.db == nil {
		return nil, fmt.Errorf("sqlite index: not open")
	}
	query := `SELECT run_id, entry_id, kind, used, usage_known, observed_at FROM memory_observations`
	args := []any{}
	if kind != "" {
		query += ` WHERE kind = ?`
		args = append(args, string(kind))
	}
	query += ` ORDER BY observed_at DESC`
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}

	rows, err := idx.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite index: load observations: %w", err)
	}
	defer rows.Close()

	var out []MemoryObservation
	for rows.Next() {
		var (
			obs        MemoryObservation
			kind       string
			used       int
			usageKnown int
			at         string
		)
		if err := rows.Scan(&obs.RunID, &obs.EntryID, &kind, &used, &usageKnown, &at); err != nil {
			return nil, fmt.Errorf("sqlite index: scan observation: %w", err)
		}
		obs.Kind = ObservationKind(kind)
		obs.Used = used != 0
		obs.UsageKnown = usageKnown != 0
		obs.At = parseIndexTime(at)
		out = append(out, obs)
	}
	return out, rows.Err()
}

// ArchiveEntry marks an entry as archived, with the reason.
//
// Archive rather than delete: the decision is made from an aggregate that can be
// wrong (a genuinely rare lesson may simply not have been needed yet), and an
// archived entry still exists to be recovered. Deleting is a separate, later,
// explicitly authorised step.
func (idx *SQLiteIndex) ArchiveEntry(ctx context.Context, entryID string, kind ObservationKind, reason string, at time.Time) error {
	if idx == nil || idx.db == nil {
		return fmt.Errorf("sqlite index: not open")
	}
	if strings.TrimSpace(entryID) == "" {
		return fmt.Errorf("sqlite index: archive needs an entry id")
	}
	if kind == "" {
		kind = ObservationKindMemory
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	// A re-archive of an already-archived entry keeps the FIRST archive time:
	// the retention period runs from when it left the pool, and refreshing it on
	// every prune run would mean nothing is ever deletable.
	_, err := idx.db.ExecContext(ctx,
		`INSERT INTO memory_archive (entry_id, kind, archived_at, reason, recovered_at)
		 VALUES (?, ?, ?, ?, NULL)
		 ON CONFLICT(entry_id) DO UPDATE SET reason = excluded.reason`,
		entryID, string(kind), formatTime(at), reason)
	if err != nil {
		return fmt.Errorf("sqlite index: archive entry: %w", err)
	}
	return nil
}

// RecoverEntry undoes an archive.
//
// The row is kept and marked with a recovery time rather than deleted, so the
// history of "this was archived once and brought back" survives — a memory that
// keeps being archived and recovered is telling you the threshold is wrong, and
// deleting the evidence would hide that.
func (idx *SQLiteIndex) RecoverEntry(ctx context.Context, entryID string, at time.Time) error {
	if idx == nil || idx.db == nil {
		return fmt.Errorf("sqlite index: not open")
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	res, err := idx.db.ExecContext(ctx,
		`UPDATE memory_archive SET recovered_at = ? WHERE entry_id = ? AND recovered_at IS NULL`,
		formatTime(at), entryID)
	if err != nil {
		return fmt.Errorf("sqlite index: recover entry: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("sqlite index: entry %q is not archived", entryID)
	}
	return nil
}

// ArchivedEntries returns the entries currently out of the recall pool.
//
// "Currently" means archived with no recovery: a recovered entry is back in the
// pool and must not be filtered out, which is easy to get wrong if this is
// written as "any row in the archive table".
func (idx *SQLiteIndex) ArchivedEntries(ctx context.Context, kind ObservationKind) (map[string]time.Time, error) {
	if idx == nil || idx.db == nil {
		return nil, fmt.Errorf("sqlite index: not open")
	}
	query := `SELECT entry_id, archived_at FROM memory_archive WHERE recovered_at IS NULL`
	args := []any{}
	if kind != "" {
		query += ` AND kind = ?`
		args = append(args, string(kind))
	}
	rows, err := idx.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite index: list archived: %w", err)
	}
	defer rows.Close()

	out := map[string]time.Time{}
	for rows.Next() {
		var id, at string
		if err := rows.Scan(&id, &at); err != nil {
			return nil, fmt.Errorf("sqlite index: scan archive: %w", err)
		}
		out[id] = parseIndexTime(at)
	}
	return out, rows.Err()
}

// ArchiveRecord is one archived entry as a reviewer sees it.
type ArchiveRecord struct {
	EntryID    string
	Kind       ObservationKind
	ArchivedAt time.Time
	Reason     string
	Recovered  bool
}

// ListArchive returns the archive with reasons, oldest first.
//
// It includes recovered entries when includeRecovered is set, because the
// question "has this been archived and brought back before?" is exactly what
// reveals a threshold set too aggressively.
func (idx *SQLiteIndex) ListArchive(ctx context.Context, kind ObservationKind, includeRecovered bool) ([]ArchiveRecord, error) {
	if idx == nil || idx.db == nil {
		return nil, fmt.Errorf("sqlite index: not open")
	}
	query := `SELECT entry_id, kind, archived_at, COALESCE(reason, ''), COALESCE(recovered_at, '') FROM memory_archive`
	conds := []string{}
	args := []any{}
	if kind != "" {
		conds = append(conds, `kind = ?`)
		args = append(args, string(kind))
	}
	if !includeRecovered {
		conds = append(conds, `recovered_at IS NULL`)
	}
	if len(conds) > 0 {
		query += ` WHERE ` + strings.Join(conds, " AND ")
	}
	query += ` ORDER BY archived_at ASC`

	rows, err := idx.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite index: list archive: %w", err)
	}
	defer rows.Close()

	var out []ArchiveRecord
	for rows.Next() {
		var (
			rec       ArchiveRecord
			kindStr   string
			atStr     string
			recoverAt string
		)
		if err := rows.Scan(&rec.EntryID, &kindStr, &atStr, &rec.Reason, &recoverAt); err != nil {
			return nil, fmt.Errorf("sqlite index: scan archive record: %w", err)
		}
		rec.Kind = ObservationKind(kindStr)
		rec.ArchivedAt = parseIndexTime(atStr)
		rec.Recovered = recoverAt != ""
		out = append(out, rec)
	}
	return out, rows.Err()
}

// ForgetEntry removes an archived entry's archive row, which is what makes a
// deletion stick: with the row gone and the entry deleted, nothing re-adds it.
func (idx *SQLiteIndex) ForgetEntry(ctx context.Context, entryID string) error {
	if idx == nil || idx.db == nil {
		return fmt.Errorf("sqlite index: not open")
	}
	if _, err := idx.db.ExecContext(ctx, `DELETE FROM memory_archive WHERE entry_id = ?`, entryID); err != nil {
		return fmt.Errorf("sqlite index: forget entry: %w", err)
	}
	return nil
}

// DeleteLesson removes a lesson from the cross-run table.
//
// Lessons live in this index rather than in the agent's memory store, so the
// control plane can delete them directly — which is the point of keeping the two
// stores separate. The agent could never do this, and should not be able to: it
// only ever read them.
func (idx *SQLiteIndex) DeleteLesson(ctx context.Context, lessonID string) error {
	if idx == nil || idx.db == nil {
		return fmt.Errorf("sqlite index: not open")
	}
	if strings.TrimSpace(lessonID) == "" {
		return fmt.Errorf("sqlite index: delete lesson needs an id")
	}
	if _, err := idx.db.ExecContext(ctx, `DELETE FROM lessons WHERE id = ?`, lessonID); err != nil {
		return fmt.Errorf("sqlite index: delete lesson: %w", err)
	}
	return nil
}

// CountObservations reports how many observations exist, for a one-line summary
// before a prune run does anything.
func (idx *SQLiteIndex) CountObservations(ctx context.Context, kind ObservationKind) (int, error) {
	if idx == nil || idx.db == nil {
		return 0, fmt.Errorf("sqlite index: not open")
	}
	query := `SELECT COUNT(*) FROM memory_observations`
	args := []any{}
	if kind != "" {
		query += ` WHERE kind = ?`
		args = append(args, string(kind))
	}
	var n int
	if err := idx.db.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("sqlite index: count observations: %w", err)
	}
	return n, nil
}

// ObservationKindMemory and ObservationKindLesson mirror the core vocabulary so
// this package does not need to import the agent's core just for two strings.
const (
	ObservationKindMemory = "memory"
	ObservationKindLesson = "lesson"
)

// ObservationKind is the storage-side spelling of the observation kind.
type ObservationKind = string

// MemoryObservation is the storage-side shape of an appended observation.
type MemoryObservation struct {
	RunID   string
	EntryID string
	Kind    ObservationKind
	// Used is meaningful only when UsageKnown is set: a run that could not
	// report whether it used the entry has evidence about the recall, not about
	// the entry.
	Used       bool
	UsageKnown bool
	At         time.Time
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
