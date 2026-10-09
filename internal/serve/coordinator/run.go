// Package coordinator assembles the Forge Coordinator's serving logic —
// storage, event bus, CDC, discovery, observability and all three servers —
// as a ctx-driven Run function. It is shared by every entry point: the
// dedicated cmd/coordinator binary, and the forge CLI's `coordinator` and
// `standalone` subcommands. Signal handling stays in the entry points.
package coordinator

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/reflection"

	forgev1 "github.com/castwell/forge/api/proto/gen"
	"github.com/castwell/forge/internal/coordinator"
	"github.com/castwell/forge/internal/forgex/failure"
	"github.com/castwell/forge/internal/forgex/policy"
	forgexruntime "github.com/castwell/forge/internal/forgex/runtime"
	"github.com/castwell/forge/internal/forgex/toolgw"
	"github.com/castwell/forge/internal/observability"
)

// Run assembles and serves the coordinator until appCtx is cancelled, then
// tears everything down in the same order the original binary did. Setup
// failures are returned (the entry point decides how to report them);
// per-server runtime errors stay logged, exactly as before.
func Run(appCtx context.Context) error {
	log.Println("INFO: forge-coordinator starting...")

	// --- Storage ---
	store, err := openStorage(context.Background())
	if err != nil {
		return fmt.Errorf("open storage: %w", err)
	}
	defer store.Close()

	// --- Coordinator ---
	coord := coordinator.NewCoordinator(store)

	// --- Event notification bus + durable heartbeat store (optional) ---
	// FORGE_NATS_URL → JetStream events + NATS KV heartbeats; PostgreSQL
	// storage without NATS → LISTEN/NOTIFY; neither → storage-only events.
	publisher, hbStore, closeBus, err := setupNATS(store)
	if err != nil {
		return fmt.Errorf("event bus: %w", err)
	}
	defer closeBus()
	if publisher != nil {
		coord.SetEventBus(publisher)
	}

	// Dispatch-time template rendering: workflow {{...}} params resolve
	// against the workflow's inputs and its dependencies' named outputs just
	// before execution — the execution half of the YAML template contract.
	coord.SetParamRenderer(registryParamRenderer())

	// --- CDC triggers (optional) ---
	// FORGE_CDC_TRIGGERS unset = off; configured-but-broken fails startup.
	stopCDC, err := setupCDC(appCtx, coord)
	if err != nil {
		return fmt.Errorf("cdc: %w", err)
	}
	defer stopCDC()

	// --- Discovery & leader election (etcd, optional) ---
	// Without FORGE_ETCD_* this is a no-op and the coordinator stays in its
	// standalone mode: direct worker registration, always leader.
	stopDiscovery, err := setupDiscovery(appCtx, coord, hbStore)
	if err != nil {
		return fmt.Errorf("discovery: %w", err)
	}
	defer stopDiscovery()

	// --- Kueue GPU queue (optional) ---
	// FORGE_KUEUE_ENABLED unset = off (dispatch unchanged); enabled but
	// misconfigured fails startup with the cluster error. When on, GPU tasks
	// are submitted to Kubernetes and the reconciler reports their results
	// back — submission without write-back would park tasks in RUNNING.
	kueueManager, err := setupKueue(coord)
	if err != nil {
		return err
	}
	startKueueReconciler(appCtx, coord, kueueManager)

	// --- Task/workflow timeouts ---
	// The timeout manager was written but never assembled, so no task has ever
	// been failed for exceeding its deadline. That gap also left Kueue jobs
	// without a backstop: their deadline is written into TimeoutAt, and this
	// loop is what reads it.
	startTimeoutManager(appCtx, coord)

	// --- Workflow triggers (optional) ---
	// FORGE_WORKFLOW_TRIGGERS unset = off (zero change); on = the cron/poll
	// blocks already declared in workflow YAML start firing.
	stopTriggers, err := setupTriggers(appCtx, coord)
	if err != nil {
		return fmt.Errorf("triggers: %w", err)
	}
	defer stopTriggers()

	if envBool("FORGEX_RUNTIME_OBSERVER_ENABLED") {
		root := envOrDefault("FORGEX_RUNTIME_ROOT", ".forgex-runtime")
		observerCfg := forgexruntime.FileObserverConfig{
			Root:      root,
			AutoIndex: envBool("FORGEX_RUNTIME_AUTO_INDEX"),
			Authority: envOrDefault("FORGEX_RUNTIME_AUTHORITY", "L0"),
		}
		if contractsPath := os.Getenv("FORGEX_RUNTIME_CONTRACTS"); contractsPath != "" {
			contracts, err := toolgw.LoadContracts(contractsPath)
			if err != nil {
				return fmt.Errorf("load ForgeX runtime contracts: %w", err)
			}
			observerCfg.Contracts = contracts
		}
		if policyPath := os.Getenv("FORGEX_RUNTIME_POLICY"); policyPath != "" {
			policyCfg, err := policy.LoadConfig(policyPath)
			if err != nil {
				return fmt.Errorf("load ForgeX runtime policy: %w", err)
			}
			observerCfg.Policy = policy.NewEngine(policyCfg)
		}
		if taxonomyPath := os.Getenv("FORGEX_RUNTIME_TAXONOMY"); taxonomyPath != "" {
			taxonomy, err := failure.LoadTaxonomy(taxonomyPath)
			if err != nil {
				return fmt.Errorf("load ForgeX runtime taxonomy: %w", err)
			}
			observerCfg.Taxonomy = taxonomy
		}
		observerCfg.EvalRules = os.Getenv("FORGEX_RUNTIME_EVAL_RULES")
		observerCfg.EvalSuite = os.Getenv("FORGEX_RUNTIME_EVAL_SUITE")
		coord.SetRuntimeObserver(forgexruntime.NewFileObserver(observerCfg))
		log.Printf("INFO: ForgeX runtime observer enabled root=%s auto_index=%v authority=%s contracts=%v policy=%v taxonomy=%v auto_eval=%v", root, envBool("FORGEX_RUNTIME_AUTO_INDEX"), observerCfg.Authority, observerCfg.Contracts != nil, observerCfg.Policy != nil, observerCfg.Taxonomy != nil, observerCfg.EvalRules != "" && observerCfg.EvalSuite != "")
	}

	// --- Observability ---
	metrics := observability.NewMetrics()
	// Hand the counters to the state machine. Without this the /metrics endpoint
	// publishes six series that can only ever read zero: the coordinator
	// produced no observations at all, which reads as an idle system rather than
	// an uninstrumented one.
	coord.SetMetrics(observability.NewCoordinatorMetrics(metrics))
	// The tracer is installed process-wide: the gRPC server and every dial
	// site read it through the interceptors, so wiring happens here once.
	observability.TracerConfigEnv("forge-coordinator")
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

	// --- Workflow entry points (webhook / manual) ---
	// Mounted on the same listener. Whether they are registered at all depends
	// on the bind address and the secret (see webhook.go): a routable address
	// without a secret gets no entry point rather than an open one.
	registerWorkflowEntryPoints(mux, coord)

	// --- Human-in-the-loop ---
	// The endpoints an operator answers approvals at, plus the sweep that times
	// out requests nobody answered. The coordinator supplies the store and the
	// release path; the requests themselves are filed by the worker's hitl
	// handler in its own process (see hitl.go).
	hitlSweep := setupHITL(coord, store, mux)

	// The sweep runs on its own goroutine rather than inside the timeout
	// manager: they watch different things (task deadlines versus human
	// requests) and either may be in play without the other.
	go hitlSweep(appCtx)

	httpAddr := httpAddr()
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

	// --- gRPC Server ---
	grpcAddr := envOrDefault("FORGE_GRPC_ADDR", ":50051")
	grpcLn, err := net.Listen("tcp", grpcAddr)
	if err != nil {
		return fmt.Errorf("listen gRPC %s: %w", grpcAddr, err)
	}

	grpcServer := grpc.NewServer(observability.ServerOptions()...)
	forgev1.RegisterCoordinatorServiceServer(grpcServer, coord)
	// Out-of-process workers register themselves through WorkerService/
	// Register on THIS listener; without it every worker died at startup
	// with "unknown service" (see register_rpc.go).
	forgev1.RegisterWorkerServiceServer(grpcServer, coord)
	reflection.Register(grpcServer)

	go func() {
		log.Printf("INFO: gRPC server listening on %s", grpcAddr)
		if err := grpcServer.Serve(grpcLn); err != nil {
			log.Printf("ERROR: gRPC serve: %v", err)
		}
	}()

	// --- gRPC-Gateway REST Server ---
	ctx := context.Background()
	gwMux := runtime.NewServeMux()
	opts := append([]grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())},
		observability.ClientDialOptions()...)
	if err := forgev1.RegisterCoordinatorServiceHandlerFromEndpoint(ctx, gwMux, grpcAddr, opts); err != nil {
		return fmt.Errorf("register gRPC-Gateway: %w", err)
	}

	restAddr := envOrDefault("FORGE_REST_ADDR", ":8081")
	restLn, err := net.Listen("tcp", restAddr)
	if err != nil {
		return fmt.Errorf("listen REST %s: %w", restAddr, err)
	}
	go func() {
		log.Printf("INFO: REST server (gRPC-Gateway) listening on %s", restAddr)
		if err := http.Serve(restLn, corsMiddleware(gwMux)); err != nil {
			log.Printf("ERROR: REST serve: %v", err)
		}
	}()

	// --- Graceful Shutdown: the entry point cancels appCtx, which already
	// stops leader election, the worker watch, CDC and the failure detector;
	// teardown below mirrors the original binary's order.
	<-appCtx.Done()
	log.Println("INFO: forge-coordinator shutting down...")
	grpcServer.GracefulStop()
	profiler.Stop()
	restLn.Close()
	httpLn.Close()
	store.Close()
	log.Println("INFO: forge-coordinator stopped")
	return nil
}

// corsMiddleware wraps an http.Handler with CORS headers for dashboard cross-origin access.
// In production, set FORGE_CORS_ORIGINS to a comma-separated allowlist (e.g. "https://dashboard.example.com").
// Default (empty or unset): allows localhost origins only.
func corsMiddleware(h http.Handler) http.Handler {
	allowedRaw := os.Getenv("FORGE_CORS_ORIGINS")
	allowed := map[string]bool{
		"http://localhost:5173": true, // Vite dev server
		"http://localhost:3000": true,
		"http://127.0.0.1:5173": true,
		"http://127.0.0.1:3000": true,
	}
	if allowedRaw != "" {
		for _, origin := range strings.Split(allowedRaw, ",") {
			origin = strings.TrimSpace(origin)
			if origin != "" {
				allowed[origin] = true
			}
		}
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if allowed[origin] {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h.ServeHTTP(w, r)
	})
}

func envOrDefault(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

func envBool(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}
