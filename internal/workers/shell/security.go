package shell

import "time"

// Config holds Shell Worker configuration.
type Config struct {
	// AllowedCommands is the whitelist of executable command names.
	// If empty, only built-in actions (run_test, build, lint) are allowed.
	AllowedCommands []string

	// AllowedWorkdirs restricts where commands can run.
	// If empty, no restriction is applied.
	AllowedWorkdirs []string

	// ForbiddenPatterns are command argument patterns that are always rejected.
	ForbiddenPatterns []string

	// DefaultTimeout per command execution.
	DefaultTimeout time.Duration

	// MaxOutputBytes limits captured stdout+stderr.
	MaxOutputBytes int
}

// DefaultConfig returns a safe default configuration.
func DefaultConfig() Config {
	return Config{
		AllowedCommands: []string{
			"go", "npm", "npx", "yarn", "pnpm",
			"make", "cmake",
			"python", "python3", "pip", "pip3",
			"golangci-lint", "eslint", "prettier",
			"buf", "protoc",
			"cargo", "rustc",
		},
		ForbiddenPatterns: []string{
			"rm -rf /",
			"sudo",
			"curl | sh",
			"wget | sh",
			"> /dev/sda",
		},
		DefaultTimeout: 2 * time.Minute,
		MaxOutputBytes: 256 * 1024, // 256KB
	}
}
