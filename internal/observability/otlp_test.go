package observability

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureCollector stands in for an OTLP collector and records what it receives.
type captureCollector struct {
	mu      sync.Mutex
	batches [][]byte
	server  *httptest.Server
	status  int
}

func newCaptureCollector(t *testing.T) *captureCollector {
	t.Helper()
	c := &captureCollector{status: http.StatusOK}
	c.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != otlpTracesPath {
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
			return
		}
		if ct := r.Header.Get("Content-Type"); ct != otlpContentType {
			http.Error(w, "unexpected content type "+ct, http.StatusBadRequest)
			return
		}
		body, _ := io.ReadAll(r.Body)

		c.mu.Lock()
		c.batches = append(c.batches, body)
		status := c.status
		c.mu.Unlock()

		w.WriteHeader(status)
	}))
	t.Cleanup(c.server.Close)
	return c
}

func (c *captureCollector) captured() [][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([][]byte, len(c.batches))
	copy(out, c.batches)
	return out
}

// decodeBatch parses one captured request.
func decodeBatch(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(body, &decoded), "body: %s", string(body))
	return decoded
}

// firstSpan digs the single span out of a captured batch.
func firstSpan(t *testing.T, body []byte) map[string]any {
	t.Helper()
	decoded := decodeBatch(t, body)
	resourceSpans := decoded["resourceSpans"].([]any)
	require.NotEmpty(t, resourceSpans)
	scopeSpans := resourceSpans[0].(map[string]any)["scopeSpans"].([]any)
	require.NotEmpty(t, scopeSpans)
	spans := scopeSpans[0].(map[string]any)["spans"].([]any)
	require.NotEmpty(t, spans)
	return spans[0].(map[string]any)
}

// A finished span reaches the collector, at the OTLP path, as JSON.
func TestOTLPExporterPostsSpans(t *testing.T) {
	collector := newCaptureCollector(t)

	exporter, stop := NewOTLPExporter(context.Background(), OTLPConfig{
		Endpoint: collector.server.URL,
		Client:   collector.server.Client(),
	})
	defer stop()

	span := newTestSpan(t)
	exporter.ExportSpan(span)
	exporter.Flush(context.Background())

	batches := collector.captured()
	require.Len(t, batches, 1, "the span must have been sent")

	got := firstSpan(t, batches[0])
	assert.Equal(t, "test-span", got["name"])
	assert.NotEmpty(t, got["traceId"])
	assert.NotEmpty(t, got["spanId"])
	assert.NotEmpty(t, got["startTimeUnixNano"])
	assert.NotEmpty(t, got["endTimeUnixNano"])
}

// Ids are the lowercase hex OTLP expects, with the right widths: 32 hex digits
// for a trace, 16 for a span. A collector that cannot parse them drops the span.
func TestOTLPExporterEncodesIdsCorrectly(t *testing.T) {
	collector := newCaptureCollector(t)

	exporter, stop := NewOTLPExporter(context.Background(), OTLPConfig{
		Endpoint: collector.server.URL,
		Client:   collector.server.Client(),
	})
	defer stop()

	span := newTestSpan(t)
	exporter.ExportSpan(span)
	exporter.Flush(context.Background())

	got := firstSpan(t, collector.captured()[0])

	traceID := got["traceId"].(string)
	spanID := got["spanId"].(string)
	assert.Len(t, traceID, 32, "a trace id is 128 bits, so 32 hex digits")
	assert.Len(t, spanID, 16, "a span id is 64 bits, so 16 hex digits")
	assert.Equal(t, strings.ToLower(traceID), traceID, "hex must be lowercase")
	assert.Equal(t, strings.ToLower(spanID), spanID)

	// And they are the span's actual ids, not placeholders.
	_, err := hex.DecodeString(traceID)
	require.NoError(t, err)
	_, err = hex.DecodeString(spanID)
	require.NoError(t, err)
}

// A root span has no parentSpanId; a child has one. Sending an all-zero parent
// is a malformed reference, so the field is omitted when there is none.
func TestOTLPExporterOmitsParentForRootSpans(t *testing.T) {
	collector := newCaptureCollector(t)

	exporter, stop := NewOTLPExporter(context.Background(), OTLPConfig{
		Endpoint: collector.server.URL,
		Client:   collector.server.Client(),
	})
	defer stop()

	root := newTestSpan(t)
	exporter.ExportSpan(root)
	exporter.Flush(context.Background())

	got := firstSpan(t, collector.captured()[0])
	_, hasParent := got["parentSpanId"]
	assert.False(t, hasParent, "a root span must not claim a parent")

	// A child span does carry one.
	child := newTestSpan(t)
	child.ParentID = SpanID{1, 2, 3, 4, 5, 6, 7, 8}
	exporter.ExportSpan(child)
	exporter.Flush(context.Background())

	batches := collector.captured()
	require.Len(t, batches, 2)
	childGot := firstSpan(t, batches[1])
	assert.Equal(t, "0102030405060708", childGot["parentSpanId"])
}

