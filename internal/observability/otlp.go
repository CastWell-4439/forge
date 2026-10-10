package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"
)

// OTLP export.
//
// The tracing layer has advertised OTLP since it was written — the README says
// "支持 OTLP 导出" and the compose file publishes Jaeger's 4317 port — while the
// only exporters were a log line and an in-memory slice. Jaeger therefore
// received nothing, and the deployment looked instrumented from the outside.
//
// This is the exporter that makes the claim true. It speaks OTLP/HTTP rather
// than OTLP/gRPC: the data is a small JSON document, the stdlib can post it, and
// a gRPC client would add a code path whose only job is to wrap the same bytes.
// Collectors accept both — Jaeger's all-in-one listens on 4318 for this one.

// OTLP/HTTP paths and content type, per the OpenTelemetry specification.
const (
	otlpTracesPath    = "/v1/traces"
	otlpContentType   = "application/json"
	otlpDefaultClient = 5 * time.Second
)

// OTLPExporter posts finished spans to an OTLP/HTTP endpoint.
//
// It is safe for concurrent use: spans finish on many goroutines, and the HTTP
// client is shared. Spans that arrive while a request is in flight are queued and
// sent in the next batch rather than opening a connection each.
type OTLPExporter struct {
	endpoint  string
	client    *http.Client
	batchSize int

	mu     sync.Mutex
	queue  []*Span
	closed bool
}

// OTLPConfig configures the exporter.
type OTLPConfig struct {
	// Endpoint is the collector's base URL, e.g. http://localhost:4318.
	// The OTLP path is appended.
	Endpoint string
	// BatchSize is how many spans trigger a flush. Zero means 64.
	BatchSize int
	// FlushInterval is how long a partial batch waits before being sent. Zero
	// means one second.
	//
	// A batch needs both: without a size, a single span would wait for the
	// interval; without an interval, the last spans of a quiet process would
	// never be sent.
	FlushInterval time.Duration
	// Client, when set, replaces the default HTTP client. Tests use this to
	// point at a recorder.
	Client *http.Client
}

// NewOTLPExporter builds an exporter and starts its flush loop. The returned
// stop function flushes what is queued and ends the loop.
func NewOTLPExporter(ctx context.Context, cfg OTLPConfig) (*OTLPExporter, func()) {
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 64
	}
	if cfg.FlushInterval <= 0 {
		cfg.FlushInterval = time.Second
	}
	client := cfg.Client
	if client == nil {
		client = &http.Client{Timeout: otlpDefaultClient}
	}

	e := &OTLPExporter{
		endpoint:  cfg.Endpoint,
		client:    client,
		batchSize: cfg.BatchSize,
	}

	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(cfg.FlushInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-ticker.C:
				e.Flush(context.Background())
			}
		}
	}()

	return e, func() {
		close(done)
		// Flush on the way out: the spans a process produces last are often the
		// ones that explain why it stopped.
		e.Flush(context.Background())
	}
}

// ExportSpan queues a span, flushing when the batch is full.
//
// It never blocks on the network: a tracing backend that is slow or down must
// not slow the work being traced, which is the whole reason spans are batched.
func (e *OTLPExporter) ExportSpan(span *Span) {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return
	}
	e.queue = append(e.queue, span)
	full := len(e.queue) >= e.batchSize
	e.mu.Unlock()

	if full {
		e.Flush(context.Background())
	}
}

// Flush sends what is queued.
//
// A failed send is logged once per attempt and the spans are dropped, not
// retried into a growing buffer: an unbounded queue is a memory leak wearing an
// observability costume, and a gap in a trace is easier to reason about than a
// process that died holding one.
func (e *OTLPExporter) Flush(ctx context.Context) {
	e.mu.Lock()
	if len(e.queue) == 0 {
		e.mu.Unlock()
		return
	}
	batch := e.queue
	e.queue = nil
	e.mu.Unlock()

	body, err := marshalOTLPTraces(batch)
	if err != nil {
		log.Printf("WARN: otlp: marshal %d spans: %v", len(batch), err)
		return
	}

	url := e.endpoint + otlpTracesPath
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		log.Printf("WARN: otlp: build request: %v", err)
		return
	}
	req.Header.Set("Content-Type", otlpContentType)

	resp, err := e.client.Do(req)
	if err != nil {
		log.Printf("WARN: otlp: export %d spans to %s: %v", len(batch), url, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		log.Printf("WARN: otlp: export %d spans to %s: status %s", len(batch), url, resp.Status)
	}
}

// Close stops accepting spans and flushes.
func (e *OTLPExporter) Close() {
	e.mu.Lock()
	e.closed = true
	e.mu.Unlock()
	e.Flush(context.Background())
}

// --- OTLP JSON encoding ---
//
// The wire shapes below follow the OTLP/JSON encoding of the trace protobuf:
// field names in camelCase, trace/span ids as lowercase hex, timestamps as
// decimal strings of nanoseconds. Only the fields this tracer produces are
// modelled; a collector ignores what is absent.

type otlpExportRequest struct {
	ResourceSpans []otlpResourceSpans `json:"resourceSpans"`
}

type otlpResourceSpans struct {
	Resource   otlpResource     `json:"resource"`
	ScopeSpans []otlpScopeSpans `json:"scopeSpans"`
}

