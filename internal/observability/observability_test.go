package observability

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Metrics Tests ---

func TestCounterIncAndValue(t *testing.T) {
	c := newCounter("test_total", "test", []string{"status"})
	c.Inc("ok")
	c.Inc("ok")
	c.Inc("fail")

	assert.Equal(t, 2.0, c.Value("ok"))
	assert.Equal(t, 1.0, c.Value("fail"))
	assert.Equal(t, 0.0, c.Value("unknown"))
}

func TestGaugeSetAndValue(t *testing.T) {
	g := newGauge("test_gauge", "test", []string{"lang"})
	g.Set(5, "go")
	g.Inc("go")
	g.Dec("go")

	assert.Equal(t, 5.0, g.Value("go"))

	g.Set(10, "python")
	assert.Equal(t, 10.0, g.Value("python"))
}

func TestHistogramObserve(t *testing.T) {
	h := newHistogram("test_duration", "test", []string{"handler"}, []float64{0.1, 0.5, 1.0})
	h.Observe(0.05, "fetch")
	h.Observe(0.3, "fetch")
	h.Observe(2.0, "fetch")

	h.mu.RLock()
	data := h.counts[labelsKey([]string{"fetch"})]
	h.mu.RUnlock()

	require.NotNil(t, data)
	assert.Equal(t, uint64(3), data.count)
	assert.InDelta(t, 2.35, data.sum, 0.01)
	assert.Equal(t, uint64(1), data.buckets[0]) // <= 0.1
	assert.Equal(t, uint64(2), data.buckets[1]) // <= 0.5
	assert.Equal(t, uint64(2), data.buckets[2]) // <= 1.0 (2.0 doesn't fit)
}

func TestHistogramObserveDuration(t *testing.T) {
	h := newHistogram("dur", "test", nil, DefaultBuckets)
	start := time.Now().Add(-100 * time.Millisecond)
	h.ObserveDuration(start)

	h.mu.RLock()
	data := h.counts[""]
	h.mu.RUnlock()
	require.NotNil(t, data)
	assert.Equal(t, uint64(1), data.count)
	assert.Greater(t, data.sum, 0.05) // at least 50ms
}

func TestMetricsHandler(t *testing.T) {
	m := NewMetrics()
	m.WorkflowsTotal.Inc("completed")
	m.WorkflowsTotal.Inc("completed")
	m.WorkflowsTotal.Inc("failed")
	m.ActiveWorkflows.Set(5)
	m.TaskDuration.Observe(1.5, "ffmpeg", "ok")
	m.WorkerPoolSize.Set(3, "go", "idle")
	m.QueueDepth.Set(7, "high")

	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, req)

	body, _ := io.ReadAll(rec.Body)
	text := string(body)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, text, "forge_workflows_total")
	assert.Contains(t, text, "forge_active_workflows")
	assert.Contains(t, text, "forge_task_duration_seconds")
	assert.Contains(t, text, "forge_worker_pool_size")
	assert.Contains(t, text, "forge_queue_depth")
	assert.Contains(t, text, `status="completed"`)
	assert.Contains(t, text, `status="failed"`)
}

// --- Tracing Tests ---

func TestTracerStartSpan(t *testing.T) {
	exporter := &InMemoryExporter{}
	tracer := NewTracer(TracerConfig{
		ServiceName: "test",
		Exporter:    exporter,
		SampleRate:  1.0,
	})

	ctx, span := tracer.StartSpan(context.Background(), "test-op")
	span.SetAttribute("key", "value")
	span.AddEvent("started", nil)
	span.SetStatus(SpanStatusOK, "")
	tracer.EndSpan(span)

	assert.Equal(t, 1, exporter.Count())
	assert.Equal(t, "test-op", exporter.Spans[0].Name)
	assert.Equal(t, "value", exporter.Spans[0].Attributes["key"])
	assert.NotZero(t, span.Context.TraceID)
	assert.NotZero(t, span.Context.SpanID)

	// Verify context carries span.
	extracted, ok := SpanFromContext(ctx)
	assert.True(t, ok)
	assert.Equal(t, span, extracted)
}

func TestTracerChildSpan(t *testing.T) {
	exporter := &InMemoryExporter{}
	tracer := NewTracer(TracerConfig{
		ServiceName: "test",
		Exporter:    exporter,
		SampleRate:  1.0,
	})

	ctx, parent := tracer.StartSpan(context.Background(), "parent")
	_, child := tracer.StartSpan(ctx, "child")
	tracer.EndSpan(child)
	tracer.EndSpan(parent)

	assert.Equal(t, 2, exporter.Count())
	// Child should share parent's trace ID.
	assert.Equal(t, parent.Context.TraceID, child.Context.TraceID)
	// Child's parent ID should be parent's span ID.
	assert.Equal(t, parent.Context.SpanID, child.ParentID)
}

func TestInjectExtractTraceContext(t *testing.T) {
	tracer := NewTracer(TracerConfig{ServiceName: "test", SampleRate: 1.0})
	ctx, span := tracer.StartSpan(context.Background(), "op")

	headers := InjectTraceContext(ctx)
	assert.Contains(t, headers, "traceparent")

	tp := headers["traceparent"]
	assert.True(t, strings.HasPrefix(tp, "00-"))

	sc, ok := ExtractTraceContext(headers)
	assert.True(t, ok)
	assert.Equal(t, span.Context.TraceID, sc.TraceID)
	assert.Equal(t, span.Context.SpanID, sc.SpanID)
}

func TestSpanContextString(t *testing.T) {
	sc := SpanContext{Sampled: true}
	s := sc.String()
	assert.True(t, strings.HasPrefix(s, "00-"))
	assert.True(t, strings.HasSuffix(s, "-01"))
}

// --- Profiling Tests ---

func TestProfilerCollect(t *testing.T) {
	p := NewProfiler(ProfilingConfig{
		Enabled:  true,
		Interval: 50 * time.Millisecond,
		Types:    []ProfileType{ProfileHeap, ProfileGoroutine},
	})

	p.Start()
	time.Sleep(200 * time.Millisecond)
	p.Stop()

	assert.Greater(t, p.SnapshotCount(), 0)

	snapshots := p.LatestSnapshots(5)
	assert.LessOrEqual(t, len(snapshots), 5)

	// Verify heap snapshot has expected fields.
	for _, s := range snapshots {
		if s.Type == ProfileHeap {
			assert.Contains(t, s.Data, "alloc_bytes")
			assert.Contains(t, s.Data, "heap_objects")
		}
		if s.Type == ProfileGoroutine {
			assert.Contains(t, s.Data, "count")
			count := s.Data["count"].(int)
			assert.Greater(t, count, 0)
		}
	}
}

func TestProfilerDebugHandler(t *testing.T) {
	p := NewProfiler(DefaultProfilingConfig())

	req := httptest.NewRequest("GET", "/debug/profile", nil)
	rec := httptest.NewRecorder()
	p.DebugHandler().ServeHTTP(rec, req)

	body := rec.Body.String()
	assert.Contains(t, body, "goroutines")
	assert.Contains(t, body, "heap_alloc")
	assert.Contains(t, body, "gomaxprocs")
}

func TestProfilerDisabled(t *testing.T) {
	p := NewProfiler(ProfilingConfig{Enabled: false})
	p.Start() // should not panic or start goroutine
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, 0, p.SnapshotCount())
}
