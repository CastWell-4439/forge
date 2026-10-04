package worker

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/castwell/forge/internal/worker"
)

// With an LLM key configured the agent worker registers its handler.
func TestRegisterAgentWithKey(t *testing.T) {
	t.Setenv(envLLMAPIKey, "test-key")

	r := worker.NewRegistry()
	registerAgent(r)
	require.NotNil(t, r.Get("agent"), "agent worker must register when an LLM key exists")
}

// Without a key it registers the explanatory handler — the node reports what
// is missing instead of dying with "unknown handler" at dispatch time.
func TestRegisterAgentWithoutKeyIsHonest(t *testing.T) {
	t.Setenv(envLLMAPIKey, "")

	r := worker.NewRegistry()
	registerAgent(r)
	require.NotNil(t, r.Get("agent"))

	_, err := r.Get("agent")(context.Background(), map[string]interface{}{"action": "run", "task": "x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), envLLMAPIKey, "the handler must name the missing key")
}

// A typo'd mode falls back to real with a warning instead of silently
// turning the agent into a canned-result machine.
func TestRegisterAgentToleratesModeTypos(t *testing.T) {
	t.Setenv(envLLMAPIKey, "test-key")
	t.Setenv(envAgentMode, "ture") // typo

	r := worker.NewRegistry()
	registerAgent(r)
	require.NotNil(t, r.Get("agent"))
}

// The tool-output guard is a security default: it is on unless explicitly
// turned off, and an unrecognised value keeps it on (a typo must never be the
// thing that disables injection screening).
func TestAgentGuardDefaultsOnAndOnlyOffDisablesIt(t *testing.T) {
	t.Setenv(envAgentGuard, "")
	assert.True(t, agentGuardEnabled(), "guard is on by default")

	for _, off := range []string{"off", "OFF", " 0 ", "false", "no"} {
		t.Setenv(envAgentGuard, off)
		assert.False(t, agentGuardEnabled(), "%q disables the guard", off)
	}
	for _, on := range []string{"on", "1", "true", "yes"} {
		t.Setenv(envAgentGuard, on)
		assert.True(t, agentGuardEnabled(), "%q keeps the guard on", on)
	}

	t.Setenv(envAgentGuard, "flase") // typo
	assert.True(t, agentGuardEnabled(), "a typo keeps the guard on, never off")
}
