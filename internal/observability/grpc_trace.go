package observability

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// --- Process-wide tracer handle ---
//
// Instrumentation points (gRPC interceptors at dial and serve sites) live in
// packages that should not have a Tracer threaded through every constructor,
// so they read one process-wide handle — the same shape OpenTelemetry uses
// for its global provider. Binaries install the real tracer at startup via
// TracerConfigEnv; anything else that runs first sees a no-op tracer.

var (
	globalMu     sync.RWMutex
	globalTracer *Tracer
)

// SetGlobalTracer installs the tracer that interceptors and instrumentation
// points will use. Passing nil resets to the no-op default.
func SetGlobalTracer(t *Tracer) {
	globalMu.Lock()
	globalTracer = t
	globalMu.Unlock()
}

// GlobalTracer returns the installed tracer, or a no-op default when none is
// set. It never returns nil. (Named GlobalTracer, not Tracer: that name is
// the span-producing type.)
func GlobalTracer() *Tracer {
	globalMu.Lock()
	defer globalMu.Unlock()
	if globalTracer == nil {
		globalTracer = NewTracer(TracerConfig{ServiceName: "forge"})
	}
	return globalTracer
}

// TracerConfigEnv reads the tracer configuration from the environment and
// installs it as the process-wide tracer:
//
//	FORGE_TRACE_EXPORTER = log (default) | noop | otlp
//	FORGE_TRACE_SAMPLE   = 0.0..1.0 (default 1.0)
//	FORGE_OTLP_ENDPOINT  = collector base URL for the otlp exporter
//	                       (default http://localhost:4318 — the OTLP/HTTP port)
//
// The default is the log exporter so tracing is observable out of the box —
// a tracer that is installed but silently discards everything would be the
// same half-wired state this wiring exists to end.
//
// Selecting "otlp" without an endpoint uses the standard collector port rather
// than refusing: the deployment that asks for OTLP has almost certainly put a
// collector somewhere conventional, and a warning names the default it chose.
func TracerConfigEnv(serviceName string) *Tracer {
	exporterName := strings.ToLower(strings.TrimSpace(os.Getenv("FORGE_TRACE_EXPORTER")))
	var exporter SpanExporter
	switch exporterName {
	case "", "log":
		exporter = LogExporter{}
	case "noop", "off", "none":
		exporter = &NoopExporter{}
	case "otlp":
		endpoint := strings.TrimSpace(os.Getenv("FORGE_OTLP_ENDPOINT"))
		if endpoint == "" {
			endpoint = defaultOTLPEndpoint
			log.Printf("INFO: otlp exporter selected; using the default collector endpoint %s "+
				"(set FORGE_OTLP_ENDPOINT to change it)", endpoint)
		}
		built, stop := NewOTLPExporter(context.Background(), OTLPConfig{Endpoint: endpoint})
		otlpStop = stop
		exporter = built
	default:
		log.Printf("WARN: unknown FORGE_TRACE_EXPORTER %q, spans will be discarded", exporterName)
		exporter = &NoopExporter{}
	}

	rate := 1.0
	if raw := strings.TrimSpace(os.Getenv("FORGE_TRACE_SAMPLE")); raw != "" {
		if parsed, err := strconv.ParseFloat(raw, 64); err == nil && parsed >= 0 && parsed <= 1 {
			rate = parsed
		} else {
			log.Printf("WARN: invalid FORGE_TRACE_SAMPLE %q, using 1.0", raw)
		}
	}

	t := NewTracer(TracerConfig{
		ServiceName: serviceName,
		Exporter:    exporter,
		SampleRate:  rate,
	})
	SetGlobalTracer(t)
	return t
}

// defaultOTLPEndpoint is the OTLP/HTTP port a collector conventionally listens
// on. 4317 is the gRPC port, which this exporter does not speak.
const defaultOTLPEndpoint = "http://localhost:4318"

// otlpStop, when set, flushes the OTLP exporter's queue.
//
// It is package state because TracerConfigEnv is called for its side effect
// (installing the global tracer) and has no return channel for a stop function.
// A process that wants its last spans sent calls StopTracing; one that does not
// still gets them, because the exporter flushes on its own interval.
var otlpStop func()

// StopTracing flushes any queued spans. It is safe to call when tracing was
// never started, or when the exporter is not OTLP.
func StopTracing() {
	if otlpStop != nil {
		otlpStop()
		otlpStop = nil
	}
}

// LogExporter writes one line per finished span to the standard logger.
// It is the default exporter: cheap, greppable, and honest — a finished span
// either shows up or the log says nothing was traced.
type LogExporter struct{}

// ExportSpan implements SpanExporter.
func (LogExporter) ExportSpan(span *Span) {
	log.Print(formatSpanLine(span))
}

// formatSpanLine renders one span as a single log line.
func formatSpanLine(span *Span) string {
	status := "ok"
	if span.Status == SpanStatusError {
		status = "error"
	}
	dur := time.Duration(0)
	if !span.EndTime.IsZero() && !span.StartTime.IsZero() {
		dur = span.EndTime.Sub(span.StartTime)
	}
	return fmt.Sprintf("[trace] %s trace=%032x dur=%s status=%s",
		span.Name, span.Context.TraceID, dur.Round(time.Millisecond), status)
}

// --- gRPC instrumentation ---

const traceparentKey = "traceparent"

// withRemoteParent attaches an extracted remote span context as the parent of
// any span started from this context, so the trace crosses process borders.
func withRemoteParent(ctx context.Context, sc *SpanContext) context.Context {
	parent := &Span{Context: *sc, Name: "remote"}
	return context.WithValue(ctx, traceKey{}, parent)
}

