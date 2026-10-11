// Package acp implements the client side of the Agent Client Protocol.
//
// ACP lets a client drive an agent that runs as a separate program: the client
// launches it, speaks JSON-RPC 2.0 over its stdin/stdout, and receives streamed
// updates while a prompt turn runs. This package is that client, so a delegated
// subtask can be handed to an external agent — Codex, Claude Code, or anything
// else that speaks the protocol — without this project growing one adapter per
// implementation.
//
// Why a protocol instead of per-agent adapters: the shape of "run a subtask
// somewhere else and stream back what it did" is the same every time, and it is
// exactly what ACP standardises. An adapter per tool would be the same code
// written N times with N sets of quirks, and every new agent would need another.
//
// Scope, stated plainly: this is the CLIENT side. We drive an external agent; we
// do not serve as one. The file-system, terminal and elicitation capabilities
// are declared as UNSUPPORTED, because declaring a capability we do not
// implement is a lie the agent would act on.
package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"sync/atomic"
)

// ProtocolVersion is the ACP protocol version this client speaks.
//
// It is sent in `initialize` and the agent's answer is checked against it: a
// version the agent does not support is a negotiation failure, and continuing
// anyway would mean interpreting its messages by guesswork.
const ProtocolVersion = 1

// jsonrpcVersion is the only value JSON-RPC 2.0 allows.
const jsonrpcVersion = "2.0"

// Transport carries JSON-RPC messages to an agent.
//
// It is an interface because the protocol is transport-agnostic by design: stdio
// is the only one finalised today (Streamable HTTP is still a draft), and this
// project learned from MCP that a second transport arrives eventually. Naming
// the seam now costs nothing; retrofitting it means touching every call site.
type Transport interface {
	// Call sends a request and waits for its response.
	Call(ctx context.Context, method string, params any) (json.RawMessage, error)
	// Notify sends a notification. No response is expected or read.
	Notify(ctx context.Context, method string, params any) error
	// SetNotificationHandler registers the receiver for agent-initiated
	// notifications. It is a single handler rather than a channel because the
	// client routes every notification the same way: to the active turn.
	SetNotificationHandler(fn func(method string, params json.RawMessage))
	// Close shuts the transport down.
	Close() error
}

// rpcRequest is an outgoing request.
type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// rpcNotification is an outgoing notification: the same shape without an id.
type rpcNotification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// rpcError is a JSON-RPC error object.
type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// Error implements error so a JSON-RPC failure travels as one.
func (e *rpcError) Error() string {
	return fmt.Sprintf("json-rpc error %d: %s", e.Code, e.Message)
}

// StdioTransport speaks ACP over a child process's stdin and stdout.
//
// The wire format is newline-delimited JSON: one complete message per line, and
// a message must not contain an embedded newline (the protocol says so, and the
// encoder below guarantees it by emitting compact JSON).
type StdioTransport struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser

	// writeMu serializes stdin writes. Without it two concurrent calls interleave
	// their JSON frames on the same pipe and the agent reads a corrupted line.
	// MCP's transport carries the same lock for the same reason.
	writeMu sync.Mutex

	// pending maps a request id to the channel its response arrives on.
	pending sync.Map // int64 -> chan rpcResponse

	nextID atomic.Int64

	// notifyMu guards the handler, which is set before the first turn and read
	// by the read loop.
	notifyMu sync.RWMutex
	notify   func(method string, params json.RawMessage)

	closeOnce sync.Once
	done      chan struct{}
	readErr   error
}

// rpcResponse is an incoming response or notification. One struct covers both:
// a notification has no id and carries a method instead.
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// StdioConfig describes the agent process to launch.
type StdioConfig struct {
	// Command is the executable.
	Command string
	// Args are its arguments.
	Args []string
	// Env is the process environment (KEY=VALUE). Empty means inherit.
	Env []string
	// Dir is the working directory. Empty means inherit.
	Dir string
	// Stderr, when set, receives the agent's stderr. The protocol allows the
	// agent to log there, and discarding it makes a failing agent impossible to
	// diagnose — MCP's transport discards it, which this deliberately does not.
	Stderr io.Writer
}

