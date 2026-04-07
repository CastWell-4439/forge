package coordinator

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const videoProductionYAML = `
name: video-production
version: 1
timeout: 3600s

tasks:
  fetch-data:
    handler: feishu.pull
    params:
      source_id: 123
    timeout: 300s
    retry:
      max_attempts: 3
      backoff: exponential
      initial_interval: 5s

  ai-generate:
    handler: ai.generate
    depends_on: [fetch-data]
    params:
      model: gpt-4
    timeout: 600s

  render-video:
    handler: video.render
    depends_on: [ai-generate]
    timeout: 1800s

  upload:
    handler: oss.upload
    depends_on: [render-video]
    timeout: 300s
    retry:
      max_attempts: 5
      backoff: exponential

  notify:
    handler: feishu.notify
    depends_on: [upload]
    timeout: 30s
`

func TestParseDAG_VideoProduction(t *testing.T) {
	dag, err := ParseDAG([]byte(videoProductionYAML))
	require.NoError(t, err)

	assert.Equal(t, "video-production", dag.Name)
	assert.Equal(t, 1, dag.Version)
	assert.Equal(t, 3600*time.Second, dag.Timeout)
	assert.Len(t, dag.Tasks, 5)

	// Verify fetch-data task
	fetchData := dag.Tasks["fetch-data"]
	require.NotNil(t, fetchData)
	assert.Equal(t, "feishu.pull", fetchData.Handler)
	assert.Equal(t, 300*time.Second, fetchData.Timeout)
	assert.Equal(t, 3, fetchData.Retry.MaxAttempts)
	assert.Equal(t, BackoffExponential, fetchData.Retry.BackoffType)
	assert.Equal(t, 5*time.Second, fetchData.Retry.InitialInterval)
	assert.Empty(t, fetchData.DependsOn)

	// Verify ai-generate depends on fetch-data
	aiGen := dag.Tasks["ai-generate"]
	require.NotNil(t, aiGen)
	assert.Equal(t, []string{"fetch-data"}, aiGen.DependsOn)
	assert.Equal(t, "ai.generate", aiGen.Handler)

	// Verify upload retry
	upload := dag.Tasks["upload"]
	require.NotNil(t, upload)
	assert.Equal(t, 5, upload.Retry.MaxAttempts)
}

func TestTopologicalSort_LinearDAG(t *testing.T) {
	// A -> B -> C (linear chain)
	dag := &DAG{
		Name: "linear",
		Tasks: map[string]*TaskDef{
			"A": {Name: "A", Handler: "h1"},
			"B": {Name: "B", Handler: "h2", DependsOn: []string{"A"}},
			"C": {Name: "C", Handler: "h3", DependsOn: []string{"B"}},
		},
		Edges: map[string][]string{
			"A": {},
			"B": {"A"},
			"C": {"B"},
		},
	}

	sorted, err := dag.TopologicalSort()
	require.NoError(t, err)
	assert.Equal(t, []string{"A", "B", "C"}, sorted)
}

func TestTopologicalSort_FanOutDAG(t *testing.T) {
	// A -> B, A -> C, B -> D, C -> D (diamond/fan-out)
	dag := &DAG{
		Name: "fan-out",
		Tasks: map[string]*TaskDef{
			"A": {Name: "A", Handler: "h1"},
			"B": {Name: "B", Handler: "h2", DependsOn: []string{"A"}},
			"C": {Name: "C", Handler: "h3", DependsOn: []string{"A"}},
			"D": {Name: "D", Handler: "h4", DependsOn: []string{"B", "C"}},
		},
		Edges: map[string][]string{
			"A": {},
			"B": {"A"},
			"C": {"A"},
			"D": {"B", "C"},
		},
	}

	sorted, err := dag.TopologicalSort()
	require.NoError(t, err)
	require.Len(t, sorted, 4)
	// A must be first, D must be last
	assert.Equal(t, "A", sorted[0])
	assert.Equal(t, "D", sorted[3])
	// B and C must be in the middle (order between them is deterministic due to sort)
	assert.Contains(t, sorted[1:3], "B")
	assert.Contains(t, sorted[1:3], "C")
}

func TestTopologicalSort_CyclicDAG(t *testing.T) {
	// A -> B -> C -> A (cycle)
	dag := &DAG{
		Name: "cyclic",
		Tasks: map[string]*TaskDef{
			"A": {Name: "A", Handler: "h1", DependsOn: []string{"C"}},
			"B": {Name: "B", Handler: "h2", DependsOn: []string{"A"}},
			"C": {Name: "C", Handler: "h3", DependsOn: []string{"B"}},
		},
		Edges: map[string][]string{
			"A": {"C"},
			"B": {"A"},
			"C": {"B"},
		},
	}

	_, err := dag.TopologicalSort()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DAG contains cycle")
}

func TestValidate_OrphanNode(t *testing.T) {
	dag := &DAG{
		Name: "orphan",
		Tasks: map[string]*TaskDef{
			"A": {Name: "A", Handler: "h1"},
			"B": {Name: "B", Handler: "h2", DependsOn: []string{"nonexistent"}},
		},
		Edges: map[string][]string{
			"A": {},
			"B": {"nonexistent"},
		},
	}

	err := dag.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nonexistent")
	assert.Contains(t, err.Error(), "does not exist")
}

func TestValidate_TimeoutSanity(t *testing.T) {
	dag := &DAG{
		Name:    "timeout-test",
		Timeout: 60 * time.Second,
		Tasks: map[string]*TaskDef{
			"A": {Name: "A", Handler: "h1", Timeout: 120 * time.Second},
		},
		Edges: map[string][]string{"A": {}},
	}

	err := dag.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds workflow timeout")
}

func TestParseDAG_RoundTrip(t *testing.T) {
	// Parse the video-production YAML and verify it validates and sorts correctly
	dag, err := ParseDAG([]byte(videoProductionYAML))
	require.NoError(t, err)

	err = dag.Validate()
	require.NoError(t, err)

	sorted, err := dag.TopologicalSort()
	require.NoError(t, err)
	require.Len(t, sorted, 5)

	// fetch-data must be first (no deps)
	assert.Equal(t, "fetch-data", sorted[0])
	// notify must be last (depends on upload which depends on render-video...)
	assert.Equal(t, "notify", sorted[4])

	// Verify the full chain order
	indexOf := func(name string) int {
		for i, n := range sorted {
			if n == name {
				return i
			}
		}
		return -1
	}
	assert.Less(t, indexOf("fetch-data"), indexOf("ai-generate"))
	assert.Less(t, indexOf("ai-generate"), indexOf("render-video"))
	assert.Less(t, indexOf("render-video"), indexOf("upload"))
	assert.Less(t, indexOf("upload"), indexOf("notify"))
}
