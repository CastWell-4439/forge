package checkpoint

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/castwell/forge/internal/agent/core"
)

// PGCheckpointStore persists checkpoints in the agent_checkpoints table created
// by deploy/migrations/004_checkpoint.sql.
//
// That table reserves a metadata JSONB column for "extra state (tool results,
// etc.)", which is where the rest of the recovery context goes: the side-effect
// ledger, the retry count and the approval state. Keeping them there means the
// schema did not have to change.
type PGCheckpointStore struct {
	pool *pgxpool.Pool
}

// checkpointMetadata is the shape stored in the metadata column.
type checkpointMetadata struct {
	ToolCalls     []core.ToolCallRecord `json:"tool_calls,omitempty"`
	RetryCount    int                   `json:"retry_count,omitempty"`
	ApprovalState string                `json:"approval_state,omitempty"`
}

// NewPGCheckpointStore connects to PostgreSQL using the given DSN.
func NewPGCheckpointStore(ctx context.Context, dsn string) (*PGCheckpointStore, error) {
	if dsn == "" {
		return nil, fmt.Errorf("checkpoint: PostgreSQL DSN is required")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("checkpoint: parse DSN: %w", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("checkpoint: connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("checkpoint: ping: %w", err)
	}
	return &PGCheckpointStore{pool: pool}, nil
}

// Close releases the connection pool.
func (s *PGCheckpointStore) Close() {
	if s != nil && s.pool != nil {
		s.pool.Close()
	}
}

const checkpointColumns = `id, session_id, step_index, messages, metadata, created_at`

// Save upserts a checkpoint. The table's unique key is (session_id, step_index),
// so re-saving the same step replaces it rather than accumulating rows.
func (s *PGCheckpointStore) Save(ctx context.Context, cp *core.Checkpoint) error {
	if cp == nil {
		return fmt.Errorf("checkpoint is nil")
	}
	if cp.ID == "" {
		return fmt.Errorf("checkpoint ID is required")
	}
	if cp.SessionID == "" {
		return fmt.Errorf("checkpoint session_id is required")
	}

	messages, err := json.Marshal(cp.Messages)
	if err != nil {
		return fmt.Errorf("marshal checkpoint messages: %w", err)
	}
	metadata, err := json.Marshal(checkpointMetadata{
		ToolCalls:     cp.ToolCalls,
		RetryCount:    cp.RetryCount,
		ApprovalState: cp.ApprovalState,
	})
	if err != nil {
		return fmt.Errorf("marshal checkpoint metadata: %w", err)
	}

	_, err = s.pool.Exec(ctx, `
		INSERT INTO agent_checkpoints (id, session_id, step_index, messages, metadata, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (session_id, step_index) DO UPDATE SET
			id         = EXCLUDED.id,
			messages   = EXCLUDED.messages,
			metadata   = EXCLUDED.metadata,
			created_at = EXCLUDED.created_at`,
		cp.ID, cp.SessionID, cp.StepIndex, messages, metadata, cp.CreatedAt)
	if err != nil {
		return fmt.Errorf("save checkpoint %s: %w", cp.ID, err)
	}
	return nil
}

// Load returns a checkpoint by ID.
func (s *PGCheckpointStore) Load(ctx context.Context, id string) (*core.Checkpoint, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+checkpointColumns+` FROM agent_checkpoints WHERE id = $1`, id)
	cp, err := scanCheckpoint(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("checkpoint %q: %w", id, core.ErrNoCheckpoint)
		}
		return nil, fmt.Errorf("load checkpoint %s: %w", id, err)
	}
	return cp, nil
}

// Latest returns the checkpoint with the highest step index for a session.
func (s *PGCheckpointStore) Latest(ctx context.Context, sessionID string) (*core.Checkpoint, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+checkpointColumns+` FROM agent_checkpoints
		 WHERE session_id = $1 ORDER BY step_index DESC LIMIT 1`, sessionID)
	cp, err := scanCheckpoint(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("session %q: %w", sessionID, core.ErrNoCheckpoint)
		}
		return nil, fmt.Errorf("latest checkpoint for %s: %w", sessionID, err)
	}
	return cp, nil
}

// scanRow is the subset of pgx.Row this package needs.
type scanRow interface {
	Scan(dest ...any) error
}

func scanCheckpoint(row scanRow) (*core.Checkpoint, error) {
	var (
		cp        core.Checkpoint
		messages  []byte
		metadata  []byte
		createdAt time.Time
	)
	if err := row.Scan(&cp.ID, &cp.SessionID, &cp.StepIndex, &messages, &metadata, &createdAt); err != nil {
		return nil, err
	}
	cp.CreatedAt = createdAt

	if len(messages) > 0 {
		if err := json.Unmarshal(messages, &cp.Messages); err != nil {
			return nil, fmt.Errorf("parse checkpoint messages: %w", err)
		}
	}
	if len(metadata) > 0 {
		var meta checkpointMetadata
		if err := json.Unmarshal(metadata, &meta); err != nil {
			return nil, fmt.Errorf("parse checkpoint metadata: %w", err)
		}
		cp.ToolCalls = meta.ToolCalls
		cp.RetryCount = meta.RetryCount
		cp.ApprovalState = meta.ApprovalState
	}
	return &cp, nil
}

var _ core.CheckpointStore = (*PGCheckpointStore)(nil)
