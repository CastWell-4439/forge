package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"
)

var (
	bucketWorkflowDefs = []byte("workflow_definitions")
	bucketWorkflows    = []byte("workflows")
	bucketTasks        = []byte("tasks")
	bucketTasksByWF    = []byte("tasks_by_workflow") // workflow_id -> []task_id
	bucketEvents       = []byte("events")
	bucketEventsByWF   = []byte("events_by_workflow") // workflow_id -> []event_id
)

// BoltStorage implements Storage using bbolt for standalone/dev mode.
type BoltStorage struct {
	db     *bolt.DB
	mu     sync.Mutex
	seqNum int64 // event sequence counter
}

// NewBoltStorage creates a new BoltDB-backed storage at the given path.
func NewBoltStorage(path string) (*BoltStorage, error) {
	db, err := bolt.Open(path, 0600, &bolt.Options{Timeout: 1 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("open bolt db %s: %w", path, err)
	}

	err = db.Update(func(tx *bolt.Tx) error {
		for _, b := range [][]byte{
			bucketWorkflowDefs, bucketWorkflows, bucketTasks,
			bucketTasksByWF, bucketEvents, bucketEventsByWF,
		} {
			if _, err := tx.CreateBucketIfNotExists(b); err != nil {
				return fmt.Errorf("create bucket %s: %w", b, err)
			}
		}
		return nil
	})
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("init bolt buckets: %w", err)
	}

	return &BoltStorage{db: db}, nil
}

// SaveWorkflowDefinition stores a workflow definition keyed by "name:version".
func (s *BoltStorage) SaveWorkflowDefinition(_ context.Context, def *WorkflowDefinition) error {
	now := time.Now()
	if def.CreatedAt.IsZero() {
		def.CreatedAt = now
	}
	def.UpdatedAt = now

	key := fmt.Sprintf("%s:%d", def.Name, def.Version)
	data, err := json.Marshal(def)
	if err != nil {
		return fmt.Errorf("marshal workflow definition: %w", err)
	}

	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketWorkflowDefs).Put([]byte(key), data)
	})
}

// GetWorkflowDefinition retrieves a workflow definition by name and version.
func (s *BoltStorage) GetWorkflowDefinition(_ context.Context, name string, version int) (*WorkflowDefinition, error) {
	key := fmt.Sprintf("%s:%d", name, version)
	var def WorkflowDefinition

	err := s.db.View(func(tx *bolt.Tx) error {
		data := tx.Bucket(bucketWorkflowDefs).Get([]byte(key))
		if data == nil {
			return fmt.Errorf("workflow definition %s v%d not found", name, version)
		}
		return json.Unmarshal(data, &def)
	})
	if err != nil {
		return nil, err
	}
	return &def, nil
}

// SaveWorkflow inserts a new workflow instance.
func (s *BoltStorage) SaveWorkflow(_ context.Context, wf *Workflow) error {
	if wf.CreatedAt.IsZero() {
		wf.CreatedAt = time.Now()
	}
	data, err := json.Marshal(wf)
	if err != nil {
		return fmt.Errorf("marshal workflow: %w", err)
	}

	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketWorkflows).Put([]byte(wf.ID), data)
	})
}

// GetWorkflow retrieves a workflow instance by ID.
func (s *BoltStorage) GetWorkflow(_ context.Context, workflowID string) (*Workflow, error) {
	var wf Workflow
	err := s.db.View(func(tx *bolt.Tx) error {
		data := tx.Bucket(bucketWorkflows).Get([]byte(workflowID))
		if data == nil {
			return fmt.Errorf("workflow %s not found", workflowID)
		}
		return json.Unmarshal(data, &wf)
	})
	if err != nil {
		return nil, err
	}
	return &wf, nil
}

// ListWorkflows returns workflow instances filtered by status.
func (s *BoltStorage) ListWorkflows(_ context.Context, status WorkflowStatus, limit int, offset int) ([]*Workflow, error) {
	var workflows []*Workflow
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketWorkflows)
		return b.ForEach(func(k, v []byte) error {
			var wf Workflow
			if err := json.Unmarshal(v, &wf); err != nil {
				return err
			}
			if status == "" || wf.Status == status {
				workflows = append(workflows, &wf)
			}
			return nil
		})
	})
	if err != nil {
		return nil, fmt.Errorf("list workflows: %w", err)
	}

	// Sort by created_at descending
	sort.Slice(workflows, func(i, j int) bool {
		return workflows[i].CreatedAt.After(workflows[j].CreatedAt)
	})

	// Apply offset and limit
	if offset >= len(workflows) {
		return nil, nil
	}
	workflows = workflows[offset:]
	if limit > 0 && limit < len(workflows) {
		workflows = workflows[:limit]
	}
	return workflows, nil
}

