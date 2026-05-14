package core

import (
	"context"
	"encoding/json"
	"time"
)

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
}

// Checkpoint represents a saved agent state for recovery.
type Checkpoint struct {
	ID        string    `json:"id"`
	SessionID string    `json:"session_id"`
	StepIndex int       `json:"step_index"`
	Messages  []Message `json:"messages"`
	CreatedAt time.Time `json:"created_at"`
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
