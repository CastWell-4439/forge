// Package main is the entry point for the Forge Worker.
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

	_ = tracer // used when worker logic is wired in

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

	// --- gRPC Client (placeholder) ---
	coordAddr := envOrDefault("FORGE_COORDINATOR_ADDR", "localhost:50051")
	log.Printf("INFO: coordinator endpoint configured at %s (not yet wired)", coordAddr)

	// --- Graceful Shutdown ---
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	log.Printf("INFO: received %s, shutting down...", sig)
	profiler.Stop()
	httpLn.Close()
	log.Printf("INFO: forge-worker (%s) stopped", lang)
}

func envOrDefault(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}
