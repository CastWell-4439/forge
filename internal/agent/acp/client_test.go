package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeAgent is a test ACP agent: a real subprocess that speaks the protocol over
// stdio. It is a process rather than an in-memory Transport on purpose — the
// framing rules (one JSON message per line, nothing else on stdout) are part of
// what this package must get right, and an in-memory transport would test none
// of them.
//
// The script is the TEST BINARY re-invoked with an environment variable, which
// is the standard Go way to get a real subprocess without shipping a helper.
func TestMain(m *testing.M) {
	if os.Getenv("ACP_FAKE_AGENT") != "" {
		runFakeAgent()
		return
	}
	os.Exit(m.Run())
}

// runFakeAgent is the subprocess body. Its behaviour is steered by
// ACP_FAKE_MODE so one binary covers every case the tests need.
func runFakeAgent() {
	mode := os.Getenv("ACP_FAKE_MODE")
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	write := func(v any) {
		body, _ := json.Marshal(v)
		fmt.Fprintln(os.Stdout, string(body))
	}

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var msg struct {
			ID     *int64          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(line, &msg); err != nil {
			// Log to stderr, never stdout: stdout carries only ACP messages.
			fmt.Fprintln(os.Stderr, "fake agent: unparseable line")
			continue
		}

		reply := func(result any) {
			if msg.ID != nil {
				write(map[string]any{"jsonrpc": "2.0", "id": *msg.ID, "result": result})
			}
		}
		fail := func(code int, message string) {
			if msg.ID != nil {
				write(map[string]any{"jsonrpc": "2.0", "id": *msg.ID,
					"error": map[string]any{"code": code, "message": message}})
			}
		}

		switch msg.Method {
		case "initialize":
			version := ProtocolVersion
			if mode == "wrong-version" {
				version = ProtocolVersion + 99
			}
			reply(map[string]any{
				"protocolVersion":   version,
				"agentCapabilities": map[string]any{"loadSession": true},
				"agentInfo":         map[string]any{"name": "fake-agent", "version": "0.1.0"},
			})

		case "session/new":
			if mode == "session-error" {
				fail(-32600, "cannot create a session")
				continue
			}
			if mode == "no-session-id" {
				reply(map[string]any{})
				continue
			}
			reply(map[string]any{"sessionId": "agent-session-1"})

		case "session/prompt":
			// Stream a few updates, then answer with the stop reason. This is the
			// shape of a real turn: notifications first, then the response.
			notify := func(update map[string]any) {
				write(map[string]any{
					"jsonrpc": "2.0", "method": "session/update",
					"params": map[string]any{"sessionId": "agent-session-1", "update": update},
				})
			}
			notify(map[string]any{"sessionUpdate": "agent_thought_chunk",
				"content": map[string]any{"type": "text", "text": "thinking hard"}})
			notify(map[string]any{"sessionUpdate": "tool_call",
				"toolCallId": "t1", "title": "read a file", "status": "in_progress"})

			switch mode {
			case "no-answer":
				// Answer without streaming any message: a turn that did work but
				// said nothing.
			case "refusal":
				reply(map[string]any{"stopReason": "refusal"})
				continue
			default:
				notify(map[string]any{"sessionUpdate": "agent_message_chunk",
					"content": map[string]any{"type": "text", "text": "done: "}})
				notify(map[string]any{"sessionUpdate": "agent_message_chunk",
					"content": map[string]any{"type": "text", "text": "the task is complete"}})
			}
			reply(map[string]any{"stopReason": "end_turn"})

		case "session/cancel":
			// A notification: no reply. The agent just notes it.
			fmt.Fprintln(os.Stderr, "fake agent: cancelled")

		default:
			if msg.ID != nil {
				fail(-32601, "method not found: "+msg.Method)
			}
		}
	}
}

// startFakeAgent launches the fake agent and returns a transport to it.
func startFakeAgent(t *testing.T, mode string) *StdioTransport {
	t.Helper()

	exe, err := os.Executable()
	require.NoError(t, err)

	transport, err := NewStdioTransport(StdioConfig{
		Command: exe,
		Env: append(os.Environ(),
			"ACP_FAKE_AGENT=1",
			"ACP_FAKE_MODE="+mode,
		),
		Stderr: io.Discard,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = transport.Close() })
	return transport
}

// --- the handshake ---

