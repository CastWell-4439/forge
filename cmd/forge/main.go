// Package main is the entry point for the forge CLI: one binary with
// coordinator / worker / standalone subcommands. The serving logic lives in
// internal/serve/* (shared with the dedicated cmd/coordinator and cmd/worker
// binaries); this shell owns argument dispatch, signal handling and exit
// codes. Configuration is environment-driven like every other entry point —
// the CLI deliberately adds no flags.
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	servecoordinator "github.com/castwell/forge/internal/serve/coordinator"
	serveworker "github.com/castwell/forge/internal/serve/worker"
)

// The coordinator's listen address, mirroring the serve package's env names.
const (
	envCoordGRPCAddr     = "FORGE_GRPC_ADDR"
	defaultCoordGRPCAddr = ":50051"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run dispatches the subcommand and returns the process exit code.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 1
	}

	switch args[0] {
	case "help", "-h", "--help":
		usage(stdout)
		return 0
	case "coordinator":
		return serveOne(servecoordinator.Run)
	case "worker":
		return serveOne(serveworker.Run)
	case "standalone":
		return runStandalone()
	default:
		fmt.Fprintf(stderr, "forge: unknown command %q\n\n", args[0])
		usage(stderr)
		return 1
	}
}

// serveOne runs a single role until SIGINT/SIGTERM (or a setup failure).
func serveOne(fn func(context.Context) error) int {
	ctx, stop := signalContext()
	defer stop()
	if err := fn(ctx); err != nil {
		log.Printf("FATAL: %v", err)
		return 1
	}
	return 0
}

// runStandalone runs coordinator and worker in one process: two Run calls
// over a shared context, so one signal tears both down. Ports keep their
// distinct defaults (coordinator :50051/:9090/:8081, worker :50052/:9091),
// making standalone behaviour identical to running the two binaries in two
// terminals — just packed into one process.
//
// Ordering matters: the worker registers with the coordinator immediately on
// Start, so launching both at once races the coordinator's listener (observed
// live: connection refused → the whole node exited). The coordinator therefore
// gets a bounded readiness wait — its gRPC port accepting connections — before
// the worker starts. A coordinator that dies during the wait fails startup; a
// signal during the wait is a clean stop.
//
// If either role fails afterwards, the other is cancelled and the failure is
// reported: a half-running single node is worse than a stopped one.
func runStandalone() int {
	ctx, stop := signalContext()
	defer stop()

	coordErr := make(chan error, 1)
	go func() { coordErr <- servecoordinator.Run(ctx) }()

	ready, err := waitCoordinatorReady(ctx, coordinatorGRPCAddr(), coordErr)
	if err != nil {
		log.Printf("FATAL: standalone: %v", err)
		return 1
	}
	if !ready {
		// Cancelled during startup: let the coordinator wind down cleanly.
		<-coordErr
		return 0
	}

	workerErr := make(chan error, 1)
	go func() { workerErr <- serveworker.Run(ctx) }()

	// Merge both results: whichever role finishes first is collected, so a
	// failing worker cancels the coordinator (and vice versa) instead of
	// blocking on the other channel forever.
	merged := make(chan error, 2)
	go func() { merged <- <-coordErr }()
	go func() { merged <- <-workerErr }()

	var first error
	for i := 0; i < 2; i++ {
		if err := <-merged; err != nil && first == nil {
			first = err
			stop() // cancel the surviving role
		}
	}
	if first != nil {
		log.Printf("FATAL: standalone: %v", first)
		return 1
	}
	return 0
}

// coordinatorGRPCAddr resolves the coordinator's listen address into a
// dialable host:port (the env value defaults to the ":50051" listen form).
func coordinatorGRPCAddr() string {
	addr := os.Getenv(envCoordGRPCAddr)
	if addr == "" {
		addr = defaultCoordGRPCAddr
	}
	if strings.HasPrefix(addr, ":") {
		return "127.0.0.1" + addr
	}
	return addr
}

// waitCoordinatorReady polls the coordinator's gRPC port. Returns ready=true
// once it accepts connections, ready=false with no error on cancellation
// (clean stop), or an error when the coordinator failed during startup.
func waitCoordinatorReady(ctx context.Context, addr string, coordErr <-chan error) (ready bool, err error) {
	deadline := time.After(30 * time.Second)
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	dialer := net.Dialer{Timeout: 2 * time.Second}

	for {
		if conn, derr := dialer.DialContext(ctx, "tcp", addr); derr == nil {
			_ = conn.Close()
			return true, nil
		}
		select {
		case cerr := <-coordErr:
			if cerr != nil {
				return false, fmt.Errorf("coordinator during startup: %w", cerr)
			}
			return false, fmt.Errorf("coordinator exited during startup")
		case <-ctx.Done():
			return false, nil
		case <-deadline:
			return false, fmt.Errorf("coordinator gRPC port %s not ready after 30s", addr)
		case <-ticker.C:
		}
	}
}

// signalContext returns a ctx cancelled on SIGINT/SIGTERM.
func signalContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		log.Printf("INFO: received %s, shutting down...", sig)
		cancel()
	}()
	return ctx, cancel
}

func usage(w io.Writer) {
	fmt.Fprint(w, `forge — single-binary entry point

Usage:
  forge coordinator   run the coordinator (same as ./cmd/coordinator)
  forge worker        run the worker (same as ./cmd/worker)
  forge standalone    run coordinator + worker in one process (single-node mode)
  forge help          show this help

Configuration is environment-driven (FORGE_* variables); see README.
`)
}
