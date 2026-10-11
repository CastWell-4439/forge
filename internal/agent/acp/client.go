package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/castwell/forge/internal/agent/harness"
)

// The ACP client: handshake, session, prompt turn.
//
// The methods and their shapes follow the protocol's v1 documentation:
//
//	initialize      protocolVersion + clientCapabilities + clientInfo
//	session/new     create a session, get its id
//	session/prompt  send a turn; the answer arrives as session/update
//	                  notifications and the call returns the stop reason
//	session/cancel  interrupt the turn in progress
//
// What this file adds on top of the wire format is the failure handling, which
// is where a remote agent differs from a local loop: a local child fails with a
// Go error, while a remote one can fail at the protocol level (a version it does
// not speak), the process level (it died), the transport level (it stopped
// answering) or the semantic level (it answered with a refusal). Those are not
// interchangeable, so they are classified rather than collapsed into one error.

// StopReason is why a prompt turn ended.
type StopReason string

const (
	// StopEndTurn is a normal completion.
	StopEndTurn StopReason = "end_turn"
	// StopMaxTokens means the agent hit its own limit.
	StopMaxTokens StopReason = "max_tokens"
	// StopMaxTurnRequests means the agent hit a request limit.
	StopMaxTurnRequests StopReason = "max_turn_requests"
	// StopRefusal means the agent declined the task.
	StopRefusal StopReason = "refusal"
	// StopCancelled means the turn was cancelled.
	StopCancelled StopReason = "cancelled"
)

// FailureClass says what KIND of failure this is, because the right response
// differs per kind and collapsing them loses the distinction.
type FailureClass string

const (
	// FailureProtocol is a negotiation failure: the agent does not speak this
	// version or lacks a required capability. Retrying cannot change a
	// negotiation, so it is never retryable.
	FailureProtocol FailureClass = "protocol"
	// FailureProcess is the agent process ending (crash, exit, stdout closed).
	// Retrying starts a new process, which may well work — but the caller
	// decides, because a crash loop is worse than a failure.
	FailureProcess FailureClass = "process"
	// FailureTimeout is the agent not answering within the turn's deadline.
	FailureTimeout FailureClass = "timeout"
	// FailureSemantic is the agent answering with an error or a refusal: the
	// protocol worked and the agent said no.
	FailureSemantic FailureClass = "semantic"
)

// Error is an ACP failure with its class attached.
//
// The class is the point: a caller that wants to retry timeouts but not protocol
// failures needs to tell them apart, and matching on message text would be a
// guess. Retryable is filled from the class, so the two can never disagree.
type Error struct {
	Class FailureClass
	// Method is the ACP method that failed, when one was in flight.
	Method string
	// Err is the underlying cause.
	Err error
	// Retryable says whether retrying the same call could plausibly succeed.
	Retryable bool
}

func (e *Error) Error() string {
	if e.Method != "" {
		return fmt.Sprintf("acp %s: %s: %v", e.Method, e.Class, e.Err)
	}
	return fmt.Sprintf("acp: %s: %v", e.Class, e.Err)
}

// Unwrap exposes the cause, so errors.Is/As reach the transport error.
func (e *Error) Unwrap() error { return e.Err }

// Classify determines a failure's class from the error itself.
//
// It is exported because the delegation layer needs the same judgement when it
// decides whether to retry, and a second implementation would eventually
// disagree with this one.
func Classify(err error) FailureClass {
	if err == nil {
		return ""
	}

	// An ACP failure already knows its class. Checking this FIRST matters: the
	// class was decided where the failure happened, with more context than the
	// error text carries, and re-deriving it from the message would sometimes
	// land on a different answer — which is how a protocol failure would come
	// back as retryable.
	var acpErr *Error
	if errors.As(err, &acpErr) {
		return acpErr.Class
	}

	// A refusal or an agent-reported error is the protocol working correctly.
	var rpcErr *rpcError
	if errors.As(err, &rpcErr) {
		return FailureSemantic
	}

	// Deadline and cancellation are the caller's clock, not the agent's fault.
	if errors.Is(err, context.DeadlineExceeded) {
		return FailureTimeout
	}

	// A closed transport or a dead process.
	if errors.Is(err, errTransportClosed) {
		return FailureProcess
	}
	msg := err.Error()
	if strings.Contains(msg, "agent exited") || strings.Contains(msg, "transport closed") ||
		strings.Contains(msg, "transport is closed") ||
		strings.Contains(msg, "broken pipe") || strings.Contains(msg, "file already closed") {
		return FailureProcess
	}
	if errors.Is(err, context.Canceled) {
		return FailureTimeout
	}

	// Everything else is treated as semantic: it reached the agent and came back
	// as a failure the agent is responsible for. Guessing "retryable" here would
	// be the unsafe direction.
	return FailureSemantic
}

