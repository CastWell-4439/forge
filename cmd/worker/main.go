// Package main is the entry point for the Forge Worker.
package main

import (
	"context"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/castwell/forge/internal/observability"
	"github.com/castwell/forge/internal/worker"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	lang := envOrDefault("FORGE_WORKER_LANG", "go")
	log.Printf("INFO: forge-worker (%s) starting...", lang)

	// --- Observability ---
	metrics := observability.NewMetrics()
	// Installed process-wide: the worker's gRPC server and its dial to the
	// coordinator read it through the tracing interceptors.
	observability.TracerConfigEnv("forge-worker-" + lang)
	profiler := observability.NewProfiler(observability.DefaultProfilingConfig())
	profiler.Start()

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

	// --- Handler registry ---
	// The registry must never be nil: Worker.ExecuteTask dereferences it on every
	// dispatched task, so a nil registry panicked the worker on its first task.
	registry := worker.NewRegistry()
	registerBuiltinHandlers(registry)
	log.Printf("INFO: registered handlers: %v", registry.Handlers())

	// --- Worker gRPC Server (registers with the Coordinator, then serves ExecuteTask) ---
	workerID := envOrDefault("FORGE_WORKER_ID", "worker-1")
	grpcAddr := envOrDefault("FORGE_GRPC_ADDR", ":50052")
	coordAddr := envOrDefault("FORGE_COORDINATOR_ADDR", "localhost:50051")
	capacity := 10

	w := worker.NewWorker(workerID, grpcAddr, coordAddr, capacity, registry)

	// --- ForgeX runtime gate ---
	// Evaluates every task against the tool contracts and the current authority
	// level before its handler runs. Shadow by default; enforcement is opt-in.
	installRuntimeGate(w)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	startErr := make(chan error, 1)
	go func() { startErr <- w.Start(ctx) }()

	log.Printf("INFO: forge-worker (%s) ready, coordinator=%s", lang, coordAddr)

	// --- Graceful Shutdown ---
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigCh:
		log.Printf("INFO: received %s, shutting down...", sig)
	case err := <-startErr:
		if err != nil {
			log.Printf("ERROR: worker stopped: %v", err)
		} else {
			log.Printf("INFO: worker stopped")
		}
	}

	cancel()
	w.Stop()
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
