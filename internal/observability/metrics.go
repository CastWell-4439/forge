// Package observability provides metrics, tracing, and profiling for Forge.
package observability

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// --- Metric Types ---

// Counter is a monotonically increasing counter.
type Counter struct {
	name   string
	help   string
	labels []string
	values map[string]float64
	mu     sync.RWMutex
}

// Histogram tracks value distributions.
type Histogram struct {
	name    string
	help    string
	labels  []string
	buckets []float64
	counts  map[string]*histogramData
	mu      sync.RWMutex
}

type histogramData struct {
	count   uint64
	sum     float64
	buckets []uint64 // parallel to Histogram.buckets
}

// Gauge is a value that can go up and down.
type Gauge struct {
	name   string
	help   string
	labels []string
	values map[string]float64
	mu     sync.RWMutex
}

// --- Forge Metrics (tech-spec 8.1) ---

// Metrics holds all Forge Prometheus metrics.
type Metrics struct {
	WorkflowsTotal  *Counter   // 1. Workflow throughput by status
	TaskDuration    *Histogram // 2. Task execution duration
	ActiveWorkflows *Gauge     // 3. Currently active workflows
	WorkerPoolSize  *Gauge     // 4. Workers by language and state
	TaskRetries     *Counter   // 5. Task retry count
	QueueDepth      *Gauge     // 6. Queue depth by priority
}

// DefaultBuckets for task duration histogram (seconds).
var DefaultBuckets = []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300}

// NewMetrics creates all Forge metrics.
func NewMetrics() *Metrics {
	return &Metrics{
		WorkflowsTotal:  newCounter("forge_workflows_total", "Total workflows processed", []string{"status"}),
		TaskDuration:    newHistogram("forge_task_duration_seconds", "Task execution duration", []string{"handler", "status"}, DefaultBuckets),
		ActiveWorkflows: newGauge("forge_active_workflows", "Currently active workflows", nil),
		WorkerPoolSize:  newGauge("forge_worker_pool_size", "Workers in pool", []string{"language", "state"}),
		TaskRetries:     newCounter("forge_task_retries_total", "Task retries", []string{"handler", "reason"}),
		QueueDepth:      newGauge("forge_queue_depth", "Tasks waiting in queue", []string{"priority"}),
	}
}

// --- Constructors ---

func newCounter(name, help string, labels []string) *Counter {
	return &Counter{name: name, help: help, labels: labels, values: make(map[string]float64)}
}

func newHistogram(name, help string, labels []string, buckets []float64) *Histogram {
	return &Histogram{name: name, help: help, labels: labels, buckets: buckets, counts: make(map[string]*histogramData)}
}

func newGauge(name, help string, labels []string) *Gauge {
	return &Gauge{name: name, help: help, labels: labels, values: make(map[string]float64)}
}

// --- Counter ---

func (c *Counter) Inc(labelValues ...string) { c.Add(1, labelValues...) }

func (c *Counter) Add(v float64, labelValues ...string) {
	key := labelsKey(labelValues)
	c.mu.Lock()
	c.values[key] += v
	c.mu.Unlock()
}

func (c *Counter) Value(labelValues ...string) float64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.values[labelsKey(labelValues)]
}

// --- Histogram ---

func (h *Histogram) Observe(v float64, labelValues ...string) {
	key := labelsKey(labelValues)
	h.mu.Lock()
	data, ok := h.counts[key]
	if !ok {
		data = &histogramData{buckets: make([]uint64, len(h.buckets))}
		h.counts[key] = data
	}
	data.count++
	data.sum += v
	for i, bound := range h.buckets {
		if v <= bound {
			data.buckets[i]++
		}
	}
	h.mu.Unlock()
}

func (h *Histogram) ObserveDuration(start time.Time, labelValues ...string) {
	h.Observe(time.Since(start).Seconds(), labelValues...)
}

// --- Gauge ---

func (g *Gauge) Set(v float64, labelValues ...string) {
	g.mu.Lock()
	g.values[labelsKey(labelValues)] = v
	g.mu.Unlock()
}

func (g *Gauge) Inc(labelValues ...string) { g.Add(1, labelValues...) }
func (g *Gauge) Dec(labelValues ...string) { g.Add(-1, labelValues...) }

func (g *Gauge) Add(v float64, labelValues ...string) {
	key := labelsKey(labelValues)
	g.mu.Lock()
	g.values[key] += v
	g.mu.Unlock()
}

func (g *Gauge) Value(labelValues ...string) float64 {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.values[labelsKey(labelValues)]
}

// --- Key Helpers ---

func labelsKey(vals []string) string {
	return strings.Join(vals, "|")
}

// --- HTTP /metrics Handler ---

func (m *Metrics) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		writeCounter(w, m.WorkflowsTotal)
		writeHistogram(w, m.TaskDuration)
		writeGauge(w, m.ActiveWorkflows)
		writeGauge(w, m.WorkerPoolSize)
		writeCounter(w, m.TaskRetries)
		writeGauge(w, m.QueueDepth)
	})
}

func writeCounter(w http.ResponseWriter, c *Counter) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s counter\n", c.name, c.help, c.name)
	if len(c.values) == 0 {
		fmt.Fprintf(w, "%s 0\n", c.name)
		return
	}
	for key, val := range c.values {
		fmt.Fprintf(w, "%s%s %g\n", c.name, fmtLabels(c.labels, key), val)
	}
}

func writeGauge(w http.ResponseWriter, g *Gauge) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s gauge\n", g.name, g.help, g.name)
	if len(g.values) == 0 {
		fmt.Fprintf(w, "%s 0\n", g.name)
		return
	}
	for key, val := range g.values {
		fmt.Fprintf(w, "%s%s %g\n", g.name, fmtLabels(g.labels, key), val)
	}
}

func writeHistogram(w http.ResponseWriter, h *Histogram) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s histogram\n", h.name, h.help, h.name)
	for key, data := range h.counts {
		base := fmtLabels(h.labels, key)
		for i, bound := range h.buckets {
			fmt.Fprintf(w, "%s_bucket%s %d\n", h.name, addLE(base, fmt.Sprintf("%g", bound)), data.buckets[i])
		}
		fmt.Fprintf(w, "%s_bucket%s %d\n", h.name, addLE(base, "+Inf"), data.count)
		fmt.Fprintf(w, "%s_sum%s %g\n", h.name, base, data.sum)
		fmt.Fprintf(w, "%s_count%s %d\n", h.name, base, data.count)
	}
}

func fmtLabels(names []string, key string) string {
	if len(names) == 0 || key == "" {
		return ""
	}
	vals := strings.Split(key, "|")
	pairs := make([]string, 0, len(names))
	for i, name := range names {
		v := ""
		if i < len(vals) {
			v = vals[i]
		}
		pairs = append(pairs, fmt.Sprintf(`%s="%s"`, name, v))
	}
	return "{" + strings.Join(pairs, ",") + "}"
}

func addLE(base, le string) string {
	if base == "" {
		return fmt.Sprintf(`{le="%s"}`, le)
	}
	return base[:len(base)-1] + fmt.Sprintf(`,le="%s"}`, le)
}
