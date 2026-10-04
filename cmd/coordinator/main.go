// Package main is the entry point for the Forge Coordinator.
//
// The serving logic lives in internal/serve/coordinator (shared with the
// forge CLI); this shell only owns signal handling and process exit.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	servecoordinator "github.com/castwell/forge/internal/serve/coordinator"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	ctx, cancel := context.WithCancel(context.Background())
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		log.Printf("INFO: received %s, shutting down...", sig)
		cancel()
	}()

	if err := servecoordinator.Run(ctx); err != nil {
		log.Fatalf("FATAL: %v", err)
	}
}
