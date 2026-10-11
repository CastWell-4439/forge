package acp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/castwell/forge/internal/agent/harness"
)

// ChildRunner adapts an ACP client to the delegation contract.
//
// This is what the interface lift in harness was for. A child running under ACP
// executes its own loop in another process; it is not a harness.AgentLoop and
// could not honestly pretend to be one. The delegation runner only needs three
// things from a child — run a task, continue a session, be released — and this
// type supplies them over the protocol.
//
// The adapter is deliberately thin. Everything interesting (transport, framing,
// handshake, failure classification) lives in the client, because those are
// properties of speaking to a remote agent rather than of being a delegated
// child. A second protocol would add a second adapter of this size, not a second
// delegation mechanism.

// RunnerConfig describes how to reach an ACP agent.
type RunnerConfig struct {
	// Transport carries the messages. Required.
	Transport Transport
	// Info identifies this client in the handshake.
	Info ClientInfo
	// CWD is the working directory reported to the agent. An agent that edits
	// code needs to know which tree it is in, and this client has no filesystem
	// capability to answer later.
	CWD string
	// Timeout bounds each call.
	Timeout time.Duration
}

// ChildRunner implements harness.ChildRunner over ACP.
type ChildRunner struct {
	client    *Client
	cwd       string
	sessionID string
	// started guards the one-time session creation.
	started bool
}

// NewChildRunner builds a runner and performs the handshake.
//
// The handshake is done here rather than on the first task because a protocol
// failure — the agent not speaking this version — should surface where the
// deployment is being set up, not in the middle of a run where it looks like a
// task failure.
func NewChildRunner(ctx context.Context, cfg RunnerConfig) (*ChildRunner, error) {
	if cfg.Transport == nil {
		return nil, fmt.Errorf("acp: runner needs a transport")
	}

	client := NewClient(Config{
		Transport: cfg.Transport,
		Info:      cfg.Info,
		Timeout:   cfg.Timeout,
	})

	if _, err := client.Initialize(ctx); err != nil {
		return nil, err
	}

	return &ChildRunner{client: client, cwd: cfg.CWD}, nil
}

// ensureSession creates the session on first use.
func (r *ChildRunner) ensureSession(ctx context.Context) error {
	if r.started {
		return nil
	}
	sessionID, err := r.client.NewSession(ctx, r.cwd)
	if err != nil {
		return err
	}
	r.sessionID = sessionID
	r.started = true
	return nil
}

// Run implements harness.ChildRunner.
//
// The session id the delegation layer passes in is recorded but not sent: an ACP
// session id is minted by the AGENT, so the caller's id names the delegation
// while the agent's names its own session. Trying to force one to be the other
// would mean either inventing an id the agent never issued or losing the
// caller's handle on the child.
func (r *ChildRunner) Run(ctx context.Context, sessionID string, task string) (*harness.RunResult, error) {
	if err := r.ensureSession(ctx); err != nil {
		return nil, err
	}
	_ = sessionID

	result, err := r.client.Prompt(ctx, task)
	if err != nil {
		return nil, err
	}

	// A refusal is a completed turn with a negative outcome. It is returned as a
	// result, not an error, because the caller needs to distinguish "the agent
	// said no" from "the agent broke" — and only the reason field can carry that
	// distinction.
	return result, nil
}

// Continue implements harness.ChildRunner by sending another turn on the same
// session.
//
// This is where ACP and the local child differ in a way worth naming: a local
// child's Continue re-enters a loop whose history this process holds, while an
// ACP Continue sends a second prompt to an agent that keeps its own history.
// The caller sees the same thing, which is the point of the interface.
func (r *ChildRunner) Continue(ctx context.Context, sessionID string, userInput string) (*harness.RunResult, error) {
	if err := r.ensureSession(ctx); err != nil {
		return nil, err
	}
	_ = sessionID
	return r.client.Prompt(ctx, userInput)
}

// SessionID reports the agent's session id, for logs and for a caller that wants
// to address the session directly.
func (r *ChildRunner) SessionID() string { return r.sessionID }

// Close ends the conversation with the agent.
//
// It cancels the turn in flight before closing, so an agent that is mid-task
// learns the work is abandoned rather than being killed without explanation.
// The cancel is best-effort: a transport that is already gone cannot deliver it,
// and failing Close over that would leave the process running.
func (r *ChildRunner) Close(ctx context.Context) error {
	if r.sessionID != "" {
		_ = r.client.Cancel(ctx)
	}
	return nil
}

// Describe summarises the agent for a log line, so an operator can tell which
// implementation a delegation went to without reading the config.
func (r *ChildRunner) Describe() string {
	info := r.client.AgentInfo()
	if info.Name == "" && info.Version == "" {
		return "acp agent (unnamed)"
	}
	parts := make([]string, 0, 2)
	if info.Name != "" {
		parts = append(parts, info.Name)
	}
	if info.Version != "" {
		parts = append(parts, info.Version)
	}
	return "acp agent " + strings.Join(parts, " ")
}
