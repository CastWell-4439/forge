// Package main is the entry point for the Forge Coordinator.
package main

import (
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/castwell/forge/internal/observability"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Println("INFO: forge-coordinator starting...")

	// --- Observability ---
	metrics := observability.NewMetrics()
	tracer := observability.NewTracer(observability.TracerConfig{
		ServiceName: "forge-coordinator",
		SampleRate:  1.0,
	})
	profiler := observability.NewProfiler(observability.DefaultProfilingConfig())
	profiler.Start()

	_ = tracer // used when coordinator logic is wired in

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
		log.Fatalf("FATAL: listen %s: %v", httpAddr, err)
	}
	go func() {
		log.Printf("INFO: HTTP server listening on %s (/metrics, /debug/profile, /healthz)", httpAddr)
		if err := http.Serve(httpLn, mux); err != nil {
			log.Printf("ERROR: http serve: %v", err)
		}
	}()

	// --- gRPC Server (placeholder) ---
	grpcAddr := envOrDefault("FORGE_GRPC_ADDR", ":50051")
	log.Printf("INFO: gRPC endpoint configured at %s (not yet wired)", grpcAddr)

	// --- Graceful Shutdown ---
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	log.Printf("INFO: received %s, shutting down...", sig)
	profiler.Stop()
	httpLn.Close()
	log.Println("INFO: forge-coordinator stopped")
}

func envOrDefault(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}
