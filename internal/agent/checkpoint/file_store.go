// Package checkpoint implements M12: Agent state persistence for crash recovery.
// Provides Save/Load/Latest operations on agent checkpoints.
package checkpoint

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/castwell/forge/internal/agent/core"
)

// DefaultFileStoreRoot is where checkpoints are written when no root is given.
const DefaultFileStoreRoot = ".forge-checkpoints"

// FileStore persists checkpoints as one JSON file per session step.
//
// An in-memory store cannot survive the process it is meant to recover, so a
// file-backed store is the minimum that makes a checkpoint mean anything.
// Layout: <root>/<session>/<step>-<id>.json, written through a temporary file
// and renamed, so a crash mid-write cannot leave a half-written checkpoint.
type FileStore struct {
	root string
	mu   sync.Mutex
}

// NewFileStore creates a filesystem-backed checkpoint store.
func NewFileStore(root string) *FileStore {
	if strings.TrimSpace(root) == "" {
		root = DefaultFileStoreRoot
	}
	return &FileStore{root: root}
}

// Root returns the directory checkpoints are written under.
func (s *FileStore) Root() string { return s.root }

var unsafePathChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// sessionDir maps a session id onto a directory name. The id is sanitised for
// the filesystem and suffixed with a short digest of the original, so two ids
// that sanitise to the same text still get separate directories.
func sessionDir(sessionID string) string {
	name := unsafePathChars.ReplaceAllString(strings.TrimSpace(sessionID), "-")
	name = strings.Trim(name, "-.")
	if name == "" {
		name = "session"
	}
	if len(name) > 80 {
		name = name[:80]
	}
	sum := sha256.Sum256([]byte(sessionID))
	return name + "-" + hex.EncodeToString(sum[:4])
}

func (s *FileStore) dir(sessionID string) string {
	return filepath.Join(s.root, sessionDir(sessionID))
}

// JournalFallbackPath is where the run journal's second-tier target lives:
// inside the checkpoint tree, deliberately separate from the run tree the
// primary journal writes to, so one broken tree cannot take both copies of
// the recovery state with it. It takes the same root NewFileStore takes.
func JournalFallbackPath(root, sessionID string) string {
	if strings.TrimSpace(root) == "" {
		root = DefaultFileStoreRoot
	}
	return filepath.Join(root, sessionDir(sessionID), "journal-fallback.jsonl")
}

func (s *FileStore) stepFile(cp *core.Checkpoint) string {
	return filepath.Join(s.dir(cp.SessionID), fmt.Sprintf("%010d-%s.json", cp.StepIndex, sanitizeID(cp.ID)))
}

func sanitizeID(id string) string {
	name := unsafePathChars.ReplaceAllString(strings.TrimSpace(id), "-")
	name = strings.Trim(name, "-.")
	if name == "" {
		return "checkpoint"
	}
	if len(name) > 100 {
		name = name[:100]
	}
	return name
}

// Save writes a checkpoint, replacing any earlier one for the same session step.
func (s *FileStore) Save(ctx context.Context, cp *core.Checkpoint) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if cp == nil {
		return fmt.Errorf("checkpoint is nil")
	}
	if cp.ID == "" {
		return fmt.Errorf("checkpoint ID is required")
	}
	if cp.SessionID == "" {
		return fmt.Errorf("checkpoint session_id is required")
	}

	data, err := json.Marshal(cp)
	if err != nil {
		return fmt.Errorf("marshal checkpoint %s: %w", cp.ID, err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	dir := s.dir(cp.SessionID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create checkpoint dir: %w", err)
	}

	path := s.stepFile(cp)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write checkpoint %s: %w", cp.ID, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("commit checkpoint %s: %w", cp.ID, err)
	}
	return nil
}

// Load returns a checkpoint by ID.
//
// The ID is not part of the directory layout, so this walks the session
// directories and matches on file names. It is the debugging path; resume uses
// Latest.
func (s *FileStore) Load(ctx context.Context, id string) (*core.Checkpoint, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	sessions, err := os.ReadDir(s.root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("checkpoint %q: %w", id, core.ErrNoCheckpoint)
		}
		return nil, fmt.Errorf("read checkpoint root: %w", err)
	}

	suffix := "-" + sanitizeID(id) + ".json"
	for _, session := range sessions {
		if !session.IsDir() {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(s.root, session.Name()))
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), suffix) {
				continue
			}
			return readCheckpoint(filepath.Join(s.root, session.Name(), entry.Name()))
		}
	}
	return nil, fmt.Errorf("checkpoint %q: %w", id, core.ErrNoCheckpoint)
}

// Latest returns the checkpoint with the highest step index for a session.
func (s *FileStore) Latest(ctx context.Context, sessionID string) (*core.Checkpoint, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	dir := s.dir(sessionID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("session %q: %w", sessionID, core.ErrNoCheckpoint)
		}
		return nil, fmt.Errorf("read checkpoint dir: %w", err)
	}

	// File names start with a zero-padded step index, so the last name in
	// ascending order is the newest checkpoint and no file has to be read.
	var names []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		names = append(names, entry.Name())
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("session %q: %w", sessionID, core.ErrNoCheckpoint)
	}
	sort.Strings(names)
	return readCheckpoint(filepath.Join(dir, names[len(names)-1]))
}

func readCheckpoint(path string) (*core.Checkpoint, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read checkpoint %s: %w", path, err)
	}
	var cp core.Checkpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		return nil, fmt.Errorf("parse checkpoint %s: %w", path, err)
	}
	return &cp, nil
}

var _ core.CheckpointStore = (*FileStore)(nil)
