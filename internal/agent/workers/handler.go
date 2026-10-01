// Package workers implements the agent's tool handlers: the capabilities the
// ReAct loop can call. The set is domain-agnostic on purpose - it covers the
// generic work coding agents are given everywhere (files, shell, search, git
// reads, web, data, asking a human), and carries no product-specific vocabulary.
// Handlers support two modes: "mock" for testing and "real" for production use.
package workers

import (
	"context"
	"fmt"
)

// HandlerFunc is now defined in core/tools.go for dependency direction compliance.
// It is re-exported via registry.go type alias.

// HandlerMode defines the execution mode for handlers.
type HandlerMode string

const (
	// HandlerModeMock returns fake/plausible results without calling real services.
	HandlerModeMock HandlerMode = "mock"
	// HandlerModeReal calls real external services (requires proper configuration).
	HandlerModeReal HandlerMode = "real"
)

// AskUserFunc blocks until a human answers a question. It is the assembly point
// for interactive tools: the agent layer stays free of any particular approval
// system, and whoever wires the agent decides where the answer comes from.
type AskUserFunc func(ctx context.Context, question string, options []string) (string, error)

// HandlerConfig holds configuration for handler creation.
type HandlerConfig struct {
	Mode      HandlerMode
	Workspace string // Base directory for file operations, e.g. "/tmp/forge"

	// AskUser answers ask.user in real mode. When nil the tool reports that no
	// interactive channel is configured instead of pretending a human replied.
	AskUser AskUserFunc
}

// ErrNotConfigured is returned when a real-mode handler is called but the
// underlying service is not configured. Each handler group documents its
// required external dependencies:
//   - data.*:        a database DSN
//   - web.*:         outbound network access
//   - code.execute:  a code execution sandbox
//   - ask.user:      an interactive channel (HandlerConfig.AskUser)
//
// Handlers with no external dependency - file.*, git.*, shell.run (whitelisted)
// - are implemented for real mode.
var ErrNotConfigured = fmt.Errorf("handler not configured: real mode requires external service setup")