// errTransportClosed marks a transport that is no longer usable.
var errTransportClosed = errors.New("acp: transport closed")

// retryableFor says whether a class may be retried.
//
// Protocol failures never are: the version and capabilities are fixed for the
// life of the connection, so a retry repeats the same rejection. Everything else
// can be, which is why the class rather than the message decides.
func retryableFor(class FailureClass) bool {
	switch class {
	case FailureProtocol:
		return false
	case FailureProcess, FailureTimeout, FailureSemantic:
		return true
	default:
		return false
	}
}

// ClientInfo identifies this client during the handshake.
type ClientInfo struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version,omitempty"`
}

// AgentCapabilities is what the agent reported during the handshake.
//
// Only the fields this client reads are modelled. An unknown capability is
// simply absent, which is the right default: a client must not assume a
// capability the agent did not announce.
type AgentCapabilities struct {
	LoadSession        bool `json:"loadSession,omitempty"`
	PromptCapabilities *struct {
		Image           bool `json:"image,omitempty"`
		Audio           bool `json:"audio,omitempty"`
		EmbeddedContext bool `json:"embeddedContext,omitempty"`
	} `json:"promptCapabilities,omitempty"`
}

// AgentInfo identifies the agent, as reported during the handshake.
type AgentInfo struct {
	Name    string `json:"name,omitempty"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version,omitempty"`
}

// InitializeResult is the handshake response.
type InitializeResult struct {
	ProtocolVersion   int               `json:"protocolVersion"`
	AgentCapabilities AgentCapabilities `json:"agentCapabilities"`
	AgentInfo         AgentInfo         `json:"agentInfo"`
	AuthMethods       []json.RawMessage `json:"authMethods,omitempty"`
}

// SessionUpdate is one streamed notification during a turn.
//
// The protocol defines a dozen variants and this client reads the three that
// carry the answer and the plan: agent message chunks, thought chunks, and tool
// call activity. The rest are counted and ignored rather than modelled, because
// a client that does not act on them gains nothing from parsing them.
type SessionUpdate struct {
	// SessionUpdate names the variant (agent_message_chunk, tool_call, ...).
	SessionUpdate string `json:"sessionUpdate"`
	// Content is present on message and thought chunks.
	Content *struct {
		Type string `json:"type"`
		Text string `json:"text,omitempty"`
	} `json:"content,omitempty"`
	// Title and Status are present on tool_call notifications.
	Title  string `json:"title,omitempty"`
	Status string `json:"status,omitempty"`
	// ToolCallID correlates a tool_call with its updates.
	ToolCallID string `json:"toolCallId,omitempty"`
}

// Client drives one ACP agent.
type Client struct {
	transport Transport
	info      ClientInfo
	// timeout bounds each call. Zero means no per-call deadline, which is only
	// sensible when the caller's context already carries one.
	timeout time.Duration

	// init guards the handshake so it happens once.
	initOnce sync.Once
	initRes  *InitializeResult
	initErr  error

	// sessionID is the session created by NewSession.
	sessionID string

	// turn collects the updates for the turn in flight. Updates arrive on the
	// transport's read goroutine, so access is guarded.
	turnMu   sync.Mutex
	turnText strings.Builder
	turnSeen map[string]int
}

// Config configures the client.
type Config struct {
	// Transport carries the messages. Required.
	Transport Transport
	// Info identifies this client in the handshake.
	Info ClientInfo
	// Timeout bounds each call. Zero means the caller's context is the only
	// deadline.
	Timeout time.Duration
}

// NewClient creates a client. The handshake happens on first use rather than
// here, so constructing a client cannot fail for a reason the caller cannot
// handle yet.
func NewClient(cfg Config) *Client {
	info := cfg.Info
	if info.Name == "" {
		info.Name = "forge"
	}
	return &Client{
		transport: cfg.Transport,
		info:      info,
		timeout:   cfg.Timeout,
	}
}

