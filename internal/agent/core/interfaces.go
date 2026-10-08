package core

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
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
	// Annotations is the server's declared behaviour (readOnlyHint,
	// destructiveHint, ...). It is a HINT, not a guarantee — the bridge maps
	// it to ToolDef.Effect in the cautious direction only.
	Annotations *MCPToolAnnotations `json:"annotations,omitempty"`
}

// MCPToolAnnotations mirrors the MCP specification's tool annotations.
// Pointers distinguish "the server said false" from "the server said nothing",
// which is the difference between a declared write and an undeclared tool.
type MCPToolAnnotations struct {
	Title           string `json:"title,omitempty"`
	ReadOnlyHint    *bool  `json:"readOnlyHint,omitempty"`
	DestructiveHint *bool  `json:"destructiveHint,omitempty"`
	IdempotentHint  *bool  `json:"idempotentHint,omitempty"`
	OpenWorldHint   *bool  `json:"openWorldHint,omitempty"`
}

// Tool call statuses recorded in a checkpoint's side-effect ledger.
const (
	ToolCallStarted   = "started"
	ToolCallCompleted = "completed"
)

// SubagentRequest is one delegation (N5).
//
// It carries the task and, when continuing, which child to continue. The
// remaining configuration (report shape, limits, tool narrowing) belongs to the
// deployment, not to the call: a model choosing "return full steps" would
// silently spend its own context, so that decision stays with the operator.
type SubagentRequest struct {
	// Task is what the child is asked to do.
	Task string
	// ChildID, when set, continues an existing child instead of creating one.
	// Only meaningful in continuable mode; a one-shot child is gone by the time
	// anyone could name it.
	ChildID string
	// Depth is the delegation depth of the CALLER (0 for the top-level agent).
	// It is passed in rather than stored globally because the same agent
	// instance may serve a root run and a nested one.
	Depth int
}

// SubagentResult is what comes back from a delegation.
type SubagentResult struct {
	// ChildID identifies the child, so a continuable deployment can address it
	// again. It is empty in one-shot modes.
	ChildID string
	// Answer is the child's final output. It is always present on success:
	// returning "it finished" without the result would make the tool useless.
	Answer string
	// Reason is the child's stop reason, so the parent can tell "completed"
	// from "ran out of steps" (see harness.RunResult.Reason).
	Reason string
	// Steps carries the child's trace when the report setting asks for it.
	// Empty under SubagentReportAnswer — that emptiness IS the isolation.
	Steps []SubagentStep
	// Paused/PauseReason mirror a child that stopped to ask a human. A child
	// cannot itself get an answer (it has no interactive channel), so the parent
	// must be told: swallowing it would leave a paused child nobody knows about.
	Paused      bool
	PauseReason string
}

// SubagentStep is one step of a child's trace, as reported to the parent.
type SubagentStep struct {
	Step    int    `json:"step"`
	Thought string `json:"thought,omitempty"`
	Action  string `json:"action,omitempty"`
	// Result is the tool result, included only under SubagentReportSteps. Under
	// Summary it is empty, which is what keeps a trace cheap.
	Result string `json:"result,omitempty"`
}

// SubagentRunner runs a delegation. Implemented by the harness (which owns the
// loop) and injected into the tool, so the worker layer does not need to import
// the loop — the dependency direction stays workers -> core.
type SubagentRunner interface {
	// RunSubagent performs one delegation. An error means the delegation could
	// not be performed at all (depth exceeded, child failed to start); a child
	// that ran and failed comes back as a result whose Reason says so.
	RunSubagent(ctx context.Context, req SubagentRequest) (*SubagentResult, error)
	// SubagentDepth reports the caller's current delegation depth.
	SubagentDepth(ctx context.Context) int
}

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
//
// The first four fields are the original record. The rest are the governance
// metadata (see evidence.go): without them, recall cannot tell a human-confirmed
// claim from a self-distilled one, cannot tell which of two entries speaks
// about the same subject, and cannot tell which is the older observation.
//
// Every added field is optional. An entry written before they existed reads as
// "unknown source, default confidence, observed when written", which is exactly
// the trust it deserves — the point is to stop treating unknown as certain, not
// to retroactively promote or demote anything.
type MemoryEntry struct {
	ID        string    `json:"id"`
	Content   string    `json:"content"`
	Category  string    `json:"category"`
	CreatedAt time.Time `json:"created_at"`

	// Source records who produced this memory, with a kind prefix
	// (run:/lesson:/human:). Empty means a producer that did not say.
	Source MemorySource `json:"source,omitempty"`
	// Confidence is the producer's trust in the claim, 0..1. Zero means unset
	// and reads as DefaultConfidence — so the scale's bottom is written
	// explicitly (0.01) by a producer that means "worthless".
	Confidence float64 `json:"confidence,omitempty"`
	// ObservedAt is when the claim was true, which is not the same as when it
	// was written. Zero falls back to CreatedAt.
	ObservedAt time.Time `json:"observed_at,omitempty"`
	// Layer says what kind of thing this is: a claim about the world (fact) or
	// one run's experience (episodic). Empty reads as episodic — an unlabelled
	// memory is treated as an observation rather than promoted to a claim.
	Layer MemoryLayer `json:"layer,omitempty"`
}

// SourceRunIDOrEmpty reports the run a memory came from, or "".
//
// A convenience for review reasons: an entry whose source is not a run (a
// lesson, a human note) has no run id to print, and an empty string is clearer
// in a report than the word "unknown" repeated on every line.
func (m MemoryEntry) SourceRunIDOrEmpty() string {
	src := string(m.Source)
	if strings.HasPrefix(src, MemorySourceRun) {
		return strings.TrimPrefix(src, MemorySourceRun)
	}
	return ""
}
