// Package guardrails implements M6: safety mechanisms for the Agent system.
// Includes prompt injection detection, output content filtering, and token budget enforcement.
package guardrails

import (
	"context"
	"fmt"
	"regexp"
	"unicode"
)

// ErrInjectionDetected is returned when a prompt injection attempt is detected.
var ErrInjectionDetected = fmt.Errorf("prompt injection detected")

// InjectionDetector checks user input for prompt injection patterns.
type InjectionDetector struct {
	patterns []*regexp.Regexp
}

// NewInjectionDetector creates a detector with default dangerous patterns.
func NewInjectionDetector() *InjectionDetector {
	rawPatterns := []string{
		`(?i)ignore\s+(all\s+)?previous\s+instructions`,
		`(?i)disregard\s+(all\s+)?(prior|previous|above)`,
		`(?i)you\s+are\s+now\s+`,
		`(?i)pretend\s+you\s+are`,
		`(?i)override\s+your\s+(system|instructions|rules)`,
		`(?i)system\s*prompt`,
		`(?i)forget\s+(everything|all|your\s+instructions)`,
		`(?i)new\s+instructions?\s*:`,
		`(?i)act\s+as\s+(if|a|an)\s+`,
		`(?i)do\s+not\s+follow\s+(your|the)\s+(rules|instructions)`,
	}

	patterns := make([]*regexp.Regexp, 0, len(rawPatterns))
	for _, p := range rawPatterns {
		patterns = append(patterns, regexp.MustCompile(p))
	}

	return &InjectionDetector{patterns: patterns}
}

// Check examines input for injection attempts.
// Returns ErrInjectionDetected if a dangerous pattern is found.
// Implements core.InputGuard.
func (d *InjectionDetector) Check(ctx context.Context, input string) error {
	// Check regex patterns.
	for _, p := range d.patterns {
		if p.MatchString(input) {
			return fmt.Errorf("%w: matched pattern %q", ErrInjectionDetected, p.String())
		}
	}

	// Check for flood attack (excessive repetition).
	if isFloodAttack(input) {
		return fmt.Errorf("%w: flood attack detected", ErrInjectionDetected)
	}

	// Check for invisible Unicode injection.
	if hasInvisibleChars(input) {
		return fmt.Errorf("%w: invisible unicode characters detected", ErrInjectionDetected)
	}

	return nil
}

// isFloodAttack checks if input contains excessive repetition of a single character.
func isFloodAttack(input string) bool {
	if len(input) < 200 {
		return false
	}
	// If any single character makes up >80% of a 200+ char input, it's suspicious.
	counts := make(map[rune]int)
	total := 0
	for _, r := range input {
		counts[r]++
		total++
	}
	for _, count := range counts {
		if float64(count)/float64(total) > 0.8 {
			return true
		}
	}
	return false
}

// hasInvisibleChars checks for suspicious invisible Unicode characters.
// Zero-width spaces, direction overrides, etc. are often used to hide injections.
func hasInvisibleChars(input string) bool {
	suspiciousCount := 0
	for _, r := range input {
		if isInvisibleSuspicious(r) {
			suspiciousCount++
			if suspiciousCount >= 3 {
				return true
			}
		}
	}
	return false
}

func isInvisibleSuspicious(r rune) bool {
	// Zero-width chars.
	if r == '\u200B' || r == '\u200C' || r == '\u200D' || r == '\uFEFF' {
		return true
	}
	// Direction overrides.
	if r == '\u202A' || r == '\u202B' || r == '\u202C' || r == '\u202D' || r == '\u202E' {
		return true
	}
	// Other invisible formatting.
	if unicode.Is(unicode.Cf, r) && r != '\n' && r != '\r' && r != '\t' {
		return true
	}
	return false
}

var _ interface {
	Check(context.Context, string) error
} = (*InjectionDetector)(nil)
