package workers

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// Shell handler tool definition: shell.run.
//
// Real mode runs a whitelisted command inside the workspace with a timeout -
// the same three rules the workflow-plane shell worker enforces. The whitelist
// is the build-and-test toolchain; a command is accepted when its first token
// is on the list and none of the forbidden patterns appear anywhere in it.

// shellAllowedCommands is the whitelist of executable command names.
// Deliberately mirrors internal/workers/shell.DefaultConfig().AllowedCommands
// plus git: the agent's shell can build, test and inspect, not administer.
var shellAllowedCommands = []string{
	"go", "git", "npm", "npx", "yarn", "pnpm",
	"make", "cmake",
	"python", "python3", "pip", "pip3",
	"golangci-lint", "eslint", "prettier",
	"buf", "protoc",
	"cargo", "rustc",
}

// shellForbiddenPatterns are substrings that reject a command outright.
var shellForbiddenPatterns = []string{
	"rm -rf /",
	"sudo",
	"curl | sh",
	"wget | sh",
	"> /dev/sda",
}

const (
	shellDefaultTimeout = 60 * time.Second
	shellMaxOutputBytes = 256 * 1024
)

func ShellRunDef() *ToolDef {
	return &ToolDef{
		Name:        "shell.run",
		DisplayName: "Shell Run",
		Category:    "shell",
		Description: "Run a whitelisted command (build and test toolchain: go, git, make, ...) in the workspace. Commands outside the whitelist or matching forbidden patterns are rejected.",
		InputSchema: map[string]ParamDef{
			"command": {Type: "string", Description: "Command line to run", Required: true},
			"workdir": {Type: "string", Description: "Working directory, relative to the workspace (default: workspace root)"},
			"timeout": {Type: "integer", Description: "Timeout in seconds (default 60, max 300)"},
		},
		OutputSchema: map[string]ParamDef{
			"stdout":    {Type: "string", Description: "Combined standard output and error"},
			"exit_code": {Type: "integer", Description: "Process exit code"},
		},
		RequiredParams: []string{"command"},
		EstimatedTime:  30 * time.Second,
	}
}

func NewShellRunHandler(cfg HandlerConfig) HandlerFunc {
	if cfg.Mode == HandlerModeMock {
		return mockShellRun()
	}
	return realShellRun(cfg)
}

func mockShellRun() HandlerFunc {
	return func(_ context.Context, params map[string]interface{}) (map[string]interface{}, error) {
		command, _ := params["command"].(string)
		if command == "" {
			return nil, fmt.Errorf("shell.run: missing required param 'command'")
		}
		return map[string]interface{}{
			"stdout":    fmt.Sprintf("[mock shell] ok: %s", command),
			"exit_code": 0,
		}, nil
	}
}

// checkShellCommand applies the whitelist and the forbidden patterns.
func checkShellCommand(command string) error {
	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		return fmt.Errorf("missing required param 'command'")
	}
	for _, forbidden := range shellForbiddenPatterns {
		if strings.Contains(trimmed, forbidden) {
			return fmt.Errorf("command rejected: contains forbidden pattern %q", forbidden)
		}
	}
	first := strings.Fields(trimmed)[0]
	for _, allowed := range shellAllowedCommands {
		if first == allowed {
			return nil
		}
	}
	return fmt.Errorf("command %q is not in the allowed list (%s)", first, strings.Join(shellAllowedCommands, ", "))
}

func realShellRun(cfg HandlerConfig) HandlerFunc {
	return func(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error) {
		command, _ := params["command"].(string)
		if err := checkShellCommand(command); err != nil {
			return nil, fmt.Errorf("shell.run: %w", err)
		}

		workdir := cfg.Workspace
		if wd, _ := params["workdir"].(string); wd != "" {
			target, err := resolveInWorkspace(cfg.Workspace, wd)
			if err != nil {
				return nil, fmt.Errorf("shell.run: %w", err)
			}
			workdir = target
		}
		if info, err := os.Stat(workdir); err != nil || !info.IsDir() {
			return nil, fmt.Errorf("shell.run: working directory %q does not exist", workdir)
		}

		timeout := shellDefaultTimeout
		if secs, ok := toInt(params["timeout"], 0); ok && secs > 0 {
			if secs > 300 {
				secs = 300
			}
			timeout = time.Duration(secs) * time.Second
		}
		runCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()

		// The whitelist only names the program, so the rest of the line needs a
		// shell to split arguments - cmd /c on Windows, sh -c elsewhere, chosen
		// by runtime rather than assumed.
		var cmd *exec.Cmd
		if runtime.GOOS == "windows" {
			cmd = exec.CommandContext(runCtx, "cmd", "/c", command)
		} else {
			cmd = exec.CommandContext(runCtx, "sh", "-c", command)
		}
		cmd.Dir = workdir

		var buf bytes.Buffer
		cmd.Stdout = &buf
		cmd.Stderr = &buf
		runErr := cmd.Run()

		exitCode := 0
		if runErr != nil {
			if ee, ok := runErr.(*exec.ExitError); ok {
				exitCode = ee.ExitCode()
			} else {
				return nil, fmt.Errorf("shell.run: %w", runErr)
			}
		}
		output := buf.String()
		if len(output) > shellMaxOutputBytes {
			output = output[:shellMaxOutputBytes] + "\n...(truncated)"
		}
		return map[string]interface{}{
			"stdout":    output,
			"exit_code": exitCode,
		}, nil
	}
}