// UpdateWorkflowStatus updates the status of a workflow instance.
func (s *BoltStorage) UpdateWorkflowStatus(_ context.Context, workflowID string, status WorkflowStatus) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketWorkflows)
		data := b.Get([]byte(workflowID))
		if data == nil {
			return fmt.Errorf("workflow %s not found", workflowID)
		}

		var wf Workflow
		if err := json.Unmarshal(data, &wf); err != nil {
			return err
		}

		now := time.Now()
		wf.Status = status
		switch status {
		case WorkflowStatusRunning:
			wf.StartedAt = &now
		case WorkflowStatusCompleted, WorkflowStatusFailed, WorkflowStatusCancelled:
			wf.FinishedAt = &now
		}

		updated, err := json.Marshal(&wf)
		if err != nil {
			return err
		}
		return b.Put([]byte(workflowID), updated)
	})
}

// SaveTask inserts a new task instance.
func (s *BoltStorage) SaveTask(_ context.Context, task *Task) error {
	if task.CreatedAt.IsZero() {
		task.CreatedAt = time.Now()
	}
	data, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("marshal task: %w", err)
	}

	return s.db.Update(func(tx *bolt.Tx) error {
		if err := tx.Bucket(bucketTasks).Put([]byte(task.ID), data); err != nil {
			return err
		}
		// Maintain the workflow -> tasks index
		return s.addToIndex(tx, bucketTasksByWF, task.WorkflowID, task.ID)
	})
}

// GetTask retrieves a task instance by ID.
func (s *BoltStorage) GetTask(_ context.Context, taskID string) (*Task, error) {
	var task Task
	err := s.db.View(func(tx *bolt.Tx) error {
		data := tx.Bucket(bucketTasks).Get([]byte(taskID))
		if data == nil {
			return fmt.Errorf("task %s not found", taskID)
		}
		return json.Unmarshal(data, &task)
	})
	if err != nil {
		return nil, err
	}
	return &task, nil
}

// ListTasksByWorkflow retrieves all tasks for a given workflow.
func (s *BoltStorage) ListTasksByWorkflow(_ context.Context, workflowID string) ([]*Task, error) {
	var tasks []*Task
	err := s.db.View(func(tx *bolt.Tx) error {
		ids, err := s.getIndex(tx, bucketTasksByWF, workflowID)
		if err != nil {
			return err
		}
		b := tx.Bucket(bucketTasks)
		for _, id := range ids {
			data := b.Get([]byte(id))
			if data == nil {
				continue
			}
			var task Task
			if err := json.Unmarshal(data, &task); err != nil {
				return err
			}
			tasks = append(tasks, &task)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list tasks for workflow %s: %w", workflowID, err)
	}

	sort.Slice(tasks, func(i, j int) bool {
		return tasks[i].CreatedAt.Before(tasks[j].CreatedAt)
	})
	return tasks, nil
}

// ClaimTask finds a READY task matching one of the handlers and atomically assigns it.
// BoltDB serializes all writes, so no SKIP LOCKED needed — mutual exclusion is inherent.
func (s *BoltStorage) ClaimTask(_ context.Context, workerID string, handlers []string) (*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	handlerSet := make(map[string]bool, len(handlers))
	for _, h := range handlers {
		handlerSet[h] = true
	}

	var claimed *Task
	err := s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketTasks)
		return b.ForEach(func(k, v []byte) error {
			if claimed != nil {
				return nil // already found one
			}
			var task Task
			if err := json.Unmarshal(v, &task); err != nil {
				return err
			}
			if task.Status == TaskStatusReady && handlerSet[task.Handler] {
				now := time.Now()
				task.Status = TaskStatusScheduled
				task.WorkerID = workerID
				task.ScheduledAt = &now

				data, err := json.Marshal(&task)
				if err != nil {
					return err
				}
				if err := b.Put(k, data); err != nil {
					return err
				}
				claimed = &task
			}
			return nil
		})
	})
	if err != nil {
		return nil, fmt.Errorf("claim task for worker %s: %w", workerID, err)
	}
	return claimed, nil
}

// UpdateTaskStatus updates the status of a task instance.
func (s *BoltStorage) UpdateTaskStatus(_ context.Context, taskID string, status TaskStatus) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketTasks)
		data := b.Get([]byte(taskID))
		if data == nil {
			return fmt.Errorf("task %s not found", taskID)
		}

		var task Task
		if err := json.Unmarshal(data, &task); err != nil {
			return err
		}

		now := time.Now()
		task.Status = status
		switch status {
		case TaskStatusRunning:
			task.StartedAt = &now
		case TaskStatusCompleted, TaskStatusFailed, TaskStatusSkipped:
			task.FinishedAt = &now
		}

		updated, err := json.Marshal(&task)
		if err != nil {
			return err
		}
		return b.Put([]byte(taskID), updated)
	})
}

// isTerminalTaskStatus reports whether a task status is a terminal state that
// must not be overwritten by subsequent Complete/Fail calls.
func isTerminalTaskStatus(s TaskStatus) bool {
	return s == TaskStatusCompleted || s == TaskStatusFailed || s == TaskStatusSkipped
}

