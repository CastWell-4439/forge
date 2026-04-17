package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGStorage implements Storage using PostgreSQL with pgx/v5.
type PGStorage struct {
	pool *pgxpool.Pool
}

// NewPGStorage creates a new PostgreSQL storage with a connection pool.
func NewPGStorage(ctx context.Context, dsn string) (*PGStorage, error) {
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse postgres DSN: %w", err)
	}
	config.MaxConns = 20
	config.MinConns = 2
	config.MaxConnLifetime = 30 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("create postgres pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}

	return &PGStorage{pool: pool}, nil
}

// RunMigrations executes the schema migration SQL against the database.
func (s *PGStorage) RunMigrations(ctx context.Context, sql string) error {
	_, err := s.pool.Exec(ctx, sql)
	if err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}
	return nil
}

// SaveWorkflowDefinition inserts or updates a workflow definition.
func (s *PGStorage) SaveWorkflowDefinition(ctx context.Context, def *WorkflowDefinition) error {
	err := s.pool.QueryRow(ctx, `
		INSERT INTO workflow_definitions (name, version, dag_yaml, created_at, updated_at)
		VALUES ($1, $2, $3, NOW(), NOW())
		ON CONFLICT (name, version) DO UPDATE SET dag_yaml = $3, updated_at = NOW()
		RETURNING id, created_at, updated_at
	`, def.Name, def.Version, def.DagYAML).Scan(&def.ID, &def.CreatedAt, &def.UpdatedAt)
	if err != nil {
		return fmt.Errorf("save workflow definition %s v%d: %w", def.Name, def.Version, err)
	}
	return nil
}

// GetWorkflowDefinition retrieves a workflow definition by name and version.
func (s *PGStorage) GetWorkflowDefinition(ctx context.Context, name string, version int) (*WorkflowDefinition, error) {
	def := &WorkflowDefinition{}
	err := s.pool.QueryRow(ctx, `
		SELECT id, name, version, dag_yaml, created_at, updated_at
		FROM workflow_definitions WHERE name = $1 AND version = $2
	`, name, version).Scan(&def.ID, &def.Name, &def.Version, &def.DagYAML, &def.CreatedAt, &def.UpdatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("workflow definition %s v%d not found", name, version)
		}
		return nil, fmt.Errorf("get workflow definition %s v%d: %w", name, version, err)
	}
	return def, nil
}

// SaveWorkflow inserts a new workflow instance.
func (s *PGStorage) SaveWorkflow(ctx context.Context, wf *Workflow) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO workflow_instances (id, def_id, name, status, input, output, error_msg, started_at, finished_at, created_at, timeout_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	`, wf.ID, wf.DefID, wf.Name, wf.Status, nullableJSON(wf.Input), nullableJSON(wf.Output),
		wf.ErrorMsg, wf.StartedAt, wf.FinishedAt, wf.CreatedAt, wf.TimeoutAt)
	if err != nil {
		return fmt.Errorf("save workflow %s: %w", wf.ID, err)
	}
	return nil
}

// GetWorkflow retrieves a workflow instance by ID.
func (s *PGStorage) GetWorkflow(ctx context.Context, workflowID string) (*Workflow, error) {
	wf := &Workflow{}
	err := s.pool.QueryRow(ctx, `
		SELECT id, def_id, name, status, input, output, error_msg, started_at, finished_at, created_at, timeout_at
		FROM workflow_instances WHERE id = $1
	`, workflowID).Scan(
		&wf.ID, &wf.DefID, &wf.Name, &wf.Status, &wf.Input, &wf.Output,
		&wf.ErrorMsg, &wf.StartedAt, &wf.FinishedAt, &wf.CreatedAt, &wf.TimeoutAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("workflow %s not found", workflowID)
		}
		return nil, fmt.Errorf("get workflow %s: %w", workflowID, err)
	}
	return wf, nil
}

// ListWorkflows returns workflow instances filtered by status.
func (s *PGStorage) ListWorkflows(ctx context.Context, status WorkflowStatus, limit int, offset int) ([]*Workflow, error) {
	var rows pgx.Rows
	var err error
	if status == "" {
		rows, err = s.pool.Query(ctx, `
			SELECT id, def_id, name, status, input, output, error_msg, started_at, finished_at, created_at, timeout_at
			FROM workflow_instances ORDER BY created_at DESC LIMIT $1 OFFSET $2
		`, limit, offset)
	} else {
		rows, err = s.pool.Query(ctx, `
			SELECT id, def_id, name, status, input, output, error_msg, started_at, finished_at, created_at, timeout_at
			FROM workflow_instances WHERE status = $1 ORDER BY created_at DESC LIMIT $2 OFFSET $3
		`, status, limit, offset)
	}
	if err != nil {
		return nil, fmt.Errorf("list workflows: %w", err)
	}
	defer rows.Close()

	var workflows []*Workflow
	for rows.Next() {
		wf := &Workflow{}
		if err := rows.Scan(
			&wf.ID, &wf.DefID, &wf.Name, &wf.Status, &wf.Input, &wf.Output,
			&wf.ErrorMsg, &wf.StartedAt, &wf.FinishedAt, &wf.CreatedAt, &wf.TimeoutAt,
		); err != nil {
			return nil, fmt.Errorf("scan workflow: %w", err)
		}
		workflows = append(workflows, wf)
	}
	return workflows, rows.Err()
}

// UpdateWorkflowStatus updates the status of a workflow instance.
func (s *PGStorage) UpdateWorkflowStatus(ctx context.Context, workflowID string, status WorkflowStatus) error {
	var finishedAt *time.Time
	var startedAt *time.Time
	now := time.Now()
	switch status {
	case WorkflowStatusRunning:
		startedAt = &now
	case WorkflowStatusCompleted, WorkflowStatusFailed, WorkflowStatusCancelled:
		finishedAt = &now
	}

	tag, err := s.pool.Exec(ctx, `
		UPDATE workflow_instances
		SET status = $1,
		    started_at = COALESCE($2, started_at),
		    finished_at = COALESCE($3, finished_at)
		WHERE id = $4
	`, status, startedAt, finishedAt, workflowID)
	if err != nil {
		return fmt.Errorf("update workflow %s status: %w", workflowID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("workflow %s not found", workflowID)
	}
	return nil
}

// SaveTask inserts a new task instance.
func (s *PGStorage) SaveTask(ctx context.Context, task *Task) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO task_instances (id, workflow_id, task_name, handler, status, worker_id, input, output,
			error_msg, attempt, max_attempts, scheduled_at, started_at, finished_at, timeout_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
	`, task.ID, task.WorkflowID, task.TaskName, task.Handler, task.Status, task.WorkerID,
		nullableJSON(task.Input), nullableJSON(task.Output), task.ErrorMsg,
		task.Attempt, task.MaxAttempts, task.ScheduledAt, task.StartedAt, task.FinishedAt,
		task.TimeoutAt, task.CreatedAt)
	if err != nil {
		return fmt.Errorf("save task %s: %w", task.ID, err)
	}
	return nil
}