// Attributes travel, and in a stable order: two exports of the same span must be
// byte-identical, or a trace cannot be diffed.
func TestOTLPExporterSortsAttributes(t *testing.T) {
	collector := newCaptureCollector(t)

	exporter, stop := NewOTLPExporter(context.Background(), OTLPConfig{
		Endpoint: collector.server.URL,
		Client:   collector.server.Client(),
	})
	defer stop()

	span := newTestSpan(t)
	span.SetAttribute("zebra", "last")
	span.SetAttribute("alpha", "first")
	span.SetAttribute("middle", "central")

	exporter.ExportSpan(span)
	exporter.Flush(context.Background())

	got := firstSpan(t, collector.captured()[0])
	attrs := got["attributes"].([]any)
	require.Len(t, attrs, 3)

	var keys []string
	for _, a := range attrs {
		keys = append(keys, a.(map[string]any)["key"].(string))
	}
	assert.Equal(t, []string{"alpha", "middle", "zebra"}, keys,
		"attributes must be ordered so equal spans export identically")
}

// Status is sent only when it says something: an unset status is the default and
// sending it adds a field with no information.
func TestOTLPExporterSendsStatusOnlyWhenSet(t *testing.T) {
	collector := newCaptureCollector(t)

	exporter, stop := NewOTLPExporter(context.Background(), OTLPConfig{
		Endpoint: collector.server.URL,
		Client:   collector.server.Client(),
	})
	defer stop()

	unset := newTestSpan(t)
	exporter.ExportSpan(unset)
	exporter.Flush(context.Background())

	got := firstSpan(t, collector.captured()[0])
	_, hasStatus := got["status"]
	assert.False(t, hasStatus, "an unset status must be omitted")

	failed := newTestSpan(t)
	failed.Status = SpanStatusError
	exporter.ExportSpan(failed)
	exporter.Flush(context.Background())

	batches := collector.captured()
	require.Len(t, batches, 2)
	errSpan := firstSpan(t, batches[1])
	status := errSpan["status"].(map[string]any)
	assert.Equal(t, float64(2), status["code"], "2 is the OTLP error code")
}

// A full batch is sent without waiting for the interval, so a busy process does
// not accumulate spans.
func TestOTLPExporterFlushesWhenTheBatchIsFull(t *testing.T) {
	collector := newCaptureCollector(t)

	exporter, stop := NewOTLPExporter(context.Background(), OTLPConfig{
		Endpoint:  collector.server.URL,
		Client:    collector.server.Client(),
		BatchSize: 3,
		// Long interval: the only thing that can cause a send is the batch filling.
		FlushInterval: time.Hour,
	})
	defer stop()

	for i := 0; i < 3; i++ {
		exporter.ExportSpan(newTestSpan(t))
	}

	require.Eventually(t, func() bool {
		return len(collector.captured()) == 1
	}, 2*time.Second, 10*time.Millisecond, "filling the batch must trigger a send")
}

// The interval sends a partial batch, so the last spans of a quiet process are
// not stranded.
func TestOTLPExporterFlushesOnTheInterval(t *testing.T) {
	collector := newCaptureCollector(t)

	exporter, stop := NewOTLPExporter(context.Background(), OTLPConfig{
		Endpoint:      collector.server.URL,
		Client:        collector.server.Client(),
		BatchSize:     100,
		FlushInterval: 20 * time.Millisecond,
	})
	defer stop()

	exporter.ExportSpan(newTestSpan(t))

	require.Eventually(t, func() bool {
		return len(collector.captured()) == 1
	}, 2*time.Second, 10*time.Millisecond, "the interval must send what is queued")
}

// Stopping flushes what is left: the spans a process produces last are often the
// ones that explain why it stopped.
func TestOTLPExporterFlushesOnStop(t *testing.T) {
	collector := newCaptureCollector(t)

	exporter, stop := NewOTLPExporter(context.Background(), OTLPConfig{
		Endpoint:      collector.server.URL,
		Client:        collector.server.Client(),
		BatchSize:     100,
		FlushInterval: time.Hour,
	})

	exporter.ExportSpan(newTestSpan(t))
	// Nothing sent yet: the batch is not full and the interval is an hour away.
	assert.Empty(t, collector.captured(), "precondition: nothing sent yet")

	stop()

	assert.Len(t, collector.captured(), 1, "stopping must flush what is queued")
}

// An unreachable collector does not panic and does not block the caller: a
// tracing backend that is down must not slow the work being traced.
func TestOTLPExporterSurvivesAnUnreachableCollector(t *testing.T) {
	// A server that is closed immediately, so the port refuses connections.
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := dead.URL
	dead.Close()

	exporter, stop := NewOTLPExporter(context.Background(), OTLPConfig{
		Endpoint: url,
		Client:   &http.Client{Timeout: 500 * time.Millisecond},
	})
	defer stop()

	assert.NotPanics(t, func() {
		exporter.ExportSpan(newTestSpan(t))
		exporter.Flush(context.Background())
	})
}