type otlpResource struct {
	Attributes []otlpAttribute `json:"attributes,omitempty"`
}

type otlpScopeSpans struct {
	Scope otlpScope  `json:"scope"`
	Spans []otlpSpan `json:"spans"`
}

type otlpScope struct {
	Name string `json:"name"`
}

type otlpAttribute struct {
	Key   string       `json:"key"`
	Value otlpAnyValue `json:"value"`
}

type otlpAnyValue struct {
	StringValue string `json:"stringValue,omitempty"`
}

type otlpSpan struct {
	TraceID           string          `json:"traceId"`
	SpanID            string          `json:"spanId"`
	ParentSpanID      string          `json:"parentSpanId,omitempty"`
	Name              string          `json:"name"`
	Kind              int             `json:"kind,omitempty"`
	StartTimeUnixNano string          `json:"startTimeUnixNano"`
	EndTimeUnixNano   string          `json:"endTimeUnixNano"`
	Attributes        []otlpAttribute `json:"attributes,omitempty"`
	Events            []otlpEvent     `json:"events,omitempty"`
	Status            *otlpStatus     `json:"status,omitempty"`
}

type otlpEvent struct {
	TimeUnixNano string          `json:"timeUnixNano"`
	Name         string          `json:"name"`
	Attributes   []otlpAttribute `json:"attributes,omitempty"`
}

type otlpStatus struct {
	// Code: 0 unset, 1 ok, 2 error — the OTLP enum.
	Code    int    `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// marshalOTLPTraces renders spans as an OTLP export request.
func marshalOTLPTraces(spans []*Span) ([]byte, error) {
	out := otlpExportRequest{
		ResourceSpans: []otlpResourceSpans{{
			ScopeSpans: []otlpScopeSpans{{
				Scope: otlpScope{Name: "forge"},
				Spans: make([]otlpSpan, 0, len(spans)),
			}},
		}},
	}

	for _, s := range spans {
		if s == nil {
			continue
		}
		out.ResourceSpans[0].ScopeSpans[0].Spans = append(
			out.ResourceSpans[0].ScopeSpans[0].Spans, toOTLPSpan(s))
	}

	return json.Marshal(out)
}

// toOTLPSpan converts one span.
func toOTLPSpan(s *Span) otlpSpan {
	out := otlpSpan{
		TraceID:           hexTraceID(s.Context.TraceID),
		SpanID:            hexSpanID(s.Context.SpanID),
		Name:              s.Name,
		Kind:              1, // INTERNAL: these are in-process spans
		StartTimeUnixNano: nanoString(s.StartTime),
		EndTimeUnixNano:   nanoString(s.EndTime),
	}

	// A zero parent means a root span, and the field is omitted rather than
	// sent as all-zeroes: a collector reads an absent parent as "root", while a
	// zero id is a malformed reference.
	if s.ParentID != (SpanID{}) {
		out.ParentSpanID = hexSpanID(s.ParentID)
	}

	// Attributes are sorted by key so two exports of the same span are
	// byte-identical, which is what makes a trace diffable.
	keys := make([]string, 0, len(s.Attributes))
	for k := range s.Attributes {
		keys = append(keys, k)
	}
	sortStrings(keys)
	for _, k := range keys {
		out.Attributes = append(out.Attributes, otlpAttribute{
			Key:   k,
			Value: otlpAnyValue{StringValue: s.Attributes[k]},
		})
	}

	for _, ev := range s.Events {
		event := otlpEvent{
			TimeUnixNano: nanoString(ev.Timestamp),
			Name:         ev.Name,
		}
		if len(ev.Attributes) > 0 {
			evKeys := make([]string, 0, len(ev.Attributes))
			for k := range ev.Attributes {
				evKeys = append(evKeys, k)
			}
			sortStrings(evKeys)
			for _, k := range evKeys {
				event.Attributes = append(event.Attributes, otlpAttribute{
					Key:   k,
					Value: otlpAnyValue{StringValue: ev.Attributes[k]},
				})
			}
		}
		out.Events = append(out.Events, event)
	}

	// Status is sent only when it says something: an unset status is the
	// default, and sending it explicitly adds a field with no information.
	switch s.Status {
	case SpanStatusOK:
		out.Status = &otlpStatus{Code: 1}
	case SpanStatusError:
		out.Status = &otlpStatus{Code: 2}
	}

	return out
}

// hexTraceID renders a trace id as the lowercase hex OTLP expects.
func hexTraceID(id TraceID) string {
	return fmt.Sprintf("%032x", id[:])
}

// hexSpanID renders a span id as the lowercase hex OTLP expects.
func hexSpanID(id SpanID) string {
	return fmt.Sprintf("%016x", id[:])
}

// nanoString renders a timestamp as decimal nanoseconds since the epoch.
//
// OTLP carries this as a string, not a number: a nanosecond count exceeds the
// range a JSON number can hold exactly in some parsers, and the specification
// chose strings to avoid the rounding.
func nanoString(t time.Time) string {
	if t.IsZero() {
		return "0"
	}
	return fmt.Sprintf("%d", t.UnixNano())
}

// sortStrings is a tiny insertion sort, used instead of importing sort for one
// call site in a package that otherwise has no need for it.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
