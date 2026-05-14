package mcp

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

// Transport is the interface for sending/receiving JSON-RPC messages.
// Different transports (stdio, HTTP+SSE, etc.) implement this interface.
type Transport interface {
	// Send sends a JSON-RPC request and waits for the response.
	Send(ctx context.Context, req Request) (Response, error)
	// Notify sends a JSON-RPC notification (no response expected).
	Notify(ctx context.Context, n Notification) error
	// Close shuts down the transport.
	Close() error
}

// StdioTransport communicates with an MCP server via stdin/stdout of a child process.
// Each line on stdout is a complete JSON-RPC message (newline-delimited JSON).
type StdioTransport struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser

	scanner *bufio.Scanner

	// pending maps request IDs to response channels.
	pending sync.Map // int64 -> chan Response

	nextID atomic.Int64
	done   chan struct{}
	closeOnce sync.Once
}

// StdioConfig holds configuration for starting an MCP server process.
type StdioConfig struct {
	Command string   // executable path
	Args    []string // command arguments
	Env     []string // optional environment variables (KEY=VALUE)
}

// NewStdioTransport starts the MCP server as a child process and returns a Transport.
func NewStdioTransport(cfg StdioConfig) (*StdioTransport, error) {
	cmd := exec.Command(cfg.Command, cfg.Args...)
	if len(cfg.Env) > 0 {
		cmd.Env = cfg.Env
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("create stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("create stdout pipe: %w", err)
	}

	// Discard stderr to prevent blocking. In production, could log it.
	cmd.Stderr = io.Discard

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start MCP server %q: %w", cfg.Command, err)
	}

	t := &StdioTransport{
		cmd:     cmd,
		stdin:   stdin,
		stdout:  stdout,
		scanner: bufio.NewScanner(stdout),
		done:    make(chan struct{}),
	}

	// Set scanner buffer to 1MB for large tool results.
	t.scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

	go t.readLoop()

	return t, nil
}

// Send sends a request and blocks until the matching response arrives or ctx is cancelled.
func (t *StdioTransport) Send(ctx context.Context, req Request) (Response, error) {
	ch := make(chan Response, 1)
	t.pending.Store(req.ID, ch)
	defer t.pending.Delete(req.ID)

	if err := t.writeMessage(req); err != nil {
		return Response{}, fmt.Errorf("write request: %w", err)
	}

	select {
	case <-ctx.Done():
		return Response{}, ctx.Err()
	case <-t.done:
		return Response{}, fmt.Errorf("transport closed")
	case resp := <-ch:
		return resp, nil
	}
}

// Notify sends a notification (fire-and-forget).
func (t *StdioTransport) Notify(_ context.Context, n Notification) error {
	return t.writeMessage(n)
}

// Close terminates the child process and cleans up.
func (t *StdioTransport) Close() error {
	var closeErr error
	t.closeOnce.Do(func() {
		close(t.done)
		_ = t.stdin.Close()

		// Give the process a chance to exit gracefully.
		if t.cmd.Process != nil {
			_ = t.cmd.Process.Kill()
		}
		closeErr = t.cmd.Wait()
	})
	return closeErr
}

// NextID returns the next unique request ID.
func (t *StdioTransport) NextID() int64 {
	return t.nextID.Add(1)
}

// readLoop reads lines from stdout, parses them as JSON-RPC responses,
// and dispatches to waiting callers.
func (t *StdioTransport) readLoop() {
	for t.scanner.Scan() {
		line := t.scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		resp, err := ParseResponse(line)
		if err != nil {
			// Could be a notification from server — skip for now.
			continue
		}

		if ch, ok := t.pending.Load(resp.ID); ok {
			ch.(chan Response) <- resp
		}
	}
}

// writeMessage serializes and writes a JSON message followed by a newline.
func (t *StdioTransport) writeMessage(msg json.Marshaler) error {
	data, err := msg.MarshalJSON()
	if err != nil {
		return fmt.Errorf("marshal message: %w", err)
	}
	data = append(data, '\n')

	_, err = t.stdin.Write(data)
	return err
}