// GetTask retrieves a task instance by ID.
func (s *PGStorage) GetTask(ctx context.Context, taskID string) (*Task, error) {
	task := &Task{}
	err := s.pool.QueryRow(ctx, `
		SELECT id, workflow_id, task_name, handler, status, worker_id, input, output,
			error_msg, attempt, max_attempts, scheduled_at, started_at, finished_at, timeout_at, created_at
		FROM task_instances WHERE id = $1
	`, taskID).Scan(
		&task.ID, &task.WorkflowID, &task.TaskName, &task.Handler, &task.Status, &task.WorkerID,
		&task.Input, &task.Output, &task.ErrorMsg, &task.Attempt, &task.MaxAttempts,
		&task.ScheduledAt, &task.StartedAt, &task.FinishedAt, &task.TimeoutAt, &task.CreatedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("task %s not found", taskID)
		}
		return nil, fmt.Errorf("get task %s: %w", taskID, err)
	}
	return task, nil
}

// ListTasksByWorkflow retrieves all tasks for a given workflow.
func (s *PGStorage) ListTasksByWorkflow(ctx context.Context, workflowID string) ([]*Task, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, workflow_id, task_name, handler, status, worker_id, input, output,
			error_msg, attempt, max_attempts, scheduled_at, started_at, finished_at, timeout_at, created_at
		FROM task_instances WHERE workflow_id = $1 ORDER BY created_at
	`, workflowID)
	if err != nil {
		return nil, fmt.Errorf("list tasks for workflow %s: %w", workflowID, err)
	}
	defer rows.Close()

	var tasks []*Task
	for rows.Next() {
		task := &Task{}
		if err := rows.Scan(
			&task.ID, &task.WorkflowID, &task.TaskName, &task.Handler, &task.Status, &task.WorkerID,
			&task.Input, &task.Output, &task.ErrorMsg, &task.Attempt, &task.MaxAttempts,
			&task.ScheduledAt, &task.StartedAt, &task.FinishedAt, &task.TimeoutAt, &task.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan task: %w", err)
		}
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

// ClaimTask atomically claims a ready task for a worker using FOR UPDATE SKIP LOCKED.
// This is the exact pattern from tech spec section 7.3.
func (s *PGStorage) ClaimTask(ctx context.Context, workerID string, handlers []string) (*Task, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin claim task tx: %w", err)
	}
	defer tx.Rollback(ctx)

	task := &Task{}
	err = tx.QueryRow(ctx, `
		UPDATE task_instances
		SET status = 'SCHEDULED', worker_id = $1, scheduled_at = NOW()
		WHERE id = (
			SELECT id FROM task_instances
			WHERE status = 'READY' AND handler = ANY($2)
			ORDER BY created_at
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		RETURNING id, workflow_id, task_name, handler, input, status, worker_id,
			attempt, max_attempts, scheduled_at, created_at
	`, workerID, handlers).Scan(
		&task.ID, &task.WorkflowID, &task.TaskName, &task.Handler, &task.Input,
		&task.Status, &task.WorkerID, &task.Attempt, &task.MaxAttempts,
		&task.ScheduledAt, &task.CreatedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil // no task available
		}
		return nil, fmt.Errorf("claim task for worker %s: %w", workerID, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit claim task: %w", err)
	}
	return task, nil
}

// UpdateTaskStatus updates the status of a task instance.
func (s *PGStorage) UpdateTaskStatus(ctx context.Context, taskID string, status TaskStatus) error {
	now := time.Now()
	var startedAt, finishedAt *time.Time
	switch status {
	case TaskStatusRunning:
		startedAt = &now
	case TaskStatusCompleted, TaskStatusFailed, TaskStatusSkipped:
		finishedAt = &now
	}

	tag, err := s.pool.Exec(ctx, `
		UPDATE task_instances
		SET status = $1,
		    started_at = COALESCE($2, started_at),
		    finished_at = COALESCE($3, finished_at)
		WHERE id = $4
	`, status, startedAt, finishedAt, taskID)
	if err != nil {
		return fmt.Errorf("update task %s status: %w", taskID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("task %s not found", taskID)
	}
	return nil
}

// CompleteTask marks a task as COMPLETED and persists its output.
// Uses a WHERE guard against terminal states (blacklist) for idempotency.
// A 0-row result is logged but not returned as an error.
func (s *PGStorage) CompleteTask(ctx context.Context, taskID string, output json.RawMessage) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE task_instances
		SET status = $1,
		    output = $2,
		    finished_at = NOW()
		WHERE id = $3
		  AND status NOT IN ($4, $5, $6)
	`, TaskStatusCompleted, nullableJSON(output), taskID,
		TaskStatusCompleted, TaskStatusFailed, TaskStatusSkipped)
	if err != nil {
		return fmt.Errorf("complete task %s: %w", taskID, err)
	}
	if tag.RowsAffected() == 0 {
		log.Printf("WARN: CompleteTask(%s) affected 0 rows (already terminal or not found)", taskID)
	}
	return nil
}

