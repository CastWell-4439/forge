// Package workers implements the agent's tool handlers: the capabilities the
// ReAct loop can call. The set is domain-agnostic on purpose - it covers the
// generic work coding agents are given everywhere (files, shell, search, git
// reads, web, data, asking a human), and carries no product-specific vocabulary.
// Handlers support two modes: "mock" for testing and "real" for production use.
package workers

import (
	"context"
	"fmt"

	"github.com/castwell/forge/internal/agent/core"
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

	// LoadSkill loads a published SkillPack for skill.activate. When nil the
	// tool reports that no skill store is wired instead of inventing one.
	LoadSkill LoadSkillFunc

	// Retriever backs knowledge.search. When nil the tool reports that no
	// knowledge base is wired instead of returning empty results that would
	// look like "nothing exists".
	Retriever core.Retriever

	// DataSource is the PostgreSQL DSN data.query runs against. When empty the
	// tool reports it needs a database instead of returning mock rows.
	DataSource string

	// WebFetchAllowPrivate lifts the SSRF guard on web.fetch (dev servers
	// and httptest-based tests opt in; production stays guarded).
	WebFetchAllowPrivate bool

	// WebSearchProvider selects the web.search backend: "duckduckgo"
	// (default, zero config), "brave" (needs WebSearchAPIKey) or "off"
	// (honest not-configured answer). WebSearchEndpoint overrides the
	// provider endpoint so tests can point at httptest.
	WebSearchProvider string
	WebSearchEndpoint string
	WebSearchAPIKey   string
}

// ErrNotConfigured is returned when a real-mode handler is called but the
// underlying service is not configured. Each handler group documents its
// required external dependencies:
//   - data.*:          a database DSN (HandlerConfig.DataSource)
//   - web.fetch:       outbound network access (implemented, SSRF-guarded)
//   - web.search:      "off"/unknown provider, or brave without WebSearchAPIKey
//   - code.execute:    a code execution sandbox (deliberately deferred)
//   - ask.user:        an interactive channel (HandlerConfig.AskUser)
//   - skill.activate:  a skill store (HandlerConfig.LoadSkill)
//   - knowledge.search: a knowledge base (HandlerConfig.Retriever)
//
// Handlers with no external dependency - file.*, git.*, shell.run (whitelisted)
// - are implemented for real mode.
var ErrNotConfigured = fmt.Errorf("handler not configured: real mode requires external service setup")
