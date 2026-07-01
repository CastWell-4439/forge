package integration

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/castwell/forge/internal/forgex/model"
)

// CaseInput is the normalized output of an inbound integration adapter.
type CaseInput struct {
	Source     string            `json:"source" yaml:"source"`
	ReceivedAt time.Time         `json:"received_at" yaml:"received_at"`
	TaskPacket model.TaskPacket  `json:"task_packet" yaml:"task_packet"`
	Metadata   map[string]string `json:"metadata,omitempty" yaml:"metadata,omitempty"`
}

// WebhookAdapter converts an external webhook payload into a ForgeX case input.
// Implementations must live outside the core control-plane path.
type WebhookAdapter interface {
	Parse(ctx context.Context, payload []byte) (CaseInput, error)
}

// ToolRequest is the future integration-layer request shape for real tools.
type ToolRequest struct {
	RunID    string         `json:"run_id" yaml:"run_id"`
	ToolName string         `json:"tool_name" yaml:"tool_name"`
	Args     map[string]any `json:"args,omitempty" yaml:"args,omitempty"`
}

// ToolResponse is the future integration-layer response shape for real tools.
type ToolResponse struct {
	RunID     string         `json:"run_id" yaml:"run_id"`
	ToolName  string         `json:"tool_name" yaml:"tool_name"`
	Result    map[string]any `json:"result,omitempty" yaml:"result,omitempty"`
	Error     string         `json:"error,omitempty" yaml:"error,omitempty"`
	StartedAt time.Time      `json:"started_at" yaml:"started_at"`
	EndedAt   time.Time      `json:"ended_at" yaml:"ended_at"`
}

// ExternalToolAdapter is the boundary for future real tool calls. The core
// ForgeX eval/control path must keep using deterministic local adapters unless
// a caller explicitly wires an implementation.
type ExternalToolAdapter interface {
	Call(ctx context.Context, req ToolRequest) (ToolResponse, error)
}

// NoopWebhookAdapter rejects all payloads. It documents that ForgeX core has no
// default webhook integration.
type NoopWebhookAdapter struct{}

func (NoopWebhookAdapter) Parse(ctx context.Context, payload []byte) (CaseInput, error) {
	if err := ctx.Err(); err != nil {
		return CaseInput{}, err
	}
	return CaseInput{}, fmt.Errorf("noop webhook adapter: no external parser configured")
}

// NoopToolAdapter rejects all calls. It documents that ForgeX core has no
// default external tool integration.
type NoopToolAdapter struct{}

func (NoopToolAdapter) Call(ctx context.Context, req ToolRequest) (ToolResponse, error) {
	if err := ctx.Err(); err != nil {
		return ToolResponse{}, err
	}
	return ToolResponse{
		RunID:     req.RunID,
		ToolName:  req.ToolName,
		Error:     "noop tool adapter: no external tool configured",
		StartedAt: time.Now().UTC(),
		EndedAt:   time.Now().UTC(),
	}, fmt.Errorf("noop tool adapter: no external tool configured")
}

// ValidateToolRequest checks the minimal structure expected by future external
// tool adapters.
func ValidateToolRequest(req ToolRequest) error {
	if strings.TrimSpace(req.RunID) == "" {
		return fmt.Errorf("run_id is required")
	}
	if strings.TrimSpace(req.ToolName) == "" {
		return fmt.Errorf("tool_name is required")
	}
	return nil
}