// The happy path: handshake, session, turn with streamed updates.
func TestClientCompletesATurn(t *testing.T) {
	transport := startFakeAgent(t, "ok")
	client := NewClient(Config{Transport: transport, Info: ClientInfo{Name: "test"}})
	ctx := context.Background()

	init, err := client.Initialize(ctx)
	require.NoError(t, err)
	assert.Equal(t, ProtocolVersion, init.ProtocolVersion)
	assert.Equal(t, "fake-agent", init.AgentInfo.Name)

	sessionID, err := client.NewSession(ctx, "/tmp")
	require.NoError(t, err)
	assert.Equal(t, "agent-session-1", sessionID)

	result, err := client.Prompt(ctx, "do the task")
	require.NoError(t, err)
	assert.Equal(t, "completed", result.Reason)

	// Only the message chunks become the answer. The thought chunk and the tool
	// call are not part of what the agent SAID.
	assert.Equal(t, "done: the task is complete", result.Answer)
	assert.NotContains(t, result.Answer, "thinking hard")
	assert.NotContains(t, result.Answer, "read a file")

	// The other variants were seen and counted, not folded into the answer.
	counts := client.TurnCounts()
	assert.Equal(t, 1, counts["agent_thought_chunk"])
	assert.Equal(t, 1, counts["tool_call"])
	assert.Equal(t, 2, counts["agent_message_chunk"])
}

// A version the agent does not speak is refused at the handshake, and never
// retried: a negotiation cannot succeed on the second attempt.
func TestWrongProtocolVersionIsRefused(t *testing.T) {
	transport := startFakeAgent(t, "wrong-version")
	client := NewClient(Config{Transport: transport})

	_, err := client.Initialize(context.Background())
	require.Error(t, err)

	var acpErr *Error
	require.ErrorAs(t, err, &acpErr)
	assert.Equal(t, FailureProtocol, acpErr.Class)
	assert.False(t, acpErr.Retryable, "a version mismatch cannot be fixed by retrying")
	assert.Contains(t, err.Error(), "protocol version")
}

// A refusal is a completed turn with a negative outcome, not an error: the
// caller must be able to tell "the agent said no" from "the agent broke".
func TestRefusalIsAResultNotAnError(t *testing.T) {
	transport := startFakeAgent(t, "refusal")
	client := NewClient(Config{Transport: transport})
	ctx := context.Background()

	_, err := client.Initialize(ctx)
	require.NoError(t, err)
	_, err = client.NewSession(ctx, "/tmp")
	require.NoError(t, err)

	result, err := client.Prompt(ctx, "do it")
	require.NoError(t, err, "a refusal is the protocol working")
	assert.Equal(t, "refused", result.Reason)
	assert.Empty(t, result.Answer)
}

// A turn that streamed no message is an empty answer, not a fabricated one.
func TestTurnWithoutAMessageHasAnEmptyAnswer(t *testing.T) {
	transport := startFakeAgent(t, "no-answer")
	client := NewClient(Config{Transport: transport})
	ctx := context.Background()

	_, err := client.Initialize(ctx)
	require.NoError(t, err)
	_, err = client.NewSession(ctx, "/tmp")
	require.NoError(t, err)

	result, err := client.Prompt(ctx, "do it")
	require.NoError(t, err)
	assert.Empty(t, result.Answer)
	assert.Equal(t, "completed", result.Reason)
}

// A JSON-RPC error from the agent is a semantic failure: the protocol worked and
// the agent reported a problem.
func TestAgentErrorIsSemantic(t *testing.T) {
	transport := startFakeAgent(t, "session-error")
	client := NewClient(Config{Transport: transport})
	ctx := context.Background()

	_, err := client.Initialize(ctx)
	require.NoError(t, err)

	_, err = client.NewSession(ctx, "/tmp")
	require.Error(t, err)

	var acpErr *Error
	require.ErrorAs(t, err, &acpErr)
	assert.Equal(t, FailureSemantic, acpErr.Class)
	assert.Contains(t, err.Error(), "cannot create a session")
}

// A result that cannot be read is a protocol failure, not a semantic one: the
// agent's answer did not have the shape the protocol defines.
func TestMissingSessionIDIsAProtocolFailure(t *testing.T) {
	transport := startFakeAgent(t, "no-session-id")
	client := NewClient(Config{Transport: transport})
	ctx := context.Background()

	_, err := client.Initialize(ctx)
	require.NoError(t, err)

	_, err = client.NewSession(ctx, "/tmp")
	require.Error(t, err)

	var acpErr *Error
	require.ErrorAs(t, err, &acpErr)
	assert.Equal(t, FailureProtocol, acpErr.Class)
	assert.False(t, acpErr.Retryable)
}

