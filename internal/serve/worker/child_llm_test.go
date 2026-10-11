package worker

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcore "github.com/castwell/forge/internal/agent"
)

// Unset is the normal case and means inherit: a deployment that has not thought
// about child models must not discover it is paying for two.
func TestNoChildModelMeansInherit(t *testing.T) {
	t.Setenv(envChildModel, "")
	t.Setenv(envChildBaseURL, "")
	t.Setenv(envChildAPIKey, "")
	t.Setenv(envLLMAPIKey, "parent-key")

	assert.Nil(t, newChildLLMClient(),
		"no model configured means the child uses the parent's client")
}

// A base URL or key alone is not routing: it would leave the child on the
// parent's model but pointed at another endpoint, which nobody means to write.
func TestEndpointAloneDoesNotRoute(t *testing.T) {
	t.Setenv(envChildModel, "")
	t.Setenv(envChildBaseURL, "https://elsewhere.example.com")
	t.Setenv(envChildAPIKey, "child-key")
	t.Setenv(envLLMAPIKey, "parent-key")

	assert.Nil(t, newChildLLMClient(),
		"only a model selects a different client; an endpoint on its own is a half-configuration")
}

// A model with a key routes the child.
func TestChildModelRoutesWithItsOwnKey(t *testing.T) {
	t.Setenv(envChildModel, "cheap-model")
	t.Setenv(envChildAPIKey, "child-key")
	t.Setenv(envLLMAPIKey, "parent-key")

	assert.NotNil(t, newChildLLMClient())
}

// A model without its own key falls back to the parent's, because pointing a
// child at another model on the same provider is the common case and should not
// need a second key.
func TestChildModelFallsBackToTheParentKey(t *testing.T) {
	t.Setenv(envChildModel, "cheap-model")
	t.Setenv(envChildAPIKey, "")
	t.Setenv(envLLMAPIKey, "parent-key")

	assert.NotNil(t, newChildLLMClient())
}

// No key anywhere means no child client rather than a broken one: the child
// then inherits, and the parent's own registration has already established
// whether a key exists at all.
func TestNoKeyAnywhereInherits(t *testing.T) {
	t.Setenv(envChildModel, "cheap-model")
	t.Setenv(envChildAPIKey, "")
	t.Setenv(envLLMAPIKey, "")

	assert.Nil(t, newChildLLMClient())
}

// The option ignores a nil client, so "not configured" and "configured to
// nothing" do not become two states that behave identically but read
// differently.
func TestWithChildLLMIgnoresNil(t *testing.T) {
	var agent agentcore.Agent
	agentcore.WithChildLLM(nil)(&agent)

	assert.Nil(t, agent.ChildLLM, "a nil client leaves the field unset")
}

// The safety property that makes this feature addable: routing a child to
// another model changes what it costs, not what it may do.
//
// It holds by construction — the child is assembled by the same code path as
// its parent, and only the LLM client differs — so this test asserts the
// construction rather than the outcome: the option touches one field.
func TestChildRoutingTouchesOnlyTheLLM(t *testing.T) {
	child := newChildLLMClient()
	if child == nil {
		t.Setenv(envChildModel, "cheap-model")
		t.Setenv(envLLMAPIKey, "parent-key")
		child = newChildLLMClient()
	}
	require.NotNil(t, child)

	parent := &agentcore.Agent{}
	agentcore.WithChildLLM(child)(parent)

	// Everything that decides authority is untouched.
	assert.Nil(t, parent.InputGuard)
	assert.Nil(t, parent.OutputGuard)
	assert.Nil(t, parent.ToolOutputGuard)
	assert.Empty(t, parent.Subagent.Mode, "delegation configuration is not implied by routing")
	assert.False(t, parent.NativeTools)
	assert.NotNil(t, parent.ChildLLM, "and the one field it does set is set")
}
