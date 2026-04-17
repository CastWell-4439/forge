package agent

import (
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

// SessionState represents the current state of an agent session.
type SessionState string

const (
	// StateIdle is the initial state before any processing begins.
	StateIdle SessionState = "idle"
	// StateParsing indicates the requirement is being parsed.
	StateParsing SessionState = "parsing"
	// StatePlanning indicates the DAG is being planned/generated.
	StatePlanning SessionState = "planning"
	// StateExecuting indicates the workflow is being executed by Forge.
	StateExecuting SessionState = "executing"
	// StateChecking indicates quality checks are being run.
	StateChecking SessionState = "checking"
	// StateFixing indicates a correction DAG is being generated/executed.
	StateFixing SessionState = "fixing"
	// StateCompleted indicates all work has finished successfully.
	StateCompleted SessionState = "completed"
	// StateFailed indicates the session has failed unrecoverably.
	StateFailed SessionState = "failed"
)

// validTransitions defines the allowed state transitions for the session
// state machine. From agent-tech-spec 3.4.
var validTransitions = map[SessionState][]SessionState{
	StateIdle:      {StateParsing, StateFailed},
	StateParsing:   {StatePlanning, StateFailed},
	StatePlanning:  {StateExecuting, StateFailed},
	StateExecuting: {StateChecking, StateFailed},
	StateChecking:  {StateCompleted, StateFixing, StateFailed},
	StateFixing:    {StateExecuting, StateFailed},
	StateCompleted: {},
	StateFailed:    {},
}

// Session holds the state and context for a single agent session.
// From agent-tech-spec 3.5.
type Session struct {
	mu sync.RWMutex

	// ID is the unique session identifier.
	ID string
	// State is the current session state.
	State SessionState
	// Messages is the conversation history.
	Messages []Message
	// Requirement is the parsed video requirement (continuously refined).
	Requirement *VideoRequirement
	// RetryCount tracks how many correction cycles have been attempted.
	RetryCount int
	// WorkflowID is the Forge workflow instance ID (set after submission).
	WorkflowID string
	// CreatedAt is when the session was created.
	CreatedAt time.Time

	// maxRetries is the maximum number of fix/re-execute cycles.
	maxRetries int
}

// NewSession creates a new session in the idle state.
func NewSession() *Session {
	return &Session{
		ID:         uuid.New().String(),
		State:      StateIdle,
		Messages:   make([]Message, 0),
		CreatedAt:  time.Now(),
		maxRetries: 3,
	}
}

// Transition attempts to transition the session to the target state.
// Returns an error if the transition is not valid.
func (s *Session) Transition(target SessionState) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	allowed, ok := validTransitions[s.State]
	if !ok {
		return fmt.Errorf("session %s: unknown state %q", s.ID, s.State)
	}

	for _, valid := range allowed {
		if valid == target {
			s.State = target
			return nil
		}
	}

	return fmt.Errorf("session %s: invalid transition from %q to %q", s.ID, s.State, target)
}

// GetState returns the current session state (thread-safe).
func (s *Session) GetState() SessionState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.State
}

// AddMessage appends a message to the session history.
func (s *Session) AddMessage(msg Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Messages = append(s.Messages, msg)
}

// CanRetry returns true if the session has not exceeded its retry limit.
func (s *Session) CanRetry() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.RetryCount < s.maxRetries
}

// IncrementRetry increments the retry counter.
func (s *Session) IncrementRetry() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.RetryCount++
}

// SetWorkflowID sets the Forge workflow ID for this session.
func (s *Session) SetWorkflowID(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.WorkflowID = id
}

// SetRequirement sets the parsed requirement.
func (s *Session) SetRequirement(req *VideoRequirement) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Requirement = req
}

// SessionStore is the interface for persisting sessions.
type SessionStore interface {
	// Save persists a session.
	Save(session *Session) error
	// Get retrieves a session by ID.
	Get(id string) (*Session, error)
	// Delete removes a session by ID.
	Delete(id string) error
	// List returns all sessions.
	List() ([]*Session, error)
}

// InMemorySessionStore is a simple in-memory implementation of SessionStore.
type InMemorySessionStore struct {
	mu       sync.RWMutex
	sessions map[string]*Session
}

// NewInMemorySessionStore creates a new in-memory session store.
func NewInMemorySessionStore() *InMemorySessionStore {
	return &InMemorySessionStore{
		sessions: make(map[string]*Session),
	}
}

// Save persists a session in memory.
func (s *InMemorySessionStore) Save(session *Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[session.ID] = session
	return nil
}

// Get retrieves a session by ID.
func (s *InMemorySessionStore) Get(id string) (*Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	session, ok := s.sessions[id]
	if !ok {
		return nil, fmt.Errorf("session %s not found", id)
	}
	return session, nil
}

// Delete removes a session by ID.
func (s *InMemorySessionStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sessions[id]; !ok {
		return fmt.Errorf("session %s not found", id)
	}
	delete(s.sessions, id)
	return nil
}

// List returns all sessions.
func (s *InMemorySessionStore) List() ([]*Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]*Session, 0, len(s.sessions))
	for _, session := range s.sessions {
		result = append(result, session)
	}
	return result, nil
}
