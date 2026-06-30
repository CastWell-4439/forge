package storage

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/castwell/forge/internal/forgex/model"
	_ "modernc.org/sqlite"
)

// IndexedRun is a compact row used by CLI listing and future dashboards.
type IndexedRun struct {
	ID              string
	TaskID          string
	Name            string
	Status          string
	StartedAt       time.Time
	EndedAt         time.Time
	ErrorCount      int
	StopAction      string
	EvalStatus      string
	LastCategory    string
	LastFingerprint string
}

// SQLiteIndex stores searchable summaries for local ForgeX run artifacts.
type SQLiteIndex struct {
	db *sql.DB
}

// OpenSQLiteIndex opens or creates an index database.
func OpenSQLiteIndex(path string) (*SQLiteIndex, error) {
	if path == "" {
		return nil, fmt.Errorf("sqlite index path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	idx := &SQLiteIndex{db: db}
	if err := idx.Init(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return idx, nil
}

// Close closes the underlying database connection.
func (idx *SQLiteIndex) Close() error {
	if idx == nil || idx.db == nil {
		return nil
	}
	return idx.db.Close()
}

// Init creates tables used by the local run index.
func (idx *SQLiteIndex) Init(ctx context.Context) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS runs (
			id TEXT PRIMARY KEY,
			task_id TEXT NOT NULL,
			name TEXT NOT NULL,
			status TEXT NOT NULL,
			started_at TEXT NOT NULL,
			ended_at TEXT,
			error_count INTEGER NOT NULL DEFAULT 0,
			stop_action TEXT,
			eval_status TEXT,
			last_category TEXT,
			last_fingerprint TEXT,
			indexed_at TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS errors (
			id TEXT PRIMARY KEY,
			run_id TEXT NOT NULL,
			category TEXT,
			severity TEXT,
			fingerprint TEXT,
			message TEXT,
			operation TEXT,
			timestamp TEXT,
			FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE CASCADE
		);`,
		`CREATE TABLE IF NOT EXISTS stop_decisions (
			id TEXT PRIMARY KEY,
			run_id TEXT NOT NULL,
			error_id TEXT,
			action TEXT NOT NULL,
			reason TEXT,
			decided_at TEXT,
			FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE CASCADE
		);`,
		`CREATE TABLE IF NOT EXISTS eval_results (
			id TEXT PRIMARY KEY,
			run_id TEXT NOT NULL,
			suite_id TEXT NOT NULL,
			status TEXT NOT NULL,
			created_at TEXT NOT NULL,
			FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE CASCADE
		);`,
		`CREATE INDEX IF NOT EXISTS idx_runs_status ON runs(status);`,
		`CREATE INDEX IF NOT EXISTS idx_errors_run_id ON errors(run_id);`,
		`CREATE INDEX IF NOT EXISTS idx_errors_fingerprint ON errors(fingerprint);`,
	}
	for _, stmt := range stmts {
		if _, err := idx.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

// IndexRunDir reads a .forgex/runs/<run_id> directory and upserts summary rows.
func (idx *SQLiteIndex) IndexRunDir(ctx context.Context, runDir string) error {
	artifacts, err := loadIndexArtifacts(runDir)
	if err != nil {
		return err
	}
	return idx.IndexArtifacts(ctx, artifacts)
}

// IndexArtifacts upserts one run and its child rows.
func (idx *SQLiteIndex) IndexArtifacts(ctx context.Context, artifacts indexArtifacts) error {
	tx, err := idx.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	run := artifacts.Run
	lastCategory, lastFingerprint := lastErrorSummary(artifacts.Errors)
	stopAction := ""
	if len(artifacts.StopDecisions) > 0 {
		stopAction = string(artifacts.StopDecisions[len(artifacts.StopDecisions)-1].Action)
	}
	evalStatus := ""
	if artifacts.EvalResult.ID != "" {
		evalStatus = string(artifacts.EvalResult.Status)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO runs (id, task_id, name, status, started_at, ended_at, error_count, stop_action, eval_status, last_category, last_fingerprint, indexed_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
	task_id=excluded.task_id,
	name=excluded.name,
	status=excluded.status,
	started_at=excluded.started_at,
	ended_at=excluded.ended_at,
	error_count=excluded.error_count,
	stop_action=excluded.stop_action,
	eval_status=excluded.eval_status,
	last_category=excluded.last_category,
	last_fingerprint=excluded.last_fingerprint,
	indexed_at=excluded.indexed_at`,
		run.ID, run.TaskID, run.Name, string(run.Status), formatTime(run.StartedAt), formatOptionalTime(run.EndedAt),
		len(artifacts.Errors), stopAction, evalStatus, lastCategory, lastFingerprint, formatTime(time.Now().UTC()))
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM errors WHERE run_id = ?`, run.ID); err != nil {
		return err
	}
	for _, envelope := range artifacts.Errors {
		if _, err := tx.ExecContext(ctx, `INSERT INTO errors (id, run_id, category, severity, fingerprint, message, operation, timestamp) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			envelope.ID, envelope.RunID, envelope.Category, envelope.Severity, envelope.Fingerprint, envelope.Message, envelope.Operation, formatTime(envelope.Timestamp)); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM stop_decisions WHERE run_id = ?`, run.ID); err != nil {
		return err
	}
	for _, decision := range artifacts.StopDecisions {
		if _, err := tx.ExecContext(ctx, `INSERT INTO stop_decisions (id, run_id, error_id, action, reason, decided_at) VALUES (?, ?, ?, ?, ?, ?)`,
			decision.ID, decision.RunID, decision.ErrorID, string(decision.Action), decision.Reason, formatTime(decision.DecidedAt)); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM eval_results WHERE run_id = ?`, run.ID); err != nil {
		return err
	}
	if artifacts.EvalResult.ID != "" {
		if _, err := tx.ExecContext(ctx, `INSERT INTO eval_results (id, run_id, suite_id, status, created_at) VALUES (?, ?, ?, ?, ?)`,
			artifacts.EvalResult.ID, artifacts.EvalResult.RunID, artifacts.EvalResult.SuiteID, string(artifacts.EvalResult.Status), formatTime(artifacts.EvalResult.CreatedAt)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListRuns returns indexed runs in newest-first order.
func (idx *SQLiteIndex) ListRuns(ctx context.Context, limit int) ([]IndexedRun, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := idx.db.QueryContext(ctx, `SELECT id, task_id, name, status, started_at, COALESCE(ended_at, ''), error_count, COALESCE(stop_action, ''), COALESCE(eval_status, ''), COALESCE(last_category, ''), COALESCE(last_fingerprint, '') FROM runs ORDER BY started_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var runs []IndexedRun
	for rows.Next() {
		var run IndexedRun
		var startedAt, endedAt string
		if err := rows.Scan(&run.ID, &run.TaskID, &run.Name, &run.Status, &startedAt, &endedAt, &run.ErrorCount, &run.StopAction, &run.EvalStatus, &run.LastCategory, &run.LastFingerprint); err != nil {
			return nil, err
		}
		run.StartedAt = parseIndexTime(startedAt)
		run.EndedAt = parseIndexTime(endedAt)
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

type indexArtifacts struct {
	Run           model.Run
	Errors        []model.ErrorEnvelope
	StopDecisions []model.StopDecision
	EvalResult    model.EvalResult
}

func loadIndexArtifacts(runDir string) (indexArtifacts, error) {
	var artifacts indexArtifacts
	if err := readIndexJSON(filepath.Join(runDir, "run.json"), &artifacts.Run); err != nil {
		return indexArtifacts{}, err
	}
	if err := readIndexJSONL(filepath.Join(runDir, "errors.jsonl"), &artifacts.Errors); err != nil {
		return indexArtifacts{}, err
	}
	if err := readIndexJSONL(filepath.Join(runDir, "stop_decisions.jsonl"), &artifacts.StopDecisions); err != nil {
		return indexArtifacts{}, err
	}
	evalPath := filepath.Join(runDir, "eval_result.json")
	if _, err := os.Stat(evalPath); err == nil {
		if err := readIndexJSON(evalPath, &artifacts.EvalResult); err != nil {
			return indexArtifacts{}, err
		}
	} else if !os.IsNotExist(err) {
		return indexArtifacts{}, err
	}
	return artifacts, nil
}

func readIndexJSON(path string, target any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

func readIndexJSONL[T any](path string, target *[]T) error {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var item T
		if err := json.Unmarshal(scanner.Bytes(), &item); err != nil {
			return err
		}
		*target = append(*target, item)
	}
	return scanner.Err()
}

func lastErrorSummary(errors []model.ErrorEnvelope) (category string, fingerprint string) {
	if len(errors) == 0 {
		return "", ""
	}
	last := errors[len(errors)-1]
	return last.Category, last.Fingerprint
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func formatOptionalTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return formatTime(t)
}

func parseIndexTime(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	return parsed
}
