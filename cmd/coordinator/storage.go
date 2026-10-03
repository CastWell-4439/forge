package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/castwell/forge/internal/storage"
)

// openStorage picks the storage backend:
//
//	FORGE_PG_DSN set  → PostgreSQL, with deploy/migrations applied on start
//	otherwise         → embedded BoltDB (FORGE_BOLT_PATH, default forge.db)
//
// Filling that one variable is the whole switch — the same contract as the
// database worker (D-15): config in, real backend out, no third step. The
// migrations are idempotent (IF NOT EXISTS throughout), so re-running them
// on every start is a no-op.
func openStorage(ctx context.Context) (storage.Storage, error) {
	if dsn := strings.TrimSpace(os.Getenv("FORGE_PG_DSN")); dsn != "" {
		pg, err := storage.NewPGStorage(ctx, dsn)
		if err != nil {
			return nil, fmt.Errorf("connect postgres: %w", err)
		}
		dir := envOrDefault("FORGE_MIGRATIONS_DIR", "deploy/migrations")
		if err := storage.MigrateUp(ctx, pg, dir); err != nil {
			pg.Close()
			return nil, fmt.Errorf("apply migrations: %w", err)
		}
		log.Printf("INFO: storage backend=postgres migrations=%s", dir)
		return pg, nil
	}

	boltPath := envOrDefault("FORGE_BOLT_PATH", "forge.db")
	store, err := storage.NewBoltStorage(boltPath)
	if err != nil {
		return nil, err
	}
	log.Printf("INFO: storage backend=bolt path=%s", boltPath)
	return store, nil
}
