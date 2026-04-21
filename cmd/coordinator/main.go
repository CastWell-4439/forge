// Package main is the entry point for the Forge Coordinator.
package main

import (
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	forgev1 "github.com/castwell/forge/api/proto/gen"
	"github.com/castwell/forge/internal/coordinator"
	"github.com/castwell/forge/internal/observability"
	"github.com/castwell/forge/internal/storage"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Println("INFO: forge-coordinator starting...")

	// --- Storage ---
	store, err := storage.NewBoltStorage(envOrDefault("FORGE_BOLT_PATH", "forge.db"))
	if err != nil {
		log.Fatalf("FATAL: open storage: %v", err)
	}
	defer store.Close()

	// --- Coordinator ---
	coord := coordinator.NewCoordinator(store)

	// --- Observability ---
	metrics := observability.NewMetrics()
	tracer := observability.NewTracer(observability.TracerConfig{
		ServiceName: "forge-coordinator",
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

	httpAddr := envOrDefault("FORGE_HTTP_ADDR", ":9090")
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

	// --- gRPC Server ---
	grpcAddr := envOrDefault("FORGE_GRPC_ADDR", ":50051")
	grpcLn, err := net.Listen("tcp", grpcAddr)
	if err != nil {
		log.Fatalf("FATAL: listen gRPC %s: %v", grpcAddr, err)
	}

	grpcServer := grpc.NewServer()
	forgev1.RegisterCoordinatorServiceServer(grpcServer, coord)
	reflection.Register(grpcServer) // enables grpcurl discovery

	go func() {
		log.Printf("INFO: gRPC server listening on %s", grpcAddr)
		if err := grpcServer.Serve(grpcLn); err != nil {
			log.Printf("ERROR: gRPC serve: %v", err)
		}
	}()

	// --- Graceful Shutdown ---
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	log.Printf("INFO: received %s, shutting down...", sig)
	grpcServer.GracefulStop()
	profiler.Stop()
	httpLn.Close()
	store.Close()
	log.Println("INFO: forge-coordinator stopped")
}

func envOrDefault(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}
