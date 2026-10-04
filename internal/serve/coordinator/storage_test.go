package coordinator

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/castwell/forge/internal/storage"
)

// The default stays the embedded backend: no config, no external service,
// coordinator starts exactly as before.
func TestOpenStorageDefaultsToBolt(t *testing.T) {
	t.Setenv("FORGE_PG_DSN", "")
	t.Setenv("FORGE_BOLT_PATH", filepath.Join(t.TempDir(), "test.db"))

	store, err := openStorage(context.Background())
	if err != nil {
		t.Fatalf("openStorage: %v", err)
	}
	defer store.Close()

	if _, ok := store.(*storage.BoltStorage); !ok {
		t.Errorf("backend = %T, want *storage.BoltStorage without FORGE_PG_DSN", store)
	}
}

// Filling FORGE_PG_DSN is the whole switch: PostgreSQL comes up with the
// migrations applied, verified by reading through the interface (the read
// fails if the schema is missing).
func TestOpenStorageSwitchesToPostgres(t *testing.T) {
	if os.Getenv("FORGE_PG_DSN") == "" {
		t.Skip("FORGE_PG_DSN not set; runs in CI with the postgres service")
	}
	t.Setenv("FORGE_MIGRATIONS_DIR", filepath.Join("..", "..", "..", "deploy", "migrations"))

	store, err := openStorage(context.Background())
	if err != nil {
		t.Fatalf("openStorage: %v", err)
	}
	defer store.Close()

	pg, ok := store.(*storage.PGStorage)
	if !ok {
		t.Fatalf("backend = %T, want *storage.PGStorage with FORGE_PG_DSN", store)
	}
	// The schema is really there: this query hits workflow_instances.
	if _, err := pg.ListWorkflows(context.Background(), "", 1, 0); err != nil {
		t.Fatalf("schema not usable after migrations: %v", err)
	}
}