// NewStdioTransport starts the agent process and begins reading its output.
func NewStdioTransport(cfg StdioConfig) (*StdioTransport, error) {
	if cfg.Command == "" {
		return nil, fmt.Errorf("acp: transport needs a command")
	}

	cmd := exec.Command(cfg.Command, cfg.Args...)
	if len(cfg.Env) > 0 {
		cmd.Env = cfg.Env
	}
	if cfg.Dir != "" {
		cmd.Dir = cfg.Dir
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("acp: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("acp: stdout pipe: %w", err)
	}
	if cfg.Stderr != nil {
		cmd.Stderr = cfg.Stderr
	} else {
		cmd.Stderr = io.Discard
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("acp: start %q: %w", cfg.Command, err)
	}

	t := &StdioTransport{
		cmd:    cmd,
		stdin:  stdin,
		stdout: stdout,
		done:   make(chan struct{}),
	}
	go t.readLoop()
	return t, nil
}

// SetNotificationHandler implements Transport.
func (t *StdioTransport) SetNotificationHandler(fn func(string, json.RawMessage)) {
	t.notifyMu.Lock()
	t.notify = fn
	t.notifyMu.Unlock()
}

// Call implements Transport.
func (t *StdioTransport) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := t.nextID.Add(1)
	ch := make(chan rpcResponse, 1)
	t.pending.Store(id, ch)
	defer t.pending.Delete(id)

	body, err := json.Marshal(rpcRequest{JSONRPC: jsonrpcVersion, ID: id, Method: method, Params: params})
	if err != nil {
		return nil, fmt.Errorf("acp: marshal %s: %w", method, err)
	}
	if err := t.write(body); err != nil {
		return nil, err
	}

	select {
	case resp := <-ch:
		if resp.Error != nil {
			return nil, resp.Error
		}
		return resp.Result, nil
	case <-t.done:
		// The process ended. Reporting its read error is more useful than a bare
		// cancellation: "the agent exited" is the fact, and the caller can act on
		// it.
		if t.readErr != nil {
			return nil, fmt.Errorf("acp: agent exited while waiting for %s: %w", method, t.readErr)
		}
		return nil, fmt.Errorf("acp: transport closed while waiting for %s", method)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Notify implements Transport.
func (t *StdioTransport) Notify(_ context.Context, method string, params any) error {
	body, err := json.Marshal(rpcNotification{JSONRPC: jsonrpcVersion, Method: method, Params: params})
	if err != nil {
		return fmt.Errorf("acp: marshal %s: %w", method, err)
	}
	return t.write(body)
}

// write sends one framed message.
func (t *StdioTransport) write(body []byte) error {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()

	select {
	case <-t.done:
		// The sentinel travels with the message so Classify reaches the process
		// class by errors.Is rather than by matching text.
		return fmt.Errorf("%w: cannot write", errTransportClosed)
	default:
	}

	// A trailing newline is the frame delimiter. json.Marshal emits compact JSON
	// with no embedded newlines, which is what the protocol requires — so the
	// delimiter is unambiguous.
	if _, err := t.stdin.Write(append(body, '\n')); err != nil {
		return fmt.Errorf("acp: write: %w", err)
	}
	return nil
}

// readLoop dispatches responses and notifications until stdout ends.
func (t *StdioTransport) readLoop() {
	defer func() {
		close(t.done)
		// Wake every waiter: a pending call must not hang until its context
		// expires when the answer can never arrive.
		t.pending.Range(func(key, value any) bool {
			if ch, ok := value.(chan rpcResponse); ok {
				close(ch)
			}
			return true
		})
	}()

	scanner := bufio.NewScanner(t.stdout)
	// ACP messages carry file contents and diffs, so a line can be large. The
	// default 64KB scanner limit would truncate them into parse errors.
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var msg rpcResponse
		if err := json.Unmarshal(line, &msg); err != nil {
			// The protocol says the agent must not write anything else to stdout.
			// A line that does not parse is recorded and skipped rather than
			// killing the connection: one stray log line should not end a run.
			t.readErr = fmt.Errorf("acp: unparseable message from agent: %w", err)
			continue
		}

		// A message with no id is a notification: it goes to the handler.
		if msg.ID == nil {
			if msg.Method != "" {
				t.notifyMu.RLock()
				fn := t.notify
				t.notifyMu.RUnlock()
				if fn != nil {
					fn(msg.Method, msg.Params)
				}
			}
			continue
		}

		if ch, ok := t.pending.Load(*msg.ID); ok {
			if respCh, ok := ch.(chan rpcResponse); ok {
				select {
				case respCh <- msg:
				default:
					// The caller already gave up (context expired). Dropping the
					// late response is correct: nobody is listening.
				}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		t.readErr = fmt.Errorf("acp: read agent stdout: %w", err)
	}
}

// Close terminates the transport and the agent process.
//
// Closing stdin first is the graceful signal (the agent sees EOF); the process
// is then killed if it does not exit on its own. Waiting forever for a
// misbehaving agent would hang shutdown, which is the failure mode that makes
// "stop" untrustworthy.
func (t *StdioTransport) Close() error {
	var err error
	t.closeOnce.Do(func() {
		_ = t.stdin.Close()
		if t.cmd.Process != nil {
			_ = t.cmd.Process.Kill()
		}
		_ = t.cmd.Wait()
	})
	return err
}

// Wait blocks until the agent's output ends.
func (t *StdioTransport) Wait() {
	<-t.done
}