// CompleteTask marks a task as COMPLETED and persists its output.
// Idempotent: a task already in a terminal state is left untouched.
// A missing task is treated as a no-op with a warning log.
func (s *BoltStorage) CompleteTask(_ context.Context, taskID string, output json.RawMessage) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketTasks)
		raw := b.Get([]byte(taskID))
		if raw == nil {
			log.Printf("WARN: CompleteTask(%s) task not found", taskID)
			return nil
		}
		var task Task
		if err := json.Unmarshal(raw, &task); err != nil {
			return fmt.Errorf("unmarshal task %s: %w", taskID, err)
		}
		if isTerminalTaskStatus(task.Status) {
			log.Printf("WARN: CompleteTask(%s) already in terminal state %s, skip",
				taskID, task.Status)
			return nil
		}
		now := time.Now()
		task.Status = TaskStatusCompleted
		task.Output = output
		task.FinishedAt = &now

		data, err := json.Marshal(&task)
		if err != nil {
			return fmt.Errorf("marshal task %s: %w", taskID, err)
		}
		return b.Put([]byte(taskID), data)
	})
}

// FailTask marks a task as FAILED and persists its error message.
// Idempotent with the same semantics as CompleteTask.
func (s *BoltStorage) FailTask(_ context.Context, taskID string, errorMsg string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketTasks)
		raw := b.Get([]byte(taskID))
		if raw == nil {
			log.Printf("WARN: FailTask(%s) task not found", taskID)
			return nil
		}
		var task Task
		if err := json.Unmarshal(raw, &task); err != nil {
			return fmt.Errorf("unmarshal task %s: %w", taskID, err)
		}
		if isTerminalTaskStatus(task.Status) {
			log.Printf("WARN: FailTask(%s) already in terminal state %s, skip",
				taskID, task.Status)
			return nil
		}
		now := time.Now()
		task.Status = TaskStatusFailed
		task.ErrorMsg = errorMsg
		task.FinishedAt = &now

		data, err := json.Marshal(&task)
		if err != nil {
			return fmt.Errorf("marshal task %s: %w", taskID, err)
		}
		return b.Put([]byte(taskID), data)
	})
}

// SaveEvent inserts a new event into the event log.
func (s *BoltStorage) SaveEvent(_ context.Context, event *Event) error {
	s.mu.Lock()
	s.seqNum++
	if event.SequenceNum == 0 {
		event.SequenceNum = s.seqNum
	}
	s.mu.Unlock()

	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}

	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}

	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketEvents)
		id, err := b.NextSequence()
		if err != nil {
			return err
		}
		event.ID = int64(id)
		key := fmt.Sprintf("%020d", id)

		data, err = json.Marshal(event)
		if err != nil {
			return err
		}
		if err := b.Put([]byte(key), data); err != nil {
			return err
		}
		return s.addToIndex(tx, bucketEventsByWF, event.WorkflowID, key)
	})
}

// GetWorkflowHistory retrieves all events for a workflow ordered by sequence number.
func (s *BoltStorage) GetWorkflowHistory(_ context.Context, workflowID string) ([]*Event, error) {
	var events []*Event
	err := s.db.View(func(tx *bolt.Tx) error {
		ids, err := s.getIndex(tx, bucketEventsByWF, workflowID)
		if err != nil {
			return err
		}
		b := tx.Bucket(bucketEvents)
		for _, id := range ids {
			data := b.Get([]byte(id))
			if data == nil {
				continue
			}
			var evt Event
			if err := json.Unmarshal(data, &evt); err != nil {
				return err
			}
			events = append(events, &evt)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("get workflow %s history: %w", workflowID, err)
	}

	sort.Slice(events, func(i, j int) bool {
		return events[i].SequenceNum < events[j].SequenceNum
	})
	return events, nil
}

// Close closes the underlying BoltDB.
func (s *BoltStorage) Close() error {
	return s.db.Close()
}

// CountWorkflows returns a count of workflows grouped by status.
// Scans all workflows in BoltDB (fine for dev/standalone; PG uses SQL COUNT).
func (s *BoltStorage) CountWorkflows(_ context.Context) (map[WorkflowStatus]int32, error) {
	counts := make(map[WorkflowStatus]int32)
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketWorkflows).ForEach(func(_, v []byte) error {
			var wf Workflow
			if err := json.Unmarshal(v, &wf); err != nil {
				return err
			}
			counts[wf.Status]++
			return nil
		})
	})
	if err != nil {
		return nil, fmt.Errorf("count workflows: %w", err)
	}
	return counts, nil
}

// addToIndex appends a value to a list stored under a key in an index bucket.
func (s *BoltStorage) addToIndex(tx *bolt.Tx, bucket []byte, key, value string) error {
	b := tx.Bucket(bucket)
	var ids []string
	if data := b.Get([]byte(key)); data != nil {
		if err := json.Unmarshal(data, &ids); err != nil {
			return err
		}
	}
	ids = append(ids, value)
	data, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	return b.Put([]byte(key), data)
}

// getIndex retrieves a list of values for a key from an index bucket.
func (s *BoltStorage) getIndex(tx *bolt.Tx, bucket []byte, key string) ([]string, error) {
	b := tx.Bucket(bucket)
	data := b.Get([]byte(key))
	if data == nil {
		return nil, nil
	}
	var ids []string
	if err := json.Unmarshal(data, &ids); err != nil {
		return nil, err
	}
	return ids, nil
}
