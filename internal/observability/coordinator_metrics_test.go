package observability

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// The adapter is the one place that knows which metric each call belongs to and
// in what order that metric declared its labels. If the two drift, values land
// under the wrong labels and the series becomes unreadable — so the drift is
// pinned here rather than discovered on a dashboard.
func TestCoordinatorMetricsWriteTheDeclaredSeries(t *testing.T) {
	m := NewMetrics()
	a := NewCoordinatorMetrics(m)

	a.WorkflowFinished("COMPLETED")
	a.WorkflowFinished("COMPLETED")
	a.WorkflowFinished("FAILED")

	a.TaskFinished("shell", "COMPLETED", 1.5)
	a.TaskFinished("shell", "FAILED", 0.25)

	a.TaskRetried("shell", "transient")

	a.ActiveWorkflows(3)
	a.QueueDepth(7)

	assert.Equal(t, 2.0, m.WorkflowsTotal.Value("COMPLETED"), "status is the first label")
	assert.Equal(t, 1.0, m.WorkflowsTotal.Value("FAILED"))

	assert.Equal(t, 1.0, m.TaskRetries.Value("shell", "transient"), "handler, then reason")

	assert.Equal(t, 3.0, m.ActiveWorkflows.Value())
	assert.Equal(t, 7.0, m.QueueDepth.Value("default"),
		"queue depth is reported under the label the metric declares")
}

// The adapter is nil-safe so a deployment that builds no metrics does not have
// to guard every call site.
func TestCoordinatorMetricsNilIsSafe(t *testing.T) {
	var a *CoordinatorMetrics
	assert.NotPanics(t, func() {
		a.WorkflowFinished("COMPLETED")
		a.TaskFinished("shell", "COMPLETED", 1)
		a.TaskRetried("shell", "why")
		a.ActiveWorkflows(1)
		a.QueueDepth(1)
	})

	// And one wrapping a nil Metrics, which is what a partially built assembler
	// would produce.
	a2 := NewCoordinatorMetrics(nil)
	assert.NotPanics(t, func() {
		a2.WorkflowFinished("COMPLETED")
		a2.QueueDepth(1)
	})
}

// The observations reach the exposition served at /metrics, so the series are
// actually readable rather than merely stored.
func TestCoordinatorMetricsAppearInTheExposition(t *testing.T) {
	m := NewMetrics()
	a := NewCoordinatorMetrics(m)
	a.WorkflowFinished("COMPLETED")
	a.QueueDepth(4)

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))

	body := rec.Body.String()
	assert.Contains(t, body, "forge_workflows_total")
	assert.Contains(t, body, "COMPLETED")
	assert.Contains(t, body, "forge_queue_depth")
	assert.True(t, strings.Contains(body, "4"), "the gauge's value is published")
}
