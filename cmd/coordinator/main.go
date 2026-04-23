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

	forgev1 "github.com/castwell/forge/api/proto/gen"
	"github.com/castwell/forge/internal/coordinator"
	"github.com/castwell/forge/internal/observability"
	"github.com/castwell/forge/internal/storage"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
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
	opts := []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}
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
		"http://localhost:5173":  true, // Vite dev server
		"http://localhost:3000":  true,
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
