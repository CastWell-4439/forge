package guardrails

import (
	"context"
	"regexp"
)

// ContentFilter scans agent output and redacts sensitive information
// such as API keys, passwords, and internal addresses.
type ContentFilter struct {
	rules []redactRule
}

type redactRule struct {
	pattern     *regexp.Regexp
	replacement string
}

// NewContentFilter creates a filter with default redaction rules.
func NewContentFilter() *ContentFilter {
	rules := []redactRule{
		// OpenAI-style API keys.
		{regexp.MustCompile(`sk-[A-Za-z0-9]{20,}`), "[REDACTED:api_key]"},
		// AWS access keys.
		{regexp.MustCompile(`AKIA[A-Z0-9]{16}`), "[REDACTED:aws_key]"},
		// GitHub personal access tokens.
		{regexp.MustCompile(`ghp_[A-Za-z0-9]{36}`), "[REDACTED:github_token]"},
		// Generic Bearer tokens (long hex/base64 strings after "Bearer").
		{regexp.MustCompile(`Bearer\s+[A-Za-z0-9\-_\.]{40,}`), "Bearer [REDACTED:token]"},
		// password= or pwd= patterns.
		{regexp.MustCompile(`(?i)(password|passwd|pwd)\s*[=:]\s*\S+`), "$1=[REDACTED]"},
		// Connection strings with passwords.
		{regexp.MustCompile(`(?i)://[^:]+:([^@\s]{8,})@`), "://[user]:[REDACTED]@"},
		// Private IP ranges (optional, warn rather than redact).
		// Internal hostnames (*.domob-inc.com, *.domob-inc.cn).
		{regexp.MustCompile(`[a-zA-Z0-9\-]+\.domob-inc\.(com|cn)(:[0-9]+)?`), "[REDACTED:internal_host]"},
	}

	return &ContentFilter{rules: rules}
}

// Check scans the output and returns the redacted version.
// Implements core.OutputGuard.
func (f *ContentFilter) Check(ctx context.Context, output string) (string, error) {
	result := output
	for _, rule := range f.rules {
		result = rule.pattern.ReplaceAllString(result, rule.replacement)
	}
	return result, nil
}

// Verify interface compliance at compile time.
var _ interface {
	Check(context.Context, string) (string, error)
} = (*ContentFilter)(nil)
