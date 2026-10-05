package coordinator

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	forgev1 "github.com/castwell/forge/api/proto/gen"
	"github.com/castwell/forge/internal/registry"
	"github.com/castwell/forge/internal/scheduler"
)

// writeWorkflow drops one workflow YAML into a temp directory and returns the
// directory, ready for registry.Load.
func writeWorkflow(t *testing.T, name, body string) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644))
	return dir
}

const cronWorkflowYAML = `apiVersion: forge/v1
kind: Workflow
metadata:
  name: cronwf
  version: "1"
triggers:
  - type: cron
    expr: "*/5 * * * *"
stages:
  - name: only
    tasks:
      - worker: shell
        action: run
        params:
          command: echo hi
`

const pollWorkflowYAML = `apiVersion: forge/v1
kind: Workflow
metadata:
  name: pollwf
  version: "1"
triggers:
  - type: poll
    source: feishu_mcp
    interval: 2m
    dedup_key: work_item_id
stages:
  - name: only
    tasks:
      - worker: ai
        action: summarize
`

func loadFixture(t *testing.T, yamlBody string) *registry.Registry {
	t.Helper()
	reg := registry.NewRegistry()
	require.NoError(t, reg.Load(writeWorkflow(t, "wf.yaml", yamlBody)))
	return reg
}

// The gate is the zero-change promise: off means nothing is read, nothing is
// wired, and a broken workflows directory cannot fail startup.
func TestSetupTriggersDisabledIsZeroChange(t *testing.T) {
	t.Setenv(envWorkflowTriggers, "")
	t.Setenv(envWorkflowsDir, filepath.Join(t.TempDir(), "does-not-exist"))

	stop, err := setupTriggers(context.Background(), newTestCoordinator(t))
	require.NoError(t, err, "disabled must not even try to load")
	stop()
}

// Enabled but nothing declared: a no-op, not an error.
func TestSetupTriggersEnabledWithNoTriggers(t *testing.T) {
	t.Setenv(envWorkflowTriggers, "1")
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "plain.yaml"), []byte(`apiVersion: forge/v1
kind: Workflow
metadata:
  name: plain
stages:
  - name: only
    tasks:
      - worker: shell
        action: run
`), 0o644))
	t.Setenv(envWorkflowsDir, dir)

	stop, err := setupTriggers(context.Background(), newTestCoordinator(t))
	require.NoError(t, err)
	stop()
}

// A declared cron trigger reaches the scheduler with its expression, and its
// fire path really bridges the registry workflow and lands an instance in
// storage — declared YAML to running DAG, the whole promise.
func TestCronTriggerWiresAndFires(t *testing.T) {
	reg := loadFixture(t, cronWorkflowYAML)
	coord := newTestCoordinator(t)

	cronSched, pollSched, err := buildTriggerWiring(context.Background(), reg, coord)
	require.NoError(t, err)
	assert.Nil(t, pollSched, "no poll triggers declared")

	require.NotNil(t, cronSched)
	triggers := cronSched.Triggers()
	require.Len(t, triggers, 1)
	trigger := triggers[0]
	assert.Equal(t, "*/5 * * * *", trigger.CronExpr, "the declared expression reaches the scheduler")
	assert.Equal(t, "cronwf", trigger.WorkflowName)
	assert.True(t, trigger.Enabled, "a declared trigger is active (tick skips disabled)")
	require.NotNil(t, trigger.NextFireAt, "AddTrigger computed the first fire time")

	// Fire the submission path directly: registry dialect → bridge → SubmitDAG.
	require.NoError(t, trigger.SubmitFn(context.Background()))

	resp, err := coord.ListWorkflows(context.Background(), &forgev1.ListWorkflowsRequest{})
	require.NoError(t, err)
	require.Len(t, resp.GetWorkflows(), 1, "the fired cron produced a workflow instance")
	instance := resp.GetWorkflows()[0]
	assert.Equal(t, forgev1.WorkflowStatus_WORKFLOW_STATUS_RUNNING, instance.GetStatus())

	// Detail projection carries the tasks (the list view does not).
	detail, err := coord.GetWorkflow(context.Background(), &forgev1.GetWorkflowRequest{WorkflowId: instance.GetId()})
	require.NoError(t, err)
	assert.NotEmpty(t, detail.GetWorkflow().GetTasks(), "the bridged DAG carries tasks")
}

