package observability

// CoordinatorMetrics adapts *Metrics to the coordinator's reporting interface.
//
// It lives here rather than in the coordinator because the dependency runs one
// way: the coordinator defines what it needs to report (its MetricsSink) and
// this package knows how to put that into Prometheus series. The coordinator
// never imports observability.
//
// The adapter also owns the label contract. The sink's methods take label values
// without names, so this is the one place that knows which metric each call
// belongs to and in what order its labels were declared — if the two ever drift,
// they drift here, visibly, in one screen of code.
type CoordinatorMetrics struct {
	metrics *Metrics
}

// NewCoordinatorMetrics wraps a Metrics value for the coordinator.
func NewCoordinatorMetrics(m *Metrics) *CoordinatorMetrics {
	return &CoordinatorMetrics{metrics: m}
}

// WorkflowFinished counts a workflow reaching a terminal state.
// Labels: status.
func (a *CoordinatorMetrics) WorkflowFinished(status string) {
	if a == nil || a.metrics == nil {
		return
	}
	a.metrics.WorkflowsTotal.Inc(status)
}

// TaskFinished records a task's duration and outcome.
// Labels: handler, status.
func (a *CoordinatorMetrics) TaskFinished(handler, status string, seconds float64) {
	if a == nil || a.metrics == nil {
		return
	}
	a.metrics.TaskDuration.Observe(seconds, handler, status)
}

// TaskRetried counts a rescheduled task.
// Labels: handler, reason.
func (a *CoordinatorMetrics) TaskRetried(handler, reason string) {
	if a == nil || a.metrics == nil {
		return
	}
	a.metrics.TaskRetries.Inc(handler, reason)
}

// ActiveWorkflows reports how many workflows are running.
// No labels.
func (a *CoordinatorMetrics) ActiveWorkflows(n float64) {
	if a == nil || a.metrics == nil {
		return
	}
	a.metrics.ActiveWorkflows.Set(n)
}

// QueueDepth reports how many tasks are waiting.
// Label: priority.
//
// The label is fixed to "default" for now: the scheduler does not distinguish
// priorities, and inventing a value per task would be reporting a distinction
// the system does not make.
func (a *CoordinatorMetrics) QueueDepth(n float64) {
	if a == nil || a.metrics == nil {
		return
	}
	a.metrics.QueueDepth.Set(n, "default")
}
