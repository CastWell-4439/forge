package observability

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"time"
)

// --- Tracing (OTel-compatible interface, self-contained implementation) ---

// TraceID is a 128-bit trace identifier.
type TraceID [16]byte

// SpanID is a 64-bit span identifier.
type SpanID [8]byte

// SpanContext carries trace context across boundaries.
type SpanContext struct {
	TraceID TraceID
	SpanID  SpanID
	Sampled bool
}

// String returns the W3C traceparent format.
func (sc SpanContext) String() string {
	return fmt.Sprintf("00-%032x-%016x-%02d", sc.TraceID, sc.SpanID, boolToInt(sc.Sampled))
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Span represents a unit of work in a trace.
type Span struct {
	Name       string
	Context    SpanContext
	ParentID   SpanID
	StartTime  time.Time
	EndTime    time.Time
	Status     SpanStatus
	Attributes map[string]string
	Events     []SpanEvent
	mu         sync.Mutex
}

// SpanStatus represents the span completion status.
type SpanStatus int

const (
	SpanStatusUnset SpanStatus = iota
	SpanStatusOK
	SpanStatusError
)

// SpanEvent is a timestamped annotation on a span.
type SpanEvent struct {
	Name       string
	Timestamp  time.Time
	Attributes map[string]string
}

// SetAttribute adds a key-value attribute to the span.
func (s *Span) SetAttribute(key, value string) {
	s.mu.Lock()
	if s.Attributes == nil {
		s.Attributes = make(map[string]string)
	}
	s.Attributes[key] = value
	s.mu.Unlock()
}

// AddEvent adds a timestamped event to the span.
func (s *Span) AddEvent(name string, attrs map[string]string) {
	s.mu.Lock()
	s.Events = append(s.Events, SpanEvent{
		Name:       name,
		Timestamp:  time.Now(),
		Attributes: attrs,
	})
	s.mu.Unlock()
}

// SetStatus sets the span status.
func (s *Span) SetStatus(status SpanStatus, message string) {
	s.mu.Lock()
	s.Status = status
	if message != "" {
		if s.Attributes == nil {
			s.Attributes = make(map[string]string)
		}
		s.Attributes["status.message"] = message
	}
	s.mu.Unlock()
}

// End marks the span as finished and exports it.
func (s *Span) End() {
	s.mu.Lock()
	s.EndTime = time.Now()
	s.mu.Unlock()
}

// Tracer creates and manages spans.
type Tracer struct {
	serviceName string
	exporter    SpanExporter
	sampler     float64 // 0.0 to 1.0
	mu          sync.Mutex
}

// SpanExporter receives completed spans.
type SpanExporter interface {
	ExportSpan(span *Span)
}

// TracerConfig configures the tracer.
type TracerConfig struct {
	ServiceName string
	Exporter    SpanExporter
	SampleRate  float64 // 0.0 = none, 1.0 = all
}

// NewTracer creates a new Tracer.
func NewTracer(config TracerConfig) *Tracer {
	if config.SampleRate <= 0 {
		config.SampleRate = 1.0
	}
	if config.Exporter == nil {
		config.Exporter = &NoopExporter{}
	}
	return &Tracer{
		serviceName: config.ServiceName,
		exporter:    config.Exporter,
		sampler:     config.SampleRate,
	}
}

// traceKey is the context key for span context.
type traceKey struct{}

// StartSpan creates a new span. If ctx has a parent span, links them.
func (t *Tracer) StartSpan(ctx context.Context, name string) (context.Context, *Span) {
	var parentID SpanID
	var traceID TraceID
	sampled := rand.Float64() < t.sampler

	if parent, ok := SpanFromContext(ctx); ok {
		parentID = parent.Context.SpanID
		traceID = parent.Context.TraceID
		sampled = parent.Context.Sampled // inherit sampling decision
	} else {
		traceID = generateTraceID()
	}

	span := &Span{
		Name:      name,
		StartTime: time.Now(),
		ParentID:  parentID,
		Context: SpanContext{
			TraceID: traceID,
			SpanID:  generateSpanID(),
			Sampled: sampled,
		},
	}

	span.SetAttribute("service.name", t.serviceName)

	newCtx := context.WithValue(ctx, traceKey{}, span)
	return newCtx, span
}

// EndSpan finishes a span and exports it.
func (t *Tracer) EndSpan(span *Span) {
	span.End()
	if span.Context.Sampled {
		t.exporter.ExportSpan(span)
	}
}

// SpanFromContext extracts the current span from context.
func SpanFromContext(ctx context.Context) (*Span, bool) {
	span, ok := ctx.Value(traceKey{}).(*Span)
	return span, ok
}

// InjectTraceContext returns trace context headers for propagation.
func InjectTraceContext(ctx context.Context) map[string]string {
	span, ok := SpanFromContext(ctx)
	if !ok {
		return nil
	}
	return map[string]string{
		"traceparent": span.Context.String(),
	}
}

// ExtractTraceContext creates a span context from propagation headers.
func ExtractTraceContext(headers map[string]string) (*SpanContext, bool) {
	tp, ok := headers["traceparent"]
	if !ok || len(tp) < 55 {
		return nil, false
	}
	// Format: 00-{32 hex}-{16 hex}-{2 hex}
	parts := strings.Split(tp, "-")
	if len(parts) != 4 || parts[0] != "00" {
		return nil, false
	}

	var sc SpanContext
	traceBytes, err := hexDecode(parts[1])
	if err != nil || len(traceBytes) != 16 {
		return nil, false
	}
	copy(sc.TraceID[:], traceBytes)

	spanBytes, err := hexDecode(parts[2])
	if err != nil || len(spanBytes) != 8 {
		return nil, false
	}
	copy(sc.SpanID[:], spanBytes)

	sc.Sampled = parts[3] == "01"
	return &sc, true
}

// hexDecode decodes a hex string into bytes.
func hexDecode(s string) ([]byte, error) {
	if len(s)%2 != 0 {
		return nil, fmt.Errorf("odd length hex string")
	}
	b := make([]byte, len(s)/2)
	for i := 0; i < len(s); i += 2 {
		_, err := fmt.Sscanf(s[i:i+2], "%02x", &b[i/2])
		if err != nil {
			return nil, err
		}
	}
	return b, nil
}

// --- ID Generation ---

func generateTraceID() TraceID {
	var id TraceID
	for i := range id {
		id[i] = byte(rand.Intn(256))
	}
	return id
}

func generateSpanID() SpanID {
	var id SpanID
	for i := range id {
		id[i] = byte(rand.Intn(256))
	}
	return id
}

// --- Exporters ---

// NoopExporter discards spans.
type NoopExporter struct{}

func (n *NoopExporter) ExportSpan(_ *Span) {}

// InMemoryExporter collects spans in memory (for testing).
type InMemoryExporter struct {
	Spans []*Span
	mu    sync.Mutex
}

func (e *InMemoryExporter) ExportSpan(span *Span) {
	e.mu.Lock()
	e.Spans = append(e.Spans, span)
	e.mu.Unlock()
}

func (e *InMemoryExporter) Count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.Spans)
}
