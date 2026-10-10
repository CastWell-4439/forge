package worker

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/castwell/forge/internal/worker"
)

// Off by default, like every other optional capability: the handler submits child
// workflows, so enabling it changes what a workflow can cause to happen.
func TestPlannerHandlerIsOffByDefault(t *testing.T) {
	t.Setenv(envPlannerEnabled, "")

	r := worker.NewRegistry()
	registerPlannerHandler(r, "localhost:50051")

	assert.Nil(t, r.Get("planner"),
		"a handler that submits child workflows must not appear unless it was asked for")
}

// Enabled without a key names the gap at startup rather than registering a
// handler that fails on first use.
func TestPlannerHandlerWithoutAKeyNamesTheGap(t *testing.T) {
	t.Setenv(envPlannerEnabled, "1")
	t.Setenv(envPlannerAPIKey, "")
	t.Setenv(envLLMAPIKey, "")

	r := worker.NewRegistry()
	registerPlannerHandler(r, "localhost:50051")

	assert.Nil(t, r.Get("planner"), "no key means no client, so no handler")
}

// With a key the handler is registered. The coordinator connection is lazy, so
// an unreachable address does not stop registration — the same posture as every
// other optional dependency in this worker.
func TestPlannerHandlerRegistersWithAKey(t *testing.T) {
	t.Setenv(envPlannerEnabled, "1")
	t.Setenv(envPlannerAPIKey, "test-key")

	r := worker.NewRegistry()
	registerPlannerHandler(r, "localhost:50051")

	assert.NotNil(t, r.Get("planner"), "a configured planner must be reachable by workflows")
}

// The planner's own key wins when set, so a deployment can point planning at a
// different model from the one the executing steps use.
func TestPlannerPrefersItsOwnKey(t *testing.T) {
	t.Setenv(envLLMAPIKey, "general-key")
	t.Setenv(envPlannerAPIKey, "planner-key")

	client := newPlannerLLM()
	assert.NotNil(t, client, "the planner key must be enough on its own")
}

// The general key is the fallback: a deployment with one model has one key, and
// demanding a second would be friction with no benefit.
func TestPlannerFallsBackToTheGeneralKey(t *testing.T) {
	t.Setenv(envPlannerAPIKey, "")
	t.Setenv(envLLMAPIKey, "general-key")

	client := newPlannerLLM()
	assert.NotNil(t, client, "one key must be enough to use the model")
}

// No key at all means no client, rather than a client that fails at call time.
func TestPlannerWithoutAnyKeyBuildsNothing(t *testing.T) {
	t.Setenv(envPlannerAPIKey, "")
	t.Setenv(envLLMAPIKey, "")

	assert.Nil(t, newPlannerLLM())
}

// The switch spellings this project uses are all honoured.
func TestPlannerEnabledSpellings(t *testing.T) {
	for _, on := range []string{"1", "true", "TRUE", "yes", "on"} {
		t.Run("on="+on, func(t *testing.T) {
			t.Setenv(envPlannerEnabled, on)
			assert.True(t, envTruthy(envPlannerEnabled))
		})
	}
	for _, off := range []string{"", "0", "false", "no", "off", "nonsense"} {
		t.Run("off="+off, func(t *testing.T) {
			t.Setenv(envPlannerEnabled, off)
			assert.False(t, envTruthy(envPlannerEnabled))
		})
	}
}
