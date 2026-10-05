package registry

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// compileTrigger validates the SHAPE of each trigger type at load time: an
// unusable trigger must fail when the workflow loads, not the first time it
// should have fired.
func TestCompileTriggerShapeValidation(t *testing.T) {
	cases := []struct {
		name    string
		def     TriggerDef
		wantErr string
	}{
		{
			name: "cron with expr compiles",
			def:  TriggerDef{Type: "cron", Expr: "*/5 * * * *"},
		},
		{
			name:    "cron without expr is rejected",
			def:     TriggerDef{Type: "cron"},
			wantErr: `"expr"`,
		},
		{
			name: "poll with interval and source compiles",
			def:  TriggerDef{Type: "poll", Source: "feishu_mcp", Interval: "2m", DedupKey: "work_item_id"},
		},
		{
			name:    "poll without interval is rejected",
			def:     TriggerDef{Type: "poll", Source: "feishu_mcp"},
			wantErr: `"interval"`,
		},
		{
			name:    "poll without source is rejected",
			def:     TriggerDef{Type: "poll", Interval: "2m"},
			wantErr: `"source"`,
		},
		{
			name: "webhook and manual are recognised",
			def:  TriggerDef{Type: "webhook"},
		},
		{
			name:    "unknown types fail loudly",
			def:     TriggerDef{Type: "cronjob"},
			wantErr: "unknown trigger type",
		},
		{
			name:    "unparseable interval fails",
			def:     TriggerDef{Type: "poll", Source: "s", Interval: "two minutes"},
			wantErr: "invalid interval",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := compileTrigger(tc.def)
			if tc.wantErr == "" {
				require.NoError(t, err)
				assert.Equal(t, tc.def.Type, got.Type)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

// The optional expr field survives parsing into the compiled form — the cron
// scheduler consumes it from there.
func TestTriggerExprPassesThroughParsing(t *testing.T) {
	wf, err := Parse([]byte(`apiVersion: forge/v1
kind: Workflow
metadata:
  name: exprwf
triggers:
  - type: cron
    expr: "0 9 * * 1-5"
stages:
  - name: only
    tasks:
      - worker: shell
        action: run
`))
	require.NoError(t, err)

	compiled, err := compileWorkflow(wf)
	require.NoError(t, err)
	require.Len(t, compiled.Triggers, 1)
	assert.Equal(t, "cron", compiled.Triggers[0].Type)
	assert.Equal(t, "0 9 * * 1-5", compiled.Triggers[0].Expr)
}
