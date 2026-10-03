// Package main is the entry point for the Forge Coordinator.
package main

import (
	"context"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

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

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Println("INFO: forge-coordinator starting...")
	appCtx, appCancel := context.WithCancel(context.Background())
	defer appCancel()

	// --- Storage ---
	store, err := openStorage(context.Background())
	if err != nil {
		log.Fatalf("FATAL: open storage: %v", err)
	}
	defer store.Close()

	// --- Coordinator ---
	coord := coordinator.NewCoordinator(store)

	// --- Event notification bus + durable heartbeat store (optional) ---
	// FORGE_NATS_URL → JetStream events + NATS KV heartbeats; PostgreSQL
	// storage without NATS → LISTEN/NOTIFY; neither → storage-only events.
	publisher, hbStore, closeBus, err := setupNATS(store)
	if err != nil {
		log.Fatalf("FATAL: event bus: %v", err)
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
		log.Fatalf("FATAL: cdc: %v", err)
	}
	defer stopCDC()

	// --- Discovery & leader election (etcd, optional) ---
	// Without FORGE_ETCD_* this is a no-op and the coordinator stays in its
	// standalone mode: direct worker registration, always leader.
	stopDiscovery, err := setupDiscovery(appCtx, coord, hbStore)
	if err != nil {
		log.Fatalf("FATAL: discovery: %v", err)
	}
	defer stopDiscovery()

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
				log.Fatalf("FATAL: load ForgeX runtime contracts: %v", err)
			}
			observerCfg.Contracts = contracts
		}
		if policyPath := os.Getenv("FORGEX_RUNTIME_POLICY"); policyPath != "" {
			policyCfg, err := policy.LoadConfig(policyPath)
			if err != nil {
				log.Fatalf("FATAL: load ForgeX runtime policy: %v", err)
			}
			observerCfg.Policy = policy.NewEngine(policyCfg)
		}
		if taxonomyPath := os.Getenv("FORGEX_RUNTIME_TAXONOMY"); taxonomyPath != "" {
			taxonomy, err := failure.LoadTaxonomy(taxonomyPath)
			if err != nil {
				log.Fatalf("FATAL: load ForgeX runtime taxonomy: %v", err)
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

	grpcServer := grpc.NewServer(observability.ServerOptions()...)
	forgev1.RegisterCoordinatorServiceServer(grpcServer, coord)
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
		log.Fatalf("FATAL: register gRPC-Gateway: %v", err)
	}

	restAddr := envOrDefault("FORGE_REST_ADDR", ":8081")
	restLn, err := net.Listen("tcp", restAddr)
	if err != nil {
		log.Fatalf("FATAL: listen REST %s: %v", restAddr, err)
	}
	go func() {
		log.Printf("INFO: REST server (gRPC-Gateway) listening on %s", restAddr)
		if err := http.Serve(restLn, corsMiddleware(gwMux)); err != nil {
			log.Printf("ERROR: REST serve: %v", err)
		}
	}()

	// --- Graceful Shutdown ---
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	log.Printf("INFO: received %s, shutting down...", sig)
	appCancel() // stop leader election and worker watch before tearing down
	grpcServer.GracefulStop()
	profiler.Stop()
	restLn.Close()
	httpLn.Close()
	store.Close()
	log.Println("INFO: forge-coordinator stopped")
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
