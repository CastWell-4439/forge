package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/castwell/forge/internal/registry"
)

// The bridge against the repository's real workflow file: this is the seam
// between validation (④) and dispatch (⑤), so it must faithfully carry the
// contract's shape — worker → handler, action in params, outputs, and the
// stage-derived dependency edges.
func TestBridgeDAGFromBugFixWorkflow(t *testing.T) {
	reg := registry.NewRegistry()
	require.NoError(t, reg.Load("../../workflows"))

	cw, err := reg.Get("bug_fix")
	require.NoError(t, err)

	dag, err := bridgeDAG(cw)
	require.NoError(t, err, "the shipped workflow must survive bridging + coordinator validation")

	assert.Equal(t, "bug_fix", dag.Name)
	assert.Equal(t, 1, dag.Version, `metadata version "1.0" parses to major 1`)

	// 6 stages: 3+1+1+2+3+2 tasks.
	require.Len(t, dag.Tasks, 12)

	first := dag.Tasks["investigate.0"]
	require.NotNil(t, first, "node IDs are stage.task")
	assert.Equal(t, "mcp", first.Handler, "worker becomes handler")
	assert.Equal(t, "get_workitem", first.Params["action"], "action travels in params")
	assert.Equal(t, "bug_info", first.Output, "output name carries over")

	// Parallel first stage → the next stage's task depends on all three.
	analyze := dag.Tasks["analyze.0"]
	require.NotNil(t, analyze)
	assert.ElementsMatch(t, []string{"investigate.0", "investigate.1", "investigate.2"}, analyze.DependsOn)

	// Sequential tasks inside a stage chain.
	deliver2 := dag.Tasks["deliver.1"]
	require.NotNil(t, deliver2)
	assert.Contains(t, deliver2.DependsOn, "deliver.0")
}

// Retry policy survives the bridge with the same defaults the YAML parser
// applies on its own side.
func TestBridgeDAGCarriesRetryPolicy(t *testing.T) {
	yaml := []byte(`
apiVersion: forge/v1
kind: Workflow
metadata:
  name: retry_case
  version: "2.3"
stages:
  - name: only
    tasks:
      - worker: shell
        action: run
        params:
          command: "echo {{.inputs.msg}}"
        output: first
        retry:
          max_attempts: 3
          backoff: exponential
          interval: 5s
`)
	cw, err := registry.Compile(yaml)
	require.NoError(t, err)

	dag, err := bridgeDAG(cw)
	require.NoError(t, err)

	task := dag.Tasks["only.0"]
	require.NotNil(t, task)
	assert.Equal(t, 3, task.Retry.MaxAttempts)
	assert.Equal(t, "exponential", string(task.Retry.BackoffType))
	assert.Equal(t, 5e9, float64(task.Retry.InitialInterval), "interval parses to 5s")
	assert.Equal(t, 2.0, task.Retry.Multiplier, "the parser's own default multiplier")
	assert.Equal(t, 2, dag.Version)
}

// The dispatch-time renderer wires the registry template engine into the
// coordinator seam: outputs sit at the top level next to inputs, which is
// how workflow YAML references them.
func TestRegistryParamRenderer(t *testing.T) {
	render := registryParamRenderer()

	params, err := render(
		map[string]any{
			"prompt":  "analyze {{.inputs.topic}}",
			"context": "{{.bug_info}}",
			"static":  "untouched",
		},
		map[string]any{"topic": "login bug"},
		map[string]any{"bug_info": map[string]any{"file": "auth.go"}},
	)
	require.NoError(t, err)

	assert.Equal(t, "analyze login bug", params["prompt"])
	assert.Equal(t, map[string]any{"file": "auth.go"}, params["context"], "outputs resolve structurally")
	assert.Equal(t, "untouched", params["static"])
}

// Unparseable versions default to 0 — the same value the YAML path leaves
// when no version is declared.
func TestBridgeVersionDefaults(t *testing.T) {
	assert.Equal(t, 3, bridgeVersion("3.14"))
	assert.Equal(t, 0, bridgeVersion("weird"))
	assert.Equal(t, 0, bridgeVersion(""))
}