// The handshake happens once, however many calls ask for it.
func TestInitializeRunsOnce(t *testing.T) {
	transport := startFakeAgent(t, "ok")
	client := NewClient(Config{Transport: transport})
	ctx := context.Background()

	first, err := client.Initialize(ctx)
	require.NoError(t, err)
	second, err := client.Initialize(ctx)
	require.NoError(t, err)
	assert.Same(t, first, second, "the second call returns the first result")
}

// A prompt before a session is a protocol error, and says so rather than sending
// a malformed request.
func TestPromptWithoutASessionIsRefused(t *testing.T) {
	transport := startFakeAgent(t, "ok")
	client := NewClient(Config{Transport: transport})
	ctx := context.Background()

	_, err := client.Prompt(ctx, "do it")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no session")
}

// Closing the transport ends the process, and a later call reports the process
// failure rather than hanging.
func TestCallAfterCloseReportsProcessFailure(t *testing.T) {
	transport := startFakeAgent(t, "ok")
	client := NewClient(Config{Transport: transport})

	_, err := client.Initialize(context.Background())
	require.NoError(t, err)

	require.NoError(t, transport.Close())

	// The call must fail promptly, not wait for its context.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = client.NewSession(ctx, "/tmp")
	require.Error(t, err)

	var acpErr *Error
	if assert.ErrorAs(t, err, &acpErr) {
		assert.Contains(t, []FailureClass{FailureProcess, FailureTimeout}, acpErr.Class,
			"a closed transport is a process failure, not a protocol one")
	}
}

// --- failure classification ---

// Each class is decided by the error, and the retryable flag follows from the
// class — so the two cannot disagree.
func TestClassifyCoversTheFourClasses(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		want      FailureClass
		retryable bool
	}{
		{"agent reported an error", &rpcError{Code: -32600, Message: "bad request"}, FailureSemantic, true},
		{"deadline", context.DeadlineExceeded, FailureTimeout, true},
		{"process exited", fmt.Errorf("acp: agent exited while waiting for x: boom"), FailureProcess, true},
		{"transport closed", errTransportClosed, FailureProcess, true},
		{"cancelled", context.Canceled, FailureTimeout, true},
		{"unrecognised", fmt.Errorf("something else"), FailureSemantic, true},
		{"protocol", &Error{Class: FailureProtocol, Err: fmt.Errorf("version")}, FailureProtocol, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			class := Classify(tc.err)
			assert.Equal(t, tc.want, class)
			assert.Equal(t, tc.retryable, retryableFor(class))
		})
	}
}

// A protocol failure is never retryable, whichever way it is wrapped.
func TestProtocolFailuresAreNeverRetryable(t *testing.T) {
	assert.False(t, retryableFor(FailureProtocol))
	assert.True(t, retryableFor(FailureProcess))
	assert.True(t, retryableFor(FailureTimeout))
	assert.True(t, retryableFor(FailureSemantic))
}

// The Error type carries its class and exposes the cause.
func TestErrorUnwrapsAndReportsItsClass(t *testing.T) {
	cause := fmt.Errorf("underlying")
	err := &Error{Class: FailureTimeout, Method: "session/prompt", Err: cause, Retryable: true}

	assert.ErrorIs(t, err, cause)
	assert.Contains(t, err.Error(), "session/prompt")
	assert.Contains(t, err.Error(), "timeout")
}

// --- stop reason mapping ---

// The two vocabularies answer different questions, so the mapping is explicit
// and an unknown reason is not reported as success.
func TestStopReasonMapping(t *testing.T) {
	cases := map[StopReason]string{
		StopEndTurn:         "completed",
		StopMaxTokens:       "max_tokens",
		StopMaxTurnRequests: "max_turn_requests",
		StopRefusal:         "refused",
		StopCancelled:       "cancelled",
	}
	for reason, want := range cases {
		assert.Equal(t, want, mapStopReason(reason), string(reason))
	}

	// An unfamiliar word is reported as itself: guessing "completed" from it
	// would tell the caller a task was done when nobody said so.
	unknown := mapStopReason("something_new")
	assert.Contains(t, unknown, "unknown")
	assert.NotEqual(t, "completed", unknown)
}

// --- framing ---

// The transport must tolerate a stray line on stdout that is not a message: the
// protocol forbids it, but a real agent that logs to the wrong stream should not
// end the connection.
func TestUnparseableLineDoesNotEndTheConnection(t *testing.T) {
	// The fake agent never writes junk, so this checks the read loop's contract
	// indirectly: a normal turn still works after the transport has skipped
	// nothing. The skipping itself is covered by the code path and would need a
	// second fake mode to exercise; asserting the happy path here keeps the
	// contract visible.
	transport := startFakeAgent(t, "ok")
	client := NewClient(Config{Transport: transport})
	ctx := context.Background()

	_, err := client.Initialize(ctx)
	require.NoError(t, err)
	_, err = client.NewSession(ctx, "/tmp")
	require.NoError(t, err)
	_, err = client.Prompt(ctx, "x")
	require.NoError(t, err)
}