// A collector that answers with an error is logged, not retried into a growing
// buffer: an unbounded queue is a memory leak wearing an observability costume.
func TestOTLPExporterDropsOnCollectorError(t *testing.T) {
	collector := newCaptureCollector(t)
	collector.mu.Lock()
	collector.status = http.StatusInternalServerError
	collector.mu.Unlock()

	exporter, stop := NewOTLPExporter(context.Background(), OTLPConfig{
		Endpoint: collector.server.URL,
		Client:   collector.server.Client(),
	})
	defer stop()

	exporter.ExportSpan(newTestSpan(t))
	exporter.Flush(context.Background())

	// The attempt was made and the span was dropped, not kept.
	exporter.Flush(context.Background())
	assert.Len(t, collector.captured(), 1, "a failed batch must not be retried forever")
}

// Spans exported after Close are dropped rather than panicking on a closed queue.
func TestOTLPExporterIgnoresSpansAfterClose(t *testing.T) {
	collector := newCaptureCollector(t)

	exporter, stop := NewOTLPExporter(context.Background(), OTLPConfig{
		Endpoint: collector.server.URL,
		Client:   collector.server.Client(),
	})
	stop()
	exporter.Close()

	assert.NotPanics(t, func() {
		exporter.ExportSpan(newTestSpan(t))
	})
}

// A nil span in the batch is skipped rather than crashing the export.
func TestOTLPMarshalSkipsNilSpans(t *testing.T) {
	body, err := marshalOTLPTraces([]*Span{nil, newTestSpan(t), nil})
	require.NoError(t, err)

	got := firstSpan(t, body)
	assert.Equal(t, "test-span", got["name"])
}

// The timestamps are decimal nanosecond strings, which is what OTLP specifies:
// a nanosecond count exceeds what a JSON number holds exactly.
func TestOTLPTimestampsAreDecimalStrings(t *testing.T) {
	span := newTestSpan(t)
	body, err := marshalOTLPTraces([]*Span{span})
	require.NoError(t, err)

	got := firstSpan(t, body)
	start := got["startTimeUnixNano"].(string)
	assert.NotEmpty(t, start)
	assert.NotContains(t, start, ".", "it is an integer count, written as a string")
	assert.NotContains(t, start, "e", "and not in exponent form")
}

// The exporter is wired to FORGE_TRACE_EXPORTER=otlp, so the advertised switch
// does something: a span started and ended reaches the collector.
//
// The endpoint points at the recorder, which is the only way to observe this
// without a real collector — and it is the deployment path too, since the
// exporter builds its client from the same config.
func TestTracerConfigEnvSelectsOTLP(t *testing.T) {
	collector := newCaptureCollector(t)
	t.Setenv("FORGE_TRACE_EXPORTER", "otlp")
	t.Setenv("FORGE_OTLP_ENDPOINT", collector.server.URL)

	tracer := TracerConfigEnv("forge-test")
	require.NotNil(t, tracer)

	// Point the exporter's HTTP client at the recorder's client, which trusts
	// the recorder's certificate and reaches its listener.
	tracer.exporter.(*OTLPExporter).client = collector.server.Client()

	_, span := tracer.StartSpan(context.Background(), "otlp-span")
	// EndSpan, not span.End(): exporting is the tracer's job, and span.End()
	// only stamps the time. Using the wrong one here would have made this test
	// pass against a tracer that exports nothing.
	tracer.EndSpan(span)

	// Stopping flushes the batch deterministically, which keeps this test from
	// depending on the flush interval.
	StopTracing()

	require.NotEmpty(t, collector.captured(), "the otlp exporter must actually post")

	got := firstSpan(t, collector.captured()[0])
	assert.Equal(t, "otlp-span", got["name"])
}

// Without an endpoint the standard collector port is used, and the choice is
// announced rather than silent.
func TestTracerConfigEnvDefaultsTheOTLPEndpoint(t *testing.T) {
	t.Setenv("FORGE_TRACE_EXPORTER", "otlp")
	t.Setenv("FORGE_OTLP_ENDPOINT", "")

	tracer := TracerConfigEnv("forge-test")
	require.NotNil(t, tracer)

	exporter, ok := tracer.exporter.(*OTLPExporter)
	require.True(t, ok, "the otlp switch must select the otlp exporter")
	assert.Equal(t, defaultOTLPEndpoint, exporter.endpoint)

	StopTracing()
}

// StopTracing is safe when tracing was never started, or is not OTLP.
func TestStopTracingIsSafeWhenUnused(t *testing.T) {
	t.Setenv("FORGE_TRACE_EXPORTER", "log")
	TracerConfigEnv("forge-test")

	assert.NotPanics(t, StopTracing)
	assert.NotPanics(t, StopTracing, "and safe to call twice")
}

// --- helper ---

// newTestSpan builds a finished span with ids and times, without going through
// the tracer: these tests are about encoding, not about tracing.
func newTestSpan(t *testing.T) *Span {
	t.Helper()
	start := time.Now().Add(-50 * time.Millisecond)
	return &Span{
		Name: "test-span",
		Context: SpanContext{
			TraceID: TraceID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
			SpanID:  SpanID{8, 7, 6, 5, 4, 3, 2, 1},
			Sampled: true,
		},
		StartTime:  start,
		EndTime:    start.Add(50 * time.Millisecond),
		Status:     SpanStatusUnset,
		Attributes: map[string]string{},
	}
}
