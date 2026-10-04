package runtimegate

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/castwell/forge/internal/forgex/model"
	"github.com/castwell/forge/internal/forgex/policy"
)

// The gate's action mapping must stay a rename of policy's execution semantics,
// never a second opinion. If policy reclassifies an action, this fails instead
// of the gate quietly disagreeing.
func TestGateActionFollowsPolicyMode(t *testing.T) {
	cases := []struct {
		action policy.Action
		want   model.GateAction
	}{
		{policy.ActionAllow, model.GateActionAllow},
		{policy.ActionDryRunOnly, model.GateActionAllow},
		{policy.ActionDeny, model.GateActionBlock},
		{policy.ActionRequireApproval, model.GateActionPause},
		{policy.ActionPause, model.GateActionPause},
		{policy.ActionEscalate, model.GateActionEscalate},
	}
	for _, tc := range cases {
		t.Run(string(tc.action), func(t *testing.T) {
			assert.Equal(t, tc.want, gateActionFromPolicy(tc.action))

			// Cross-check against the shared table so the two cannot drift.
			switch tc.action.Mode() {
			case policy.ExecutionBlock:
				assert.Equal(t, model.GateActionBlock, tc.want)
			case policy.ExecutionHold:
				assert.Contains(t, []model.GateAction{model.GateActionPause, model.GateActionEscalate}, tc.want)
			case policy.ExecutionProceed, policy.ExecutionDryRun:
				assert.Equal(t, model.GateActionAllow, tc.want)
			}
		})
	}
}

// An action nobody defined must not be permitted by the gate either.
func TestGateFailsClosedOnUnknownAction(t *testing.T) {
	unknown := policy.Action("something_new")

	assert.Equal(t, model.GateActionPause, gateActionFromPolicy(unknown))
	assert.NotEqual(t, model.GateActionAllow, gateActionFromPolicy(unknown))
}
