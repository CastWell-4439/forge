package storage

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// MigrateUp applies every *.sql file in dir, in filename order, against the
// database. The files are idempotent (CREATE ... IF NOT EXISTS throughout),
// so calling MigrateUp on every start is safe.
//
// A missing directory is a warning, not an error: it only means the binary
// was started outside the repository checkout, and the schema may already
// exist (psql -f deploy/migrations/... is the manual path). Anything else —
// unreadable file, rejected SQL — stops the caller, because booting against
// a half-migrated schema produces failures that look like application bugs.
func MigrateUp(ctx context.Context, s *PGStorage, dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			log.Printf("WARN: migrations dir %s not found; assuming the schema already exists", dir)
			return nil
		}
		return fmt.Errorf("read migrations dir %s: %w", dir, err)
	}

	applied := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		sql, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", path, err)
		}
		if err := s.RunMigrations(ctx, string(sql)); err != nil {
			return fmt.Errorf("apply migration %s: %w", entry.Name(), err)
		}
		applied++
	}
	log.Printf("INFO: migrations up to date (%d files from %s)", applied, dir)
	return nil
}
