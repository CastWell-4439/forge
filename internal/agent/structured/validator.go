package structured

import (
	"encoding/json"
	"fmt"
	"strings"
)

const (
	// MaxParseRetries is the maximum number of times to retry parsing
	// an LLM response before giving up.
	MaxParseRetries = 2
)

// ParseResponse attempts to parse a raw LLM output string into an AgentResponse.
// It handles common LLM quirks: markdown code fences, leading/trailing text.
func ParseResponse(raw string) (*AgentResponse, error) {
	jsonStr := ExtractJSONObject(raw)
	if jsonStr == "" {
		return nil, fmt.Errorf("no JSON object found in LLM output")
	}

	var resp AgentResponse
	if err := json.Unmarshal([]byte(jsonStr), &resp); err != nil {
		return nil, fmt.Errorf("JSON parse error: %w", err)
	}

	if err := resp.Validate(); err != nil {
		return nil, err
	}

	return &resp, nil
}

// ParseWithRetry wraps ParseResponse with automatic retry.
// On parse failure, it calls retryFn with the error message to get a new
// LLM response, then tries parsing again. Up to MaxParseRetries attempts.
//
// retryFn receives a feedback message describing what went wrong,
// and should return a new raw LLM response string.
func ParseWithRetry(raw string, retryFn func(feedback string) (string, error)) (*AgentResponse, error) {
	resp, err := ParseResponse(raw)
	if err == nil {
		return resp, nil
	}

	for attempt := 0; attempt < MaxParseRetries; attempt++ {
		feedback := fmt.Sprintf(
			"Your previous response could not be parsed. Error: %s\n"+
				"Please respond with valid JSON matching the required schema.\n"+
				"The JSON must have: \"thought\" (string, required), "+
				"and either \"action\" (object with \"name\" and \"params\") or \"answer\" (string).",
			err.Error(),
		)

		newRaw, retryErr := retryFn(feedback)
		if retryErr != nil {
			return nil, fmt.Errorf("retry %d failed: %w", attempt+1, retryErr)
		}

		resp, err = ParseResponse(newRaw)
		if err == nil {
			return resp, nil
		}
	}

	return nil, fmt.Errorf("failed to parse after %d retries: %w", MaxParseRetries, err)
}

// ExtractJSONObject finds the first complete JSON object ({...}) in a string.
// Handles markdown code fences and leading/trailing text.
func ExtractJSONObject(raw string) string {
	s := strings.TrimSpace(raw)

	// Strip markdown code fences.
	if strings.HasPrefix(s, "```") {
		if idx := strings.Index(s, "\n"); idx >= 0 {
			s = s[idx+1:]
		}
		if idx := strings.LastIndex(s, "```"); idx >= 0 {
			s = s[:idx]
		}
		s = strings.TrimSpace(s)
	}

	// Find first '{' and its matching '}'.
	start := strings.Index(s, "{")
	if start == -1 {
		return ""
	}

	depth := 0
	inString := false
	escaped := false

	for i := start; i < len(s); i++ {
		ch := s[i]

		if escaped {
			escaped = false
			continue
		}

		if ch == '\\' && inString {
			escaped = true
			continue
		}

		if ch == '"' {
			inString = !inString
			continue
		}

		if inString {
			continue
		}

		switch ch {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}

	return ""
}
