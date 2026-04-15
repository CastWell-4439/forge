package worker

import (
	"context"
	"net"
	"testing"
	"time"

	forgev1 "github.com/castwell/forge/api/proto/gen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestHeartbeat_PingPong(t *testing.T) {
	// Create a worker and start its gRPC server.
	registry := NewRegistry()
	w := NewWorker("test-worker-1", "127.0.0.1:0", "", 5, registry)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	srv := grpc.NewServer()
	forgev1.RegisterWorkerServiceServer(srv, w)
	go srv.Serve(lis)
	defer srv.GracefulStop()

	// Connect a client (simulating the coordinator).
	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	defer conn.Close()

	client := forgev1.NewWorkerServiceClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := client.Heartbeat(ctx)
	require.NoError(t, err)

	// Send a ping.
	require.NoError(t, stream.Send(&forgev1.HeartbeatPing{Timestamp: timestamppb.Now()}))

	// Receive a pong.
	pong, err := stream.Recv()
	require.NoError(t, err)
	assert.Equal(t, "test-worker-1", pong.GetWorkerId())
	assert.Equal(t, int32(0), pong.GetActiveTasks())
	assert.Equal(t, int32(5), pong.GetCapacity())

	// Send another ping.
	require.NoError(t, stream.Send(&forgev1.HeartbeatPing{Timestamp: timestamppb.Now()}))
	pong2, err := stream.Recv()
	require.NoError(t, err)
	assert.Equal(t, "test-worker-1", pong2.GetWorkerId())
}

func TestHeartbeat_WorkerReportsActiveTasks(t *testing.T) {
	registry := NewRegistry()
	w := NewWorker("test-worker-2", "127.0.0.1:0", "", 10, registry)

	// Simulate active tasks.
	w.active.Store(3)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	srv := grpc.NewServer()
	forgev1.RegisterWorkerServiceServer(srv, w)
	go srv.Serve(lis)
	defer srv.GracefulStop()

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	defer conn.Close()

	client := forgev1.NewWorkerServiceClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := client.Heartbeat(ctx)
	require.NoError(t, err)

	require.NoError(t, stream.Send(&forgev1.HeartbeatPing{Timestamp: timestamppb.Now()}))

	pong, err := stream.Recv()
	require.NoError(t, err)
	assert.Equal(t, int32(3), pong.GetActiveTasks())
	assert.Equal(t, int32(10), pong.GetCapacity())
}