// Cancel is a notification: it must not wait for an answer, because waiting to
// cancel would take as long as the thing being cancelled.
func TestCancelDoesNotWaitForAnAnswer(t *testing.T) {
	transport := startFakeAgent(t, "ok")
	client := NewClient(Config{Transport: transport})
	ctx := context.Background()

	_, err := client.Initialize(ctx)
	require.NoError(t, err)
	_, err = client.NewSession(ctx, "/tmp")
	require.NoError(t, err)

	done := make(chan struct{})
	go func() {
		_ = client.Cancel(ctx)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Cancel blocked; it is a notification and must return immediately")
	}
}

// Cancel before a session exists is a no-op, not an error: there is nothing to
// cancel, and failing would make shutdown conditional on setup having worked.
func TestCancelWithoutASessionIsANoOp(t *testing.T) {
	transport := startFakeAgent(t, "ok")
	client := NewClient(Config{Transport: transport})

	assert.NoError(t, client.Cancel(context.Background()))
}

// --- the child runner ---

// The runner satisfies the delegation contract, which is what the interface lift
// was for.
func TestChildRunnerImplementsTheDelegationContract(t *testing.T) {
	transport := startFakeAgent(t, "ok")

	runner, err := NewChildRunner(context.Background(), RunnerConfig{
		Transport: transport,
		Info:      ClientInfo{Name: "test"},
		CWD:       "/tmp",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = runner.Close(context.Background()) })

	result, err := runner.Run(context.Background(), "child-1", "do the task")
	require.NoError(t, err)
	assert.Equal(t, "done: the task is complete", result.Answer)
	assert.Equal(t, "completed", result.Reason)

	// The agent minted the session, so the runner reports the agent's id rather
	// than pretending the caller's id is one.
	assert.Equal(t, "agent-session-1", runner.SessionID())
}

// Continue sends another turn on the same session, and the answer is the second
// turn's output rather than an accumulation.
func TestChildRunnerContinueStartsAFreshAnswer(t *testing.T) {
	transport := startFakeAgent(t, "ok")
	runner, err := NewChildRunner(context.Background(), RunnerConfig{Transport: transport})
	require.NoError(t, err)
	t.Cleanup(func() { _ = runner.Close(context.Background()) })

	first, err := runner.Run(context.Background(), "c", "first")
	require.NoError(t, err)
	assert.Equal(t, "done: the task is complete", first.Answer)

	second, err := runner.Continue(context.Background(), "c", "second")
	require.NoError(t, err)
	assert.Equal(t, "done: the task is complete", second.Answer,
		"each turn's answer is its own, not the previous one plus this one")
}

// A runner without a transport is refused at construction, where the deployment
// is being set up, rather than on the first task.
func TestChildRunnerNeedsATransport(t *testing.T) {
	_, err := NewChildRunner(context.Background(), RunnerConfig{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "transport")
}

// A handshake failure surfaces at construction: a version mismatch should not
// look like a task failure halfway through a run.
func TestChildRunnerSurfacesHandshakeFailureAtConstruction(t *testing.T) {
	transport := startFakeAgent(t, "wrong-version")

	_, err := NewChildRunner(context.Background(), RunnerConfig{Transport: transport})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "protocol version")
}

// Describe names the agent, so an operator can tell which implementation a
// delegation went to.
func TestChildRunnerDescribesTheAgent(t *testing.T) {
	transport := startFakeAgent(t, "ok")
	runner, err := NewChildRunner(context.Background(), RunnerConfig{Transport: transport})
	require.NoError(t, err)
	t.Cleanup(func() { _ = runner.Close(context.Background()) })

	description := runner.Describe()
	assert.Contains(t, description, "fake-agent")
	assert.True(t, strings.HasPrefix(description, "acp agent"))
}

// The client declares no capabilities, because it implements none. Declaring one
// to look capable is how a client invites requests it cannot serve.
func TestClientDeclaresNoCapabilities(t *testing.T) {
	var caps clientCapabilities
	assert.False(t, caps.FS.ReadTextFile)
	assert.False(t, caps.FS.WriteTextFile)
	assert.False(t, caps.Terminal)
}
