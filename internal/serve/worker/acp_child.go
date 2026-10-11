package worker

import (
	"context"
	"log"
	"os"
	"strings"
	"time"

	"github.com/castwell/forge/internal/agent/acp"
	"github.com/castwell/forge/internal/agent/harness"
)

// Routing delegated children to an external ACP agent.
//
//	FORGE_SUBAGENT_ACP_COMMAND   the agent executable. Unset means children run
//	                             locally, which is the historical behaviour.
//	FORGE_SUBAGENT_ACP_ARGS      space-separated arguments for it
//	FORGE_SUBAGENT_ACP_CWD       working directory reported to the agent
//	                             (default: the agent workspace)
//	FORGE_SUBAGENT_ACP_TIMEOUT   per-call timeout (default 10m)
//
// This is the fourth delegation axis and the only one that leaves the process.
// It exists because the shape of "run a subtask somewhere else and stream back
// what it did" is the same for every external agent, and ACP standardises it —
// so one protocol implementation replaces one adapter per tool.
//
// What it does NOT change is authority. The child runs under the same
// delegation limits (depth, concurrency, background cap), is subject to the same
// release discipline, and is reached through the same ChildLoopFactory seam.
// Pointing delegations at another program changes where the work happens, not
// what this agent is allowed to do.

const (
	envACPCommand = "FORGE_SUBAGENT_ACP_COMMAND"
	envACPArgs    = "FORGE_SUBAGENT_ACP_ARGS"
	envACPCwd     = "FORGE_SUBAGENT_ACP_CWD"
	envACPTimeout = "FORGE_SUBAGENT_ACP_TIMEOUT"
)

// defaultACPCallTimeout bounds one ACP call.
//
// It is generous because a delegated subtask can legitimately take minutes, and
// a tight timeout would turn slow work into failures. It is not unlimited
// because an agent that stops answering must eventually be reported rather than
// waited on forever.
const defaultACPCallTimeout = 10 * time.Minute

// acpChildFactory returns a ChildLoopFactory that runs children under ACP, or
// nil when no external agent is configured.
//
// nil means "not configured", not "misconfigured": a deployment that has not set
// the variable wants local children, and that is the default.
func acpChildFactory(workspace string) harness.ChildLoopFactory {
	command := strings.TrimSpace(os.Getenv(envACPCommand))
	if command == "" {
		return nil
	}

	args := splitArgs(os.Getenv(envACPArgs))
	cwd := strings.TrimSpace(os.Getenv(envACPCwd))
	if cwd == "" {
		cwd = workspace
	}

	timeout := defaultACPCallTimeout
	if raw := strings.TrimSpace(os.Getenv(envACPTimeout)); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil && parsed > 0 {
			timeout = parsed
		} else {
			log.Printf("WARN: %s %q is not a positive duration; using %s",
				envACPTimeout, raw, defaultACPCallTimeout)
		}
	}

	log.Printf("INFO: delegated children run under ACP (%s), cwd=%s, timeout=%s",
		command, cwd, timeout)

	return func(ctx context.Context, childSessionID string, tools []string) (harness.ChildRunner, func(), error) {
		// Each child gets its own process. Sharing one agent process across
		// children would make one child's cancellation or crash everyone's
		// problem, and the protocol's session model does not require it.
		transport, err := acp.NewStdioTransport(acp.StdioConfig{
			Command: command,
			Args:    args,
			Dir:     cwd,
			// The agent's stderr is forwarded rather than discarded: it is the
			// only diagnostic a failing external agent produces, and MCP's
			// transport discards it, which this deliberately does not repeat.
			Stderr: os.Stderr,
		})
		if err != nil {
			return nil, nil, err
		}

		runner, err := acp.NewChildRunner(ctx, acp.RunnerConfig{
			Transport: transport,
			Info: acp.ClientInfo{
				Name:    "forge",
				Title:   "Forge agent runtime",
				Version: agentVersion,
			},
			CWD:     cwd,
			Timeout: timeout,
		})
		if err != nil {
			// The handshake failed, so the process is of no use. Closing it here
			// rather than returning a runner that will fail every task keeps the
			// failure where it happened.
			_ = transport.Close()
			return nil, nil, err
		}

		release := func() {
			// A cancel before closing lets the agent abandon its work
			// deliberately; the transport close then ends the process. Both are
			// best-effort and both are idempotent, which matters because a
			// background child is released twice: once on completion and once
			// when the run that owns it unwinds.
			closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = runner.Close(closeCtx)
			_ = transport.Close()
		}

		_ = childSessionID
		_ = tools
		return runner, release, nil
	}
}

// agentVersion identifies this runtime during the ACP handshake.
//
// It is a constant rather than a build-injected value because nothing in this
// project wires a version into the binary, and a handshake field that reads
// "dev" honestly is better than one that reads a variable that is always empty.
const agentVersion = "dev"

// splitArgs splits a command-line string on whitespace.
//
// It does not implement shell quoting, and that is deliberate: the value comes
// from an environment variable that the operator writes, so a quoting syntax
// would be one more thing to get wrong. An argument containing a space is passed
// by putting it in the command itself or by wrapping the agent in a script.
func splitArgs(raw string) []string {
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return nil
	}
	return fields
}
