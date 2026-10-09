// Package worker assembles the Forge Worker's serving logic — metrics,
// handler registry, runtime gate, etcd discovery and the gRPC worker — as a
// ctx-driven Run function. It is shared by every entry point: the dedicated
// cmd/worker binary, and the forge CLI's `worker` and `standalone`
// subcommands. Signal handling stays in the entry points.
package worker

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"

	"github.com/castwell/forge/internal/observability"
	"github.com/castwell/forge/internal/worker"
)

// Run assembles and serves the worker until appCtx is cancelled or the
// worker stops on its own. Setup failures and a failed Start are returned
// (the entry point decides how to report them); per-server runtime errors
// stay logged, exactly as before. A Start failure previously exited 0 — it
// now propagates so supervisors see a non-zero exit.
func Run(appCtx context.Context) error {
	lang := envOrDefault("FORGE_WORKER_LANG", "go")
	log.Printf("INFO: forge-worker (%s) starting...", lang)

	// --- Observability ---
	metrics := observability.NewMetrics()
	// Installed process-wide: the worker's gRPC server and its dial to the
	// coordinator read it through the tracing interceptors.
	observability.TracerConfigEnv("forge-worker-" + lang)
	// Same as the coordinator: kernel TCP latency where available and enabled,
	// a no-op otherwise.
	stopEBPF := observability.StartEBPFObserver(appCtx, metrics)
	defer stopEBPF()
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
		return fmt.Errorf("listen HTTP %s: %w", httpAddr, err)
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

	// --- etcd discovery (optional) ---
	// Registers the worker in etcd on Start so a distributed coordinator can
	// discover it. Unset keeps direct registration only.
	if d, derr := etcdDiscoveryFromEnv(workerID); derr != nil {
		return fmt.Errorf("etcd discovery: %w", derr)
	} else if d != nil {
		w.SetDiscovery(d)
		log.Printf("INFO: etcd discovery enabled (endpoints=%s)", os.Getenv(envEtcdEndpoints))
	}

	startErr := make(chan error, 1)
	go func() { startErr <- w.Start(appCtx) }()

	log.Printf("INFO: forge-worker (%s) ready, coordinator=%s", lang, coordAddr)

	// --- Graceful Shutdown: entry point cancels appCtx, or Start returns ---
	var runErr error
	select {
	case <-appCtx.Done():
		log.Printf("INFO: shutting down...")
	case err := <-startErr:
		if err != nil {
			log.Printf("ERROR: worker stopped: %v", err)
			runErr = fmt.Errorf("worker stopped: %w", err)
		} else {
			log.Printf("INFO: worker stopped")
		}
	}

	w.Stop()
	profiler.Stop()
	httpLn.Close()
	log.Printf("INFO: forge-worker (%s) stopped", lang)
	return runErr
}

func envOrDefault(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}
