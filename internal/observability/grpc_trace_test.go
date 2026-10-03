package observability

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/timestamppb"

	forgev1 "github.com/castwell/forge/api/proto/gen"
)

// testWorker is a minimal WorkerService: unary ExecuteTask (errors on a
// marker task id) and a bidi Heartbeat that answers each ping with a pong.
type testWorker struct {
	forgev1.UnimplementedWorkerServiceServer
}

func (testWorker) ExecuteTask(_ context.Context, req *forgev1.TaskRequest) (*forgev1.TaskResponse, error) {
	if req.GetTaskId() == "boom" {
		return nil, errors.New("handler exploded")
	}
	return &forgev1.TaskResponse{}, nil
}

func (testWorker) Heartbeat(stream grpc.BidiStreamingServer[forgev1.HeartbeatPing, forgev1.HeartbeatPong]) error {
	for {
		ping, err := stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if err := stream.Send(&forgev1.HeartbeatPong{
			WorkerId:  "w-test",
			Timestamp: ping.GetTimestamp(),
		}); err != nil {
			return err
		}
	}
}

// startTracedServer wires a traced server and a traced client over bufconn
// and swaps the process-wide tracer for an in-memory exporter. The returned
// cleanup restores the previous tracer.
func startTracedServer(t *testing.T) (forgev1.WorkerServiceClient, *InMemoryExporter) {
	t.Helper()
	original := GlobalTracer()
	exporter := &InMemoryExporter{}
	SetGlobalTracer(NewTracer(TracerConfig{ServiceName: "test", Exporter: exporter, SampleRate: 1.0}))
	t.Cleanup(func() { SetGlobalTracer(original) })

	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer(ServerOptions()...)
	forgev1.RegisterWorkerServiceServer(srv, testWorker{})
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	dialOpts := append([]grpc.DialOption{
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	}, ClientDialOptions()...)
	conn, err := grpc.NewClient("passthrough:///bufnet", dialOpts...)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	return forgev1.NewWorkerServiceClient(conn), exporter
}

// snapshot copies the exported spans under the exporter lock, which also
// happens-before every write the exporters saw.
func snapshot(e *InMemoryExporter) []*Span {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]*Span, len(e.Spans))
	copy(out, e.Spans)
	return out
}

func findSpan(spans []*Span, kind string) (*Span, bool) {
	for _, s := range spans {
		if s.Attributes["rpc.kind"] == kind {
			return s, true
		}
	}
	return nil, false
}

// The whole point of the wiring: one RPC produces a client span and a server
// span, they share a trace id, and the server is parented to the client —
// proof that traceparent crossed the process border.
func TestUnarySpansLinkAcrossTheWire(t *testing.T) {
	client, exporter := startTracedServer(t)

	_, err := client.ExecuteTask(context.Background(), &forgev1.TaskRequest{TaskId: "t1"})
	require.NoError(t, err)

	spans := snapshot(exporter)
	clientSpan, ok := findSpan(spans, "client_unary")
	require.True(t, ok, "client interceptor must produce a span")
	serverSpan, ok := findSpan(spans, "server_unary")
	require.True(t, ok, "server interceptor must produce a span")

	assert.Equal(t, "/forge.v1.WorkerService/ExecuteTask", serverSpan.Name)
	assert.Equal(t, clientSpan.Context.TraceID, serverSpan.Context.TraceID,
		"trace id must survive the hop")
	assert.Equal(t, clientSpan.Context.SpanID, serverSpan.ParentID,
		"server span must be a child of the client span")
	assert.Equal(t, SpanStatusOK, serverSpan.Status)
}

// An erroring handler marks both sides of the call as failed spans.
func TestUnarySpansRecordErrors(t *testing.T) {
	client, exporter := startTracedServer(t)

	_, err := client.ExecuteTask(context.Background(), &forgev1.TaskRequest{TaskId: "boom"})
	require.Error(t, err)

	var serverErr, clientErr bool
	for _, s := range snapshot(exporter) {
		switch s.Attributes["rpc.kind"] {
		case "server_unary":
			serverErr = s.Status == SpanStatusError
		case "client_unary":
			clientErr = s.Status == SpanStatusError
		}
	}
	assert.True(t, serverErr, "server span must carry the handler error")
	assert.True(t, clientErr, "client span must carry the RPC error")
}

// Streams get spans too, and they link the same way. The client span ends on
// the terminal Recv (io.EOF = normal end, not an error).
func TestStreamSpansLinkAcrossTheWire(t *testing.T) {
	client, exporter := startTracedServer(t)

	stream, err := client.Heartbeat(context.Background())
	require.NoError(t, err)
	require.NoError(t, stream.Send(&forgev1.HeartbeatPing{Timestamp: timestamppbNow()}))
	pong, err := stream.Recv()
	require.NoError(t, err)
	assert.Equal(t, "w-test", pong.GetWorkerId())
	require.NoError(t, stream.CloseSend())
	_, err = stream.Recv()
	require.ErrorIs(t, err, io.EOF)

	// The server span ends when its handler returns; that happens before the
	// client observes EOF. Give the exporter a beat for safety.
	time.Sleep(10 * time.Millisecond)

	spans := snapshot(exporter)
	clientSpan, ok := findSpan(spans, "client_stream")
	require.True(t, ok, "client stream interceptor must produce a span")
	serverSpan, ok := findSpan(spans, "server_stream")
	require.True(t, ok, "server stream interceptor must produce a span")

	assert.Equal(t, clientSpan.Context.TraceID, serverSpan.Context.TraceID)
	assert.Equal(t, clientSpan.Context.SpanID, serverSpan.ParentID)
	assert.Equal(t, SpanStatusOK, serverSpan.Status, "a cleanly closed stream is not an error")
	assert.Equal(t, SpanStatusOK, clientSpan.Status, "io.EOF must not mark the span failed")
}

// The log exporter renders a finished span as one greppable line — this is
// the default, because a tracer that silently discards is the very
// half-wired state the wiring exists to end.
func TestLogExporterFormatsOneLine(t *testing.T) {
	var got string
	span := &Span{
		Name:      "/svc/Method",
		StartTime: time.Now().Add(-2 * time.Millisecond),
		EndTime:   time.Now(),
		Status:    SpanStatusError,
	}
	span.Context.TraceID = TraceID{0x01, 0x02}
	// Capture through the standard logger.
	// (The exporter writes via log.Printf; assert the formatting inputs.)
	got = formatSpanLine(span)
	assert.Contains(t, got, "/svc/Method")
	assert.Contains(t, got, "status=error")
	assert.Contains(t, got, "dur=")
}

func timestamppbNow() *timestamppb.Timestamp {
	return timestamppb.New(time.Now())
}
