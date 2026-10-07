package core

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// ErrNoCheckpoint reports that no checkpoint exists for the requested ID or
// session. Callers distinguish "nothing to resume" from a real read failure.
var ErrNoCheckpoint = errors.New("no checkpoint")

// ---------- Enhancement Module Interfaces (Plugin Slots) ----------
// Each interface is optional. Agent assembles them via WithXxx options.
// If a module is not provided, the Agent skips the corresponding logic.

// InputGuard checks input for prompt injection attacks. (M6 Guardrails)
type InputGuard interface {
	Check(ctx context.Context, input string) error
}

// OutputGuard filters sensitive content from LLM output. (M6 Guardrails)
type OutputGuard interface {
	Check(ctx context.Context, output string) (string, error)
}

// BudgetChecker enforces token/cost budget per session. (M6 Guardrails)
type BudgetChecker interface {
	Check(ctx context.Context, sessionID string) error
	Record(ctx context.Context, sessionID string, tokens int64) error
}

// Retriever searches a knowledge base for relevant documents. (M3 RAG)
type Retriever interface {
	Search(ctx context.Context, query string, topK int) ([]Document, error)
	Index(ctx context.Context, docs []Document) error
}

// MemoryStore manages short-term and long-term agent memory. (M5 Memory)
type MemoryStore interface {
	SaveShortTerm(ctx context.Context, sessionID string, key string, value any) error
	GetShortTerm(ctx context.Context, sessionID string, key string) (any, error)
	SaveLongTerm(ctx context.Context, entry MemoryEntry) error
	SearchLongTerm(ctx context.Context, query string, topK int) ([]MemoryEntry, error)
}

// LessonSource supplies lessons distilled elsewhere — today, by ForgeX from
// finished workflow runs — so an agent can start from what was already learned
// instead of rediscovering it.
//
// It is deliberately a separate, narrow interface rather than a second
// MemoryStore: the two have opposite directions. Memory is written BY the
// agent about itself; lessons are written by another plane ABOUT runs the
// agent did not necessarily take part in, and are read-only here. Sharing one
// interface would have made "remember this" and "learn this" indistinguishable
// at the call site, which is exactly the confusion recallInto's naming already
// risks.
//
// Implementations live outside internal/agent (assembly wires them): the agent
// plane must not import the control plane.
type LessonSource interface {
	// Recall returns up to topK lessons relevant to query, newest first when
	// relevance ties. An empty result is normal, not an error.
	Recall(ctx context.Context, query string, topK int) ([]RecallItem, error)
}

// RecallItem is one lesson as the agent plane sees it: no forgex types, no
// storage layout, just the fields a prompt can use. SourceRunID is carried so
// the model (and the audit trail) can tell where a claim came from — a lesson
// without provenance is an assertion the model cannot weigh.
type RecallItem struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Category    string    `json:"category"`
	Content     string    `json:"content"`
	SourceRunID string    `json:"source_run_id,omitempty"`
	CreatedAt   time.Time `json:"created_at,omitempty"`
}

// CheckpointStore persists agent state for crash recovery. (M12 Checkpointing)
type CheckpointStore interface {
	Save(ctx context.Context, cp *Checkpoint) error
	Load(ctx context.Context, id string) (*Checkpoint, error)
	Latest(ctx context.Context, sessionID string) (*Checkpoint, error)
}

// MCPManager manages MCP server lifecycles and tool discovery. (M1 MCP)
type MCPManager interface {
	Start(ctx context.Context) error
	Stop() error
	ListTools(ctx context.Context) ([]MCPToolDef, error)
	CallTool(ctx context.Context, name string, params json.RawMessage) (*ToolResult, error)
}

// Verifier checks tool execution results for correctness. (D5 Self-Verification)
// After a tool call, Verify inspects the action and result.
// Returns ok=true if the result is satisfactory.
// If ok=false, feedback is added to the conversation as extra context for retry.
type Verifier interface {
	Verify(ctx context.Context, action ToolCall, result *ToolResult) (ok bool, feedback string, err error)
}

// MCPToolDef describes a tool discovered via MCP protocol.
type MCPToolDef struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// InputSchema is the raw JSON Schema the MCP server advertises under
	// tools/list. Dropping it left bridged tools with no parameter
	// information, so the model had to guess argument names and types.
	InputSchema map[string]interface{} `json:"input_schema,omitempty"`
}

// Tool call statuses recorded in a checkpoint's side-effect ledger.
const (
	ToolCallStarted   = "started"
	ToolCallCompleted = "completed"
)

// ToolCallRecord is one entry of a checkpoint's side-effect ledger.
//
// Recovery needs to know which tools already ran: replaying a non-idempotent
// tool would produce its side effect a second time. A record is written with
// ToolCallStarted before the tool is invoked and rewritten to ToolCallCompleted
// afterwards, so a record left in ToolCallStarted is precisely the "it may or
// may not have happened" case that a resume must not resolve by guessing.
//
// Params records what the tool was called with. It makes the ledger's own
// stated purpose enforceable at runtime: "this exact call already ran" is only
// answerable when the record says what the call was, and it also lets an
// operator reconstruct the invocation afterwards instead of seeing only its
// result.
type ToolCallRecord struct {
	ID          string         `json:"id"`
	StepIndex   int            `json:"step_index"`
	Tool        string         `json:"tool"`
	Params      map[string]any `json:"params,omitempty"`
	Idempotent  bool           `json:"idempotent"`
	Status      string         `json:"status"`
	Result      string         `json:"result,omitempty"`
	Error       string         `json:"error,omitempty"`
	StartedAt   time.Time      `json:"started_at"`
	CompletedAt time.Time      `json:"completed_at,omitempty"`
}

// Checkpoint represents a saved agent state for recovery.
type Checkpoint struct {
	ID        string    `json:"id"`
	SessionID string    `json:"session_id"`
	StepIndex int       `json:"step_index"`
	Messages  []Message `json:"messages"`

	// ToolCalls is the side-effect ledger. See ToolCallRecord.
	ToolCalls []ToolCallRecord `json:"tool_calls,omitempty"`
	// RetryCount and ApprovalState carry the rest of the recovery context so a
	// resumed run does not silently restart from a clean slate.
	RetryCount    int    `json:"retry_count,omitempty"`
	ApprovalState string `json:"approval_state,omitempty"`
	// Completed and Answer record that the run already reached its final answer.
	// Without them a resume would redo work that was already finished.
	Completed bool      `json:"completed,omitempty"`
	Answer    string    `json:"answer,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// UnresolvedToolCall returns the ledger's trailing record when it is still in
// the started state, meaning the process may have died mid-invocation.
func (c *Checkpoint) UnresolvedToolCall() (ToolCallRecord, bool) {
	if c == nil || len(c.ToolCalls) == 0 {
		return ToolCallRecord{}, false
	}
	last := c.ToolCalls[len(c.ToolCalls)-1]
	if last.Status != ToolCallStarted {
		return ToolCallRecord{}, false
	}
	return last, true
}

// Document represents a retrievable document for RAG.
type Document struct {
	ID       string            `json:"id"`
	Content  string            `json:"content"`
	Metadata map[string]string `json:"metadata,omitempty"`
	Score    float64           `json:"score,omitempty"`
}

// MemoryEntry represents a long-term memory record.
type MemoryEntry struct {
	ID        string    `json:"id"`
	Content   string    `json:"content"`
	Category  string    `json:"category"`
	CreatedAt time.Time `json:"created_at"`
}
