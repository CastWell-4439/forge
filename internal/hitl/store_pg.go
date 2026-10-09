package hitl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGStore persists HITL requests in the `hitl_requests` table (migration 006).
//
// It exists because the in-memory Manager forgets everything on restart, and a
// request that disappears is worse than one that is late: the task waiting on it
// stays parked with nothing left to answer. The table is the one shared point
// between the worker that files a request and the coordinator that acts on the
// answer, so both sides read the same facts.
//
// It takes a pool rather than a storage.Storage: this package has no business
// knowing about workflows and tasks, only about the pool needed to reach its own
// table.
type PGStore struct {
	pool *pgxpool.Pool
}

// NewPGStore builds a store over an existing pool.
func NewPGStore(pool *pgxpool.Pool) *PGStore {
	return &PGStore{pool: pool}
}

// Save inserts a new request. Re-saving the same ID is an upsert, so a retry
// after a partial failure converges instead of failing on the primary key.
func (s *PGStore) Save(ctx context.Context, req *Request) error {
	options, err := json.Marshal(orEmpty(req.Options))
	if err != nil {
		return fmt.Errorf("hitl store: marshal options: %w", err)
	}

	if _, err := s.pool.Exec(ctx, `
		INSERT INTO hitl_requests
			(id, workflow_id, task_id, message, options, status, response, created_at, timeout_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NOW())
		ON CONFLICT (id) DO UPDATE SET
			status     = EXCLUDED.status,
			response   = EXCLUDED.response,
			timeout_at = EXCLUDED.timeout_at,
			updated_at = NOW()
	`, req.ID, req.WorkflowID, req.TaskID, req.Message, options,
		string(orPending(req.Status)), nullResponse(req.Response),
		req.CreatedAt, req.TimeoutAt); err != nil {
		return fmt.Errorf("hitl store: save request %s: %w", req.ID, err)
	}
	return nil
}

// Get loads one request by ID.
func (s *PGStore) Get(ctx context.Context, id string) (*Request, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, workflow_id, task_id, message, options, status, response,
		       created_at, timeout_at
		FROM hitl_requests WHERE id = $1
	`, id)

	req, err := scanRequest(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("hitl: request %q not found", id)
		}
		return nil, fmt.Errorf("hitl store: get request %s: %w", id, err)
	}
	return req, nil
}

// ListPending returns every unanswered request, oldest first.
//
// Oldest first because a queue of approvals is worked through in the order it
// arrived: the request that has been waiting longest is the one most likely to
// be blocking someone.
func (s *PGStore) ListPending(ctx context.Context) ([]*Request, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, workflow_id, task_id, message, options, status, response,
		       created_at, timeout_at
		FROM hitl_requests WHERE status = $1
		ORDER BY created_at
	`, string(StatusPending))
	if err != nil {
		return nil, fmt.Errorf("hitl store: list pending: %w", err)
	}
	defer rows.Close()

	var out []*Request
	for rows.Next() {
		req, err := scanRequest(rows)
		if err != nil {
			return nil, fmt.Errorf("hitl store: scan pending: %w", err)
		}
		out = append(out, req)
	}
	return out, rows.Err()
}

// Update writes a request's current state back.
//
// A missing row is an error here, unlike the no-op writes elsewhere in Forge:
// losing an update would leave a request pending forever while its task sits
// parked, so the caller has to hear about it.
func (s *PGStore) Update(ctx context.Context, req *Request) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE hitl_requests
		SET status = $1, response = $2, updated_at = NOW()
		WHERE id = $3
	`, string(req.Status), nullResponse(req.Response), req.ID)
	if err != nil {
		return fmt.Errorf("hitl store: update request %s: %w", req.ID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("hitl store: update request %s: no such request", req.ID)
	}
	return nil
}

// ListExpired returns pending requests past their deadline.
//
// It reads the table rather than the manager's memory because the process that
// filed a request may not be the one sweeping: in a two-process deployment the
// worker creates requests and the coordinator times them out.
func (s *PGStore) ListExpired(ctx context.Context, now time.Time) ([]*Request, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, workflow_id, task_id, message, options, status, response,
		       created_at, timeout_at
		FROM hitl_requests
		WHERE status = $1 AND timeout_at <= $2
		ORDER BY timeout_at
	`, string(StatusPending), now)
	if err != nil {
		return nil, fmt.Errorf("hitl store: list expired: %w", err)
	}
	defer rows.Close()

	var out []*Request
	for rows.Next() {
		req, err := scanRequest(rows)
		if err != nil {
			return nil, fmt.Errorf("hitl store: scan expired: %w", err)
		}
		out = append(out, req)
	}
	return out, rows.Err()
}

// rowScanner is the shape shared by pgx.Row and pgx.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanRequest(row rowScanner) (*Request, error) {
	var (
		req         Request
		status      string
		optionsRaw  []byte
		responseRaw []byte
	)
	if err := row.Scan(&req.ID, &req.WorkflowID, &req.TaskID, &req.Message,
		&optionsRaw, &status, &responseRaw, &req.CreatedAt, &req.TimeoutAt); err != nil {
		return nil, err
	}

	req.Status = RequestStatus(status)
	if len(optionsRaw) > 0 {
		if err := json.Unmarshal(optionsRaw, &req.Options); err != nil {
			return nil, fmt.Errorf("unmarshal options: %w", err)
		}
	}
	if len(responseRaw) > 0 {
		var resp Response
		if err := json.Unmarshal(responseRaw, &resp); err != nil {
			return nil, fmt.Errorf("unmarshal response: %w", err)
		}
		req.Response = &resp
	}
	return &req, nil
}

// orPending supplies the status default the table also declares, so a Request
// built without one still persists as pending rather than as an empty string.
func orPending(s RequestStatus) RequestStatus {
	if s == "" {
		return StatusPending
	}
	return s
}

// orEmpty keeps the NOT NULL options column valid for a request with no choices.
func orEmpty(options []string) []string {
	if options == nil {
		return []string{}
	}
	return options
}

// nullResponse stores no response as SQL NULL rather than as JSON null, matching
// how the other nullable JSONB columns in this project are written.
func nullResponse(resp *Response) any {
	if resp == nil {
		return nil
	}
	data, err := json.Marshal(resp)
	if err != nil {
		return nil
	}
	return data
}