// A poll trigger without credentials fails startup naming the env vars — a
// trigger registered with a nil poller would fire forever and do nothing.
func TestPollTriggerMissingCredentialsFailsLoud(t *testing.T) {
	t.Setenv(envFeishuMCPEndpoint, "")
	t.Setenv(envFeishuMCPToken, "")

	reg := loadFixture(t, pollWorkflowYAML)
	_, _, err := buildTriggerWiring(context.Background(), reg, newTestCoordinator(t))
	require.Error(t, err)
	assert.Contains(t, err.Error(), envFeishuMCPEndpoint)
	assert.Contains(t, err.Error(), envFeishuMCPToken)
}

// An unknown source is a config error, not a silent no-op trigger.
func TestPollTriggerUnknownSourceFailsLoud(t *testing.T) {
	reg := registry.NewRegistry()
	require.NoError(t, reg.Load(writeWorkflow(t, "wf.yaml", `apiVersion: forge/v1
kind: Workflow
metadata:
  name: badsrc
triggers:
  - type: poll
    source: jira_rest
    interval: 1m
stages:
  - name: only
    tasks:
      - worker: shell
        action: run
`)))
	_, _, err := buildTriggerWiring(context.Background(), reg, newTestCoordinator(t))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "jira_rest")
}

// webhook/manual are recognised but not wired — the setup reports no error
// and starts nothing (logged, not fatal).
func TestWebhookTriggerIsRecognisedNotWired(t *testing.T) {
	reg := loadFixture(t, `apiVersion: forge/v1
kind: Workflow
metadata:
  name: hookwf
triggers:
  - type: webhook
stages:
  - name: only
    tasks:
      - worker: shell
        action: run
`)
	cronSched, pollSched, err := buildTriggerWiring(context.Background(), reg, newTestCoordinator(t))
	require.NoError(t, err)
	assert.Nil(t, cronSched)
	assert.Nil(t, pollSched)
}

// The poll callback path: events render the workflow's declared inputs and
// land an instance — the same persistence body as every other submission.
func TestSubmitRegistryWorkflowRendersEventInputs(t *testing.T) {
	reg := loadFixture(t, `apiVersion: forge/v1
kind: Workflow
metadata:
  name: inputwf
inputs:
  first_id: "{{(index .events 0).work_item_id}}"
stages:
  - name: only
    tasks:
      - worker: shell
        action: run
`)
	cw, err := reg.Get("inputwf")
	require.NoError(t, err)
	coord := newTestCoordinator(t)

	events := []scheduler.Event{{ID: "wi-7", Payload: map[string]any{"work_item_id": "wi-7"}}}
	raw := triggerInputs(registry.CompiledTrigger{Source: "feishu_mcp"}, events)
	require.NoError(t, submitRegistryWorkflow(context.Background(), reg, coord, cw, raw))

	resp, err := coord.ListWorkflows(context.Background(), &forgev1.ListWorkflowsRequest{})
	require.NoError(t, err)
	require.Len(t, resp.GetWorkflows(), 1)

	detail, err := coord.GetWorkflow(context.Background(), &forgev1.GetWorkflowRequest{WorkflowId: resp.GetWorkflows()[0].GetId()})
	require.NoError(t, err)
	var input map[string]any
	require.NoError(t, json.Unmarshal(detail.GetWorkflow().GetInput(), &input))
	assert.Equal(t, "wi-7", input["first_id"], "the declared input rendered against the polled events")
	assert.Equal(t, "feishu_mcp", input["trigger_source"])
	assert.Equal(t, float64(1), input["event_count"])
}

// triggerInputs shapes the envelope.
func TestTriggerInputsEnvelope(t *testing.T) {
	in := triggerInputs(registry.CompiledTrigger{Source: "feishu_mcp"}, []scheduler.Event{
		{ID: "1", Payload: map[string]any{"k": "v"}},
	})
	assert.Equal(t, "feishu_mcp", in["trigger_source"])
	assert.Equal(t, 1, in["event_count"])
	payloads, ok := in["events"].([]any)
	require.True(t, ok)
	require.Len(t, payloads, 1)
	assert.Equal(t, map[string]any{"k": "v"}, payloads[0])

	// Cron fires with no events: an empty envelope, not a nil panic.
	cronIn := triggerInputs(registry.CompiledTrigger{}, nil)
	assert.Equal(t, 0, cronIn["event_count"])
	assert.Empty(t, cronIn["events"])
}
