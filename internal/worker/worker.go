package worker

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync/atomic"

	forgev1 "github.com/castwell/forge/api/proto/gen"
	"github.com/castwell/forge/internal/discovery"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Worker connects to a Coordinator, registers itself, and serves task execution requests.
type Worker struct {
	forgev1.UnimplementedWorkerServiceServer

	id        string
	addr      string
	coordAddr string
	capacity  int
	registry  *Registry
	executor  *Executor
	active    atomic.Int32
	server    *grpc.Server
	disco     discovery.Discovery
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

// SetDiscovery sets the Discovery backend so the worker registers itself in etcd
// for discovery by the coordinator's WorkerManager.
func (w *Worker) SetDiscovery(d discovery.Discovery) {
	w.disco = d
}

// Start registers the worker with the coordinator and starts serving gRPC requests.
func (w *Worker) Start(ctx context.Context) error {
	// Register with coordinator via direct gRPC (legacy/fallback).
	if err := w.registerWithCoordinator(ctx); err != nil {
		return fmt.Errorf("register with coordinator: %w", err)
	}

	// If discovery is configured, also register in etcd so WorkerManager can discover us.
	if w.disco != nil {
		if err := w.registerWithDiscovery(ctx); err != nil {
			return fmt.Errorf("register with discovery: %w", err)
		}
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

// registerWithDiscovery registers this worker in etcd under forge/workers/{id}
// so the coordinator's WorkerManager can discover it via Watch.
func (w *Worker) registerWithDiscovery(ctx context.Context) error {
	node := discovery.NodeInfo{
		ID:   "forge/workers/" + w.id,
		Addr: w.addr,
		Metadata: map[string]string{
			"handlers": strings.Join(w.registry.Handlers(), ","),
			"capacity": fmt.Sprintf("%d", w.capacity),
		},
	}
	return w.disco.Register(ctx, node)
}