// incomingTraceContext extracts traceparent from gRPC incoming metadata.
func incomingTraceContext(ctx context.Context) (*SpanContext, bool) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil, false
	}
	values := md.Get(traceparentKey)
	if len(values) == 0 {
		return nil, false
	}
	return ExtractTraceContext(map[string]string{traceparentKey: values[0]})
}

// UnaryServerInterceptor starts a server span per unary RPC, parenting it to
// the caller's trace context when one was propagated.
func UnaryServerInterceptor() grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		if sc, ok := incomingTraceContext(ctx); ok {
			ctx = withRemoteParent(ctx, sc)
		}
		t := GlobalTracer()
		ctx, span := t.StartSpan(ctx, info.FullMethod)
		span.SetAttribute("rpc.system", "grpc")
		span.SetAttribute("rpc.kind", "server_unary")
		resp, err := handler(ctx, req)
		if err != nil {
			span.SetStatus(SpanStatusError, err.Error())
		} else {
			span.SetStatus(SpanStatusOK, "")
		}
		t.EndSpan(span)
		return resp, err
	}
}

// tracedServerStream carries the server span's context into the handler.
type tracedServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *tracedServerStream) Context() context.Context { return s.ctx }

// StreamServerInterceptor starts a server span per streaming RPC. The span
// ends when the stream closes, so its duration is the stream's lifetime.
func StreamServerInterceptor() grpc.StreamServerInterceptor {
	return func(
		srv any,
		ss grpc.ServerStream,
		info *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) error {
		ctx := ss.Context()
		if sc, ok := incomingTraceContext(ctx); ok {
			ctx = withRemoteParent(ctx, sc)
		}
		t := GlobalTracer()
		ctx, span := t.StartSpan(ctx, info.FullMethod)
		span.SetAttribute("rpc.system", "grpc")
		span.SetAttribute("rpc.kind", "server_stream")
		err := handler(srv, &tracedServerStream{ServerStream: ss, ctx: ctx})
		if err != nil {
			span.SetStatus(SpanStatusError, err.Error())
		} else {
			span.SetStatus(SpanStatusOK, "")
		}
		t.EndSpan(span)
		return err
	}
}

// UnaryClientInterceptor starts a client span per unary call and injects
// traceparent into the outgoing metadata so the server can parent its span.
func UnaryClientInterceptor() grpc.UnaryClientInterceptor {
	return func(
		ctx context.Context,
		method string,
		req, reply any,
		cc *grpc.ClientConn,
		invoker grpc.UnaryInvoker,
		opts ...grpc.CallOption,
	) error {
		t := GlobalTracer()
		ctx, span := t.StartSpan(ctx, method)
		span.SetAttribute("rpc.system", "grpc")
		span.SetAttribute("rpc.kind", "client_unary")
		ctx = metadata.AppendToOutgoingContext(ctx, traceparentKey, span.Context.String())
		err := invoker(ctx, method, req, reply, cc, opts...)
		if err != nil {
			span.SetStatus(SpanStatusError, err.Error())
		} else {
			span.SetStatus(SpanStatusOK, "")
		}
		t.EndSpan(span)
		return err
	}
}

// StreamClientInterceptor starts a client span per streaming call and
// injects traceparent for the server to pick up.
func StreamClientInterceptor() grpc.StreamClientInterceptor {
	return func(
		ctx context.Context,
		desc *grpc.StreamDesc,
		cc *grpc.ClientConn,
		method string,
		streamer grpc.Streamer,
		opts ...grpc.CallOption,
	) (grpc.ClientStream, error) {
		t := GlobalTracer()
		ctx, span := t.StartSpan(ctx, method)
		span.SetAttribute("rpc.system", "grpc")
		span.SetAttribute("rpc.kind", "client_stream")
		ctx = metadata.AppendToOutgoingContext(ctx, traceparentKey, span.Context.String())
		cs, err := streamer(ctx, desc, cc, method, opts...)
		if err != nil {
			span.SetStatus(SpanStatusError, err.Error())
			t.EndSpan(span)
			return nil, err
		}
		// The span ends when the caller closes the stream; wrapping keeps the
		// span handle without changing what the caller sees.
		return &tracedClientStreamWithEnd{ClientStream: cs, span: span, tracer: t}, nil
	}
}

// tracedClientStreamWithEnd ends the client span when the stream terminates.
type tracedClientStreamWithEnd struct {
	grpc.ClientStream
	span   *Span
	tracer *Tracer
	once   sync.Once
}

// RecvMsg ends the span when the stream reaches a terminal state: io.EOF is
// the normal end of a server stream (span finishes OK), anything else marks
// the span failed. A caller that never drains the stream keeps its span —
// the duration then honestly reports the stream the caller abandoned.
func (s *tracedClientStreamWithEnd) RecvMsg(m any) error {
	err := s.ClientStream.RecvMsg(m)
	if err != nil {
		if errors.Is(err, io.EOF) {
			s.span.SetStatus(SpanStatusOK, "")
		} else {
			s.span.SetStatus(SpanStatusError, err.Error())
		}
		s.once.Do(func() { s.tracer.EndSpan(s.span) })
	}
	return err
}

// ClientDialOptions returns the standard client-side tracing options so every
// dial site wires identically.
func ClientDialOptions() []grpc.DialOption {
	return []grpc.DialOption{
		grpc.WithChainUnaryInterceptor(UnaryClientInterceptor()),
		grpc.WithChainStreamInterceptor(StreamClientInterceptor()),
	}
}

// ServerOptions returns the standard server-side tracing options.
func ServerOptions() []grpc.ServerOption {
	return []grpc.ServerOption{
		grpc.ChainUnaryInterceptor(UnaryServerInterceptor()),
		grpc.ChainStreamInterceptor(StreamServerInterceptor()),
	}
}
