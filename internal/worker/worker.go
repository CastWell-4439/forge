package worker

import (
	"context"
	"fmt"
	"net"
	"sync/atomic"

	forgev1 "github.com/castwell/forge/api/proto/gen"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Worker connects to a Coordinator, registers itself, and serves task execution requests.
type Worker struct {
	forgev1.UnimplementedWorkerServiceServer

	id       string
	addr     string
	coordAddr string
	capacity int
	registry *Registry
	executor *Executor
	active   atomic.Int32
	server   *grpc.Server
}

// NewWorker creates a new Worker with the given configuration.
func NewWorker(id, addr, coordAddr string, capacity int, registry *Registry) *Worker {
	return &Worker{
		id:        id,
		addr:      addr,
		coordAddr: coordAddr,
		capacity:  capacity,
		registry:  registry,
		executor:  NewExecutor(registry),
	}
}

// Start registers the worker with the coordinator and starts serving gRPC requests.
func (w *Worker) Start(ctx context.Context) error {
	// Register with coordinator
	if err := w.registerWithCoordinator(ctx); err != nil {
		return fmt.Errorf("register with coordinator: %w", err)
	}

	// Start gRPC server for receiving task execution requests
	lis, err := net.Listen("tcp", w.addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", w.addr, err)
	}

	w.server = grpc.NewServer()
	forgev1.RegisterWorkerServiceServer(w.server, w)

	go func() {
		<-ctx.Done()
		w.server.GracefulStop()
	}()

	return w.server.Serve(lis)
}

// Stop gracefully stops the worker's gRPC server.
func (w *Worker) Stop() {
	if w.server != nil {
		w.server.GracefulStop()
	}
}

// registerWithCoordinator connects to the coordinator and sends a Register RPC.
func (w *Worker) registerWithCoordinator(ctx context.Context) error {
	conn, err := grpc.NewClient(w.coordAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("connect to coordinator %s: %w", w.coordAddr, err)
	}
	defer conn.Close()

	client := forgev1.NewWorkerServiceClient(conn)
	_, err = client.Register(ctx, &forgev1.RegisterRequest{
		Registration: &forgev1.WorkerRegistration{
			Id:       w.id,
			Addr:     w.addr,
			Handlers: w.registry.Handlers(),
			Capacity: int32(w.capacity),
		},
	})
	return err
}

// Register implements the WorkerService Register RPC (no-op for now, coordinator calls this on itself).
func (w *Worker) Register(_ context.Context, _ *forgev1.RegisterRequest) (*forgev1.RegisterResponse, error) {
	return &forgev1.RegisterResponse{Accepted: true}, nil
}

// Heartbeat implements the WorkerService Heartbeat RPC (placeholder for Phase 2).
func (w *Worker) Heartbeat(stream grpc.BidiStreamingServer[forgev1.HeartbeatPing, forgev1.HeartbeatPong]) error {
	for {
		_, err := stream.Recv()
		if err != nil {
			return err
		}
		// TODO Phase 2: respond with status
	}
}

// ExecuteTask implements the WorkerService ExecuteTask RPC.
// It receives a task from the coordinator, runs the matching handler, and returns the result.
func (w *Worker) ExecuteTask(ctx context.Context, req *forgev1.TaskRequest) (*forgev1.TaskResponse, error) {
	w.active.Add(1)
	defer w.active.Add(-1)

	return w.executor.Execute(ctx, req), nil
}

// ID returns the worker's unique identifier.
func (w *Worker) ID() string {
	return w.id
}

// Addr returns the worker's gRPC listen address.
func (w *Worker) Addr() string {
	return w.addr
}
