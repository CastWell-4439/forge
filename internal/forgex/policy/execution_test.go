package policy

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/castwell/forge/internal/forgex/model"
)

// The mapping is the shared contract between the runtime gate and the demo
// path, so every action is pinned here rather than inferred at each call site.
func TestActionModeMapping(t *testing.T) {
	cases := []struct {
		action   Action
		mode     ExecutionMode
		executes bool
		stop     model.StopAction
	}{
		{ActionAllow, ExecutionProceed, true, model.StopActionContinue},
		{ActionDryRunOnly, ExecutionDryRun, false, model.StopActionContinue},
		{ActionDeny, ExecutionBlock, false, model.StopActionStop},
		{ActionRequireApproval, ExecutionHold, false, model.StopActionPause},
		{ActionPause, ExecutionHold, false, model.StopActionPause},
		{ActionEscalate, ExecutionHold, false, model.StopActionPause},
	}

	for _, tc := range cases {
		t.Run(string(tc.action), func(t *testing.T) {
			assert.Equal(t, tc.mode, tc.action.Mode())
			assert.Equal(t, tc.executes, tc.action.AllowsExecution())
			assert.Equal(t, tc.stop, tc.action.StopAction())
		})
	}
}

// An action nobody defined must not be read as permission: unknown outcomes
// fail closed.
func TestUnknownActionFailsClosed(t *testing.T) {
	unknown := Action("something_new")

	assert.Equal(t, ExecutionHold, unknown.Mode())
	assert.False(t, unknown.AllowsExecution(), "an unknown action must never allow execution")
	assert.Equal(t, model.StopActionPause, unknown.StopAction())
}
