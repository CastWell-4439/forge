package worker

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcore "github.com/castwell/forge/internal/agent"
)

// Unset means local children, which is the historical behaviour and costs
// nothing. A deployment that has not asked for an external agent must not get
// one.
func TestNoACPCommandMeansLocalChildren(t *testing.T) {
	t.Setenv(envACPCommand, "")

	assert.Nil(t, acpChildFactory("/workspace"),
		"no command configured means children run locally")
}

// A configured command produces a factory.
func TestACPCommandProducesAFactory(t *testing.T) {
	t.Setenv(envACPCommand, "/usr/local/bin/some-agent")

	factory := acpChildFactory("/workspace")
	require.NotNil(t, factory)
}

// Arguments are split on whitespace, and no shell quoting is implemented —
// deliberately, because the value comes from an operator-written variable and a
// quoting syntax would be one more thing to get wrong.
func TestACPArgsAreSplitOnWhitespace(t *testing.T) {
	assert.Nil(t, splitArgs(""))
	assert.Nil(t, splitArgs("   "))
	assert.Equal(t, []string{"--flag", "value"}, splitArgs("--flag value"))
	assert.Equal(t, []string{"a", "b", "c"}, splitArgs("  a   b  c  "))
}

// The option ignores a nil factory, so "not configured" and "configured to
// nothing" do not become two states that behave identically but read
// differently.
func TestWithChildFactoryIgnoresNil(t *testing.T) {
	var agent agentcore.Agent
	agentcore.WithChildFactory(nil)(&agent)

	assert.Nil(t, agent.ChildFactory, "a nil factory leaves the field unset")
}

// A bad timeout is reported and falls back rather than disabling the timeout: an
// agent that stops answering must still be reported, and a typo in a duration
// should not remove that protection.
func TestBadACPTimesoutFallsBack(t *testing.T) {
	t.Setenv(envACPCommand, "/bin/true")
	t.Setenv(envACPTimeout, "not-a-duration")

	// The factory still builds; the warning is what a reader of the log gets.
	assert.NotNil(t, acpChildFactory("/workspace"))

	t.Setenv(envACPTimeout, "30s")
	assert.NotNil(t, acpChildFactory("/workspace"))
}

// The ACP factory is a hook, not a hard-wired path: with it set, the agent uses
// it instead of its own local assembly.
func TestChildFactoryOverridesLocalAssembly(t *testing.T) {
	t.Setenv(envACPCommand, "/bin/true")

	factory := acpChildFactory("/workspace")
	require.NotNil(t, factory)

	agent := &agentcore.Agent{}
	agentcore.WithChildFactory(factory)(agent)
	assert.NotNil(t, agent.ChildFactory, "the injected factory is what the agent will use")
}