// FailTask marks a task as FAILED and persists its error message.
// Same idempotent semantics as CompleteTask.
func (s *PGStorage) FailTask(ctx context.Context, taskID string, errorMsg string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE task_instances
		SET status = $1,
		    error_msg = $2,
		    finished_at = NOW()
		WHERE id = $3
		  AND status NOT IN ($4, $5, $6)
	`, TaskStatusFailed, errorMsg, taskID,
		TaskStatusCompleted, TaskStatusFailed, TaskStatusSkipped)
	if err != nil {
		return fmt.Errorf("fail task %s: %w", taskID, err)
	}
	if tag.RowsAffected() == 0 {
		log.Printf("WARN: FailTask(%s) affected 0 rows (already terminal or not found)", taskID)
	}
	return nil
}

// SaveEvent inserts a new event into the event log.
func (s *PGStorage) SaveEvent(ctx context.Context, event *Event) error {
	err := s.pool.QueryRow(ctx, `
		INSERT INTO events (workflow_id, task_id, event_type, payload, sequence_num, created_at)
		VALUES ($1, $2, $3, $4, $5, NOW())
		RETURNING id, created_at
	`, event.WorkflowID, nullString(event.TaskID), event.Type, nullableJSON(event.Payload),
		event.SequenceNum).Scan(&event.ID, &event.Timestamp)
	if err != nil {
		return fmt.Errorf("save event for workflow %s: %w", event.WorkflowID, err)
	}
	return nil
}

// GetWorkflowHistory retrieves all events for a workflow ordered by sequence number.
func (s *PGStorage) GetWorkflowHistory(ctx context.Context, workflowID string) ([]*Event, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, workflow_id, task_id, event_type, payload, sequence_num, created_at
		FROM events WHERE workflow_id = $1 ORDER BY sequence_num
	`, workflowID)
	if err != nil {
		return nil, fmt.Errorf("get workflow %s history: %w", workflowID, err)
	}
	defer rows.Close()

	var events []*Event
	for rows.Next() {
		evt := &Event{}
		var taskID *string
		if err := rows.Scan(
			&evt.ID, &evt.WorkflowID, &taskID, &evt.Type, &evt.Payload,
			&evt.SequenceNum, &evt.Timestamp,
		); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		if taskID != nil {
			evt.TaskID = *taskID
		}
		events = append(events, evt)
	}
	return events, rows.Err()
}

// Close shuts down the connection pool.
func (s *PGStorage) Close() error {
	s.pool.Close()
	return nil
}

// nullableJSON returns nil if the JSON is empty or null.
func nullableJSON(data json.RawMessage) interface{} {
	if len(data) == 0 {
		return nil
	}
	return data
}

// nullString returns nil for empty strings (used for nullable VARCHAR columns).
func nullString(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}