// clientCapabilities is what this client declares it can do.
//
// Everything is false, and that is deliberate: the protocol has the agent ask
// the client to read files, write files, run terminals and elicit input. This
// client does none of those, so it declares none of them — an agent that asks
// for an undeclared capability is misbehaving, and one that respects the
// declaration simply will not ask. Declaring a capability to look capable is
// how a client ends up failing requests it invited.
type clientCapabilities struct {
	FS struct {
		ReadTextFile  bool `json:"readTextFile"`
		WriteTextFile bool `json:"writeTextFile"`
	} `json:"fs"`
	Terminal bool `json:"terminal"`
}

// Initialize performs the handshake, once.
func (c *Client) Initialize(ctx context.Context) (*InitializeResult, error) {
	c.initOnce.Do(func() {
		var caps clientCapabilities
		caps.FS.ReadTextFile = false
		caps.FS.WriteTextFile = false
		caps.Terminal = false

		params := map[string]any{
			"protocolVersion":    ProtocolVersion,
			"clientCapabilities": caps,
			"clientInfo":         c.info,
		}

		raw, err := c.call(ctx, "initialize", params)
		if err != nil {
			c.initErr = &Error{Class: Classify(err), Method: "initialize", Err: err, Retryable: retryableFor(Classify(err))}
			return
		}

		var res InitializeResult
		if err := json.Unmarshal(raw, &res); err != nil {
			c.initErr = &Error{
				Class: FailureProtocol, Method: "initialize",
				Err: fmt.Errorf("unreadable result: %w", err), Retryable: false,
			}
			return
		}

		// The version is checked, not assumed. An agent that answers with a
		// version this client does not speak would have its later messages
		// interpreted by guesswork, and the failure would surface far from its
		// cause — so it is refused here, where the reason is obvious.
		if res.ProtocolVersion != ProtocolVersion {
			c.initErr = &Error{
				Class: FailureProtocol, Method: "initialize", Retryable: false,
				Err: fmt.Errorf("agent speaks protocol version %d, this client speaks %d",
					res.ProtocolVersion, ProtocolVersion),
			}
			return
		}

		c.initRes = &res
	})
	return c.initRes, c.initErr
}

// NewSession creates a session and remembers its id.
//
// The working directory is sent because an agent that edits code needs to know
// which tree it is in; this client has no filesystem capability, so it cannot
// answer that question later.
func (c *Client) NewSession(ctx context.Context, cwd string) (string, error) {
	if _, err := c.Initialize(ctx); err != nil {
		return "", err
	}

	params := map[string]any{"cwd": cwd, "mcpServers": []any{}}
	raw, err := c.call(ctx, "session/new", params)
	if err != nil {
		return "", err
	}

	var res struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return "", &Error{
			Class: FailureProtocol, Method: "session/new",
			Err: fmt.Errorf("unreadable result: %w", err), Retryable: false,
		}
	}
	if res.SessionID == "" {
		return "", &Error{
			Class: FailureProtocol, Method: "session/new", Retryable: false,
			Err: fmt.Errorf("agent returned no session id"),
		}
	}

	c.sessionID = res.SessionID
	return res.SessionID, nil
}

