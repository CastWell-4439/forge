package worker

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/castwell/forge/internal/agent/core"
)

// FORGE_AGENT_AUTHORITY is the deployment's ceiling for agent runs. The
// important property is the DIRECTION of the fallback: an unset or misspelled
// value must land on the default (L2, read-only), never on a permissive rung.
func TestAgentAuthorityResolvesSafely(t *testing.T) {
	t.Setenv(envAgentAuthority, "")
	assert.Equal(t, core.DefaultAuthority, agentAuthority(), "unset means the default")
	assert.Equal(t, core.AuthorityL2, core.DefaultAuthority, "and the default is read-only")

	for _, tc := range []struct {
		env  string
		want core.Authority
	}{
		{"L0", core.AuthorityL0},
		{"l1", core.AuthorityL1},   // case-insensitive
		{" L3 ", core.AuthorityL3}, // trimmed
		{"L4", core.AuthorityL4},
	} {
		t.Setenv(envAgentAuthority, tc.env)
		assert.Equal(t, tc.want, agentAuthority(), "%q resolves to %s", tc.env, tc.want)
	}

	// The security-relevant cases: anything unrecognised falls back to the
	// DEFAULT, never to L4.
	for _, bad := range []string{"L9", "admin", "true", "L", "l5", "L2.5"} {
		t.Setenv(envAgentAuthority, bad)
		assert.Equal(t, core.DefaultAuthority, agentAuthority(),
			"%q must not grant authority; it falls back to the default", bad)
	}
}
