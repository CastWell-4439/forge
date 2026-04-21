// Package main is the entry point for the Forge Worker.
package main

import (
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	forgev1 "github.com/castwell/forge/api/proto/gen"
	"github.com/castwell/forge/internal/observability"
	"github.com/castwell/forge/internal/worker"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/reflection"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	lang := envOrDefault("FORGE_WORKER_LANG", "go")
	log.Printf("INFO: forge-worker (%s) starting...", lang)

	// --- Observability ---
	metrics := observability.NewMetrics()
	tracer := observability.NewTracer(observability.TracerConfig{
		ServiceName: "forge-worker-" + lang,
		SampleRate:  1.0,
	})
	profiler := observability.NewProfiler(observability.DefaultProfilingConfig())
	profiler.Start()

	_ = tracer // attach to gRPC interceptors in future

	// --- HTTP Server (metrics + profiling + health) ---
	mux := http.NewServeMux()
	mux.Handle("/metrics", metrics.Handler())
	mux.Handle("/debug/profile", profiler.DebugHandler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	httpAddr := envOrDefault("FORGE_HTTP_ADDR", ":9091")
	httpLn, err := net.Listen("tcp", httpAddr)
	if err != nil {
		log.Fatalf("FATAL: listen HTTP %s: %v", httpAddr, err)
	}
	go func() {
		log.Printf("INFO: HTTP server listening on %s (/metrics, /debug/profile, /healthz)", httpAddr)
		if err := http.Serve(httpLn, mux); err != nil {
			log.Printf("ERROR: http serve: %v", err)
		}
	}()

	// --- gRPC Client → Coordinator (registration + heartbeat) ---
	coordAddr := envOrDefault("FORGE_COORDINATOR_ADDR", "localhost:50051")
	coordConn, err := grpc.NewClient(coordAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("FATAL: connect coordinator %s: %v", coordAddr, err)
	}
	defer coordConn.Close()
	coordClient := forgev1.NewCoordinatorServiceClient(coordConn)
	_ = coordClient // used for registration + heartbeat

	// --- Worker gRPC Server (receives ExecuteTask RPCs) ---
	workerID := envOrDefault("FORGE_WORKER_ID", "worker-1")
	grpcAddr := envOrDefault("FORGE_GRPC_ADDR", ":50052")
	capacity := 10

	w := worker.NewWorker(workerID, grpcAddr, coordAddr, capacity, nil)
	grpcLn, err := net.Listen("tcp", grpcAddr)
	if err != nil {
		log.Fatalf("FATAL: listen gRPC %s: %v", grpcAddr, err)
	}

	grpcServer := grpc.NewServer()
	forgev1.RegisterWorkerServiceServer(grpcServer, w)
	reflection.Register(grpcServer)

	go func() {
		log.Printf("INFO: gRPC server listening on %s (WorkerService)", grpcAddr)
		if err := grpcServer.Serve(grpcLn); err != nil {
			log.Printf("ERROR: gRPC serve: %v", err)
		}
	}()

	log.Printf("INFO: forge-worker (%s) ready, coordinator=%s", lang, coordAddr)

	// --- Graceful Shutdown ---
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	log.Printf("INFO: received %s, shutting down...", sig)
	grpcServer.GracefulStop()
	profiler.Stop()
	httpLn.Close()
	coordConn.Close()
	log.Printf("INFO: forge-worker (%s) stopped", lang)
}

func envOrDefault(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}