// Prompt sends a turn and returns its outcome as a RunResult.
//
// The updates stream in while the call is outstanding, so the handler is
// installed before the request goes out — installing it after would drop the
// first chunks, which are often the only ones a short turn produces.
func (c *Client) Prompt(ctx context.Context, text string) (*harness.RunResult, error) {
	if _, err := c.Initialize(ctx); err != nil {
		return nil, err
	}
	if c.sessionID == "" {
		return nil, &Error{
			Class: FailureProtocol, Method: "session/prompt", Retryable: false,
			Err: fmt.Errorf("no session: call NewSession first"),
		}
	}

	c.resetTurn()
	c.transport.SetNotificationHandler(c.onNotification)

	params := map[string]any{
		"sessionId": c.sessionID,
		"prompt": []map[string]any{
			{"type": "text", "text": text},
		},
	}

	raw, err := c.call(ctx, "session/prompt", params)
	if err != nil {
		return nil, err
	}

	var res struct {
		StopReason string `json:"stopReason"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, &Error{
			Class: FailureProtocol, Method: "session/prompt",
			Err: fmt.Errorf("unreadable result: %w", err), Retryable: false,
		}
	}

	c.turnMu.Lock()
	answer := c.turnText.String()
	c.turnMu.Unlock()

	return &harness.RunResult{
		Answer: answer,
		Reason: mapStopReason(StopReason(res.StopReason)),
	}, nil
}

// Cancel asks the agent to stop the turn in progress.
//
// It is a notification, so it is fire-and-forget: waiting for an answer to a
// cancellation would make cancelling as slow as the thing being cancelled.
func (c *Client) Cancel(ctx context.Context) error {
	if c.sessionID == "" {
		return nil
	}
	return c.transport.Notify(ctx, "session/cancel", map[string]any{"sessionId": c.sessionID})
}

// onNotification routes a streamed update into the current turn.
func (c *Client) onNotification(method string, params json.RawMessage) {
	if method != "session/update" {
		return
	}

	var envelope struct {
		Update SessionUpdate `json:"update"`
	}
	if err := json.Unmarshal(params, &envelope); err != nil {
		return
	}
	update := envelope.Update

	c.turnMu.Lock()
	defer c.turnMu.Unlock()
	if c.turnSeen == nil {
		c.turnSeen = map[string]int{}
	}
	c.turnSeen[update.SessionUpdate]++

	// Only agent output becomes the answer. A thought chunk is the agent's
	// reasoning and a tool call is activity: folding either into the answer
	// would hand the caller something the agent did not say.
	if update.SessionUpdate == "agent_message_chunk" && update.Content != nil {
		c.turnText.WriteString(update.Content.Text)
	}
}

// resetTurn clears the per-turn accumulation.
func (c *Client) resetTurn() {
	c.turnMu.Lock()
	c.turnText.Reset()
	c.turnSeen = map[string]int{}
	c.turnMu.Unlock()
}

// TurnCounts reports how many updates of each variant the last turn produced.
// It exists for diagnostics: a turn that produced only tool calls and no message
// is a different situation from one that produced nothing at all.
func (c *Client) TurnCounts() map[string]int {
	c.turnMu.Lock()
	defer c.turnMu.Unlock()
	out := make(map[string]int, len(c.turnSeen))
	for k, v := range c.turnSeen {
		out[k] = v
	}
	return out
}

// AgentInfo reports what the agent said about itself during the handshake.
//
// It returns a zero value before Initialize, which is the honest answer: nothing
// has been negotiated yet, and inventing a name would put a guess into a log
// line an operator is using to find out what is actually running.
func (c *Client) AgentInfo() AgentInfo {
	if c.initRes == nil {
		return AgentInfo{}
	}
	return c.initRes.AgentInfo
}

// Capabilities reports what the agent declared it can do.
func (c *Client) Capabilities() AgentCapabilities {
	if c.initRes == nil {
		return AgentCapabilities{}
	}
	return c.initRes.AgentCapabilities
}

// call wraps a transport call with the timeout and the failure classification.
func (c *Client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if c.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}

	raw, err := c.transport.Call(ctx, method, params)
	if err != nil {
		class := Classify(err)
		return nil, &Error{Class: class, Method: method, Err: err, Retryable: retryableFor(class)}
	}
	return raw, nil
}

// mapStopReason turns an ACP stop reason into the harness vocabulary.
//
// The mapping is explicit rather than a passthrough because the two vocabularies
// answer different questions: ACP says why the TURN ended, the harness says why
// a RUN stopped. "refusal" is the one that matters most — it is a completed turn
// with a negative outcome, and reporting it as "completed" would tell the caller
// the task was done.
func mapStopReason(reason StopReason) string {
	switch reason {
	case StopEndTurn:
		return "completed"
	case StopMaxTokens:
		return "max_tokens"
	case StopMaxTurnRequests:
		return "max_turn_requests"
	case StopRefusal:
		return "refused"
	case StopCancelled:
		return "cancelled"
	default:
		// An unknown reason is reported as itself rather than as "completed":
		// guessing success from an unfamiliar word is the wrong direction.
		return "unknown:" + string(reason)
	}
}
