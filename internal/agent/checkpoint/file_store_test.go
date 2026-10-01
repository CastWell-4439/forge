package checkpoint

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/castwell/forge/internal/agent/core"
)

func sampleCheckpoint(sessionID, id string, step int, ledger ...core.ToolCallRecord) *core.Checkpoint {
	return &core.Checkpoint{
		ID:        id,
		SessionID: sessionID,
		StepIndex: step,
		Messages: []core.Message{
			{Role: "system", Content: "sys"},
			{Role: "user", Content: "do the thing"},
		},
		ToolCalls: ledger,
	}
}

// TestFileStoreSurvivesANewInstance is the property the in-memory store cannot
// have: what one process wrote, a later process can read.
func TestFileStoreSurvivesANewInstance(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()

	writer := NewFileStore(root)
	cp := sampleCheckpoint("session-a", "session-a-step-2", 2, core.ToolCallRecord{
		ID: "call-1", StepIndex: 2, Tool: "shell", Status: core.ToolCallCompleted, Idempotent: false,
	})
	if err := writer.Save(ctx, cp); err != nil {
		t.Fatalf("save: %v", err)
	}

	// A separate store instance stands in for a restarted process.
	reader := NewFileStore(root)
	got, err := reader.Latest(ctx, "session-a")
	if err != nil {
		t.Fatalf("latest after restart: %v", err)
	}
	if got.ID != cp.ID || got.StepIndex != 2 {
		t.Errorf("latest = %s@%d, want %s@2", got.ID, got.StepIndex, cp.ID)
	}
	if len(got.ToolCalls) != 1 || got.ToolCalls[0].Tool != "shell" {
		t.Errorf("the side-effect ledger did not survive the round trip: %+v", got.ToolCalls)
	}
	if len(got.Messages) != 2 {
		t.Errorf("messages = %d, want 2", len(got.Messages))
	}

	byID, err := reader.Load(ctx, cp.ID)
	if err != nil {
		t.Fatalf("load by id: %v", err)
	}
	if byID.StepIndex != 2 {
		t.Errorf("load returned step %d, want 2", byID.StepIndex)
	}
}

func TestFileStoreLatestPicksHighestStep(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	store := NewFileStore(root)

	for _, step := range []int{0, 5, 3} {
		cp := sampleCheckpoint("s", "s-step-"+string(rune('0'+step)), step)
		if err := store.Save(ctx, cp); err != nil {
			t.Fatalf("save step %d: %v", step, err)
		}
	}

	got, err := store.Latest(ctx, "s")
	if err != nil {
		t.Fatalf("latest: %v", err)
	}
	if got.StepIndex != 5 {
		t.Errorf("latest step = %d, want 5", got.StepIndex)
	}
}

func TestFileStoreReplacesSameSessionStep(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	store := NewFileStore(root)

	first := sampleCheckpoint("s", "s-step-1", 1)
	if err := store.Save(ctx, first); err != nil {
		t.Fatalf("first save: %v", err)
	}
	second := sampleCheckpoint("s", "s-step-1", 1, core.ToolCallRecord{ID: "c", Status: core.ToolCallCompleted})
	if err := store.Save(ctx, second); err != nil {
		t.Fatalf("second save: %v", err)
	}

	got, err := store.Latest(ctx, "s")
	if err != nil {
		t.Fatalf("latest: %v", err)
	}
	if len(got.ToolCalls) != 1 {
		t.Errorf("the rewritten checkpoint was not the one read back: %+v", got)
	}

	entries, err := os.ReadDir(filepath.Join(root, sessionDir("s")))
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("saving the same step twice left %d files, want 1", len(entries))
	}
}

func TestFileStoreMissingCheckpointIsDistinguishable(t *testing.T) {
	store := NewFileStore(t.TempDir())
	ctx := context.Background()

	if _, err := store.Latest(ctx, "never-seen"); !errors.Is(err, core.ErrNoCheckpoint) {
		t.Errorf("Latest on an unknown session: err = %v, want ErrNoCheckpoint", err)
	}
	if _, err := store.Load(ctx, "nope"); !errors.Is(err, core.ErrNoCheckpoint) {
		t.Errorf("Load on an unknown id: err = %v, want ErrNoCheckpoint", err)
	}
}

func TestFileStoreRequiresIdentity(t *testing.T) {
	store := NewFileStore(t.TempDir())
	ctx := context.Background()

	for _, tc := range []struct {
		name string
		cp   *core.Checkpoint
	}{
		{"nil checkpoint", nil},
		{"missing id", &core.Checkpoint{SessionID: "s"}},
		{"missing session", &core.Checkpoint{ID: "id"}},
	} {
		if err := store.Save(ctx, tc.cp); err == nil {
			t.Errorf("%s: expected an error", tc.name)
		}
	}
}

// TestFileStoreSeparatesSessionsThatSanitiseAlike guards the directory naming:
// two different sessions must not collide because their ids reduce to the same
// safe text.
func TestFileStoreSeparatesSessionsThatSanitiseAlike(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	store := NewFileStore(root)

	if err := store.Save(ctx, sampleCheckpoint("a/b", "a-b-step-0", 0)); err != nil {
		t.Fatalf("save first: %v", err)
	}
	if err := store.Save(ctx, sampleCheckpoint("a b", "a-b-step-9", 9)); err != nil {
		t.Fatalf("save second: %v", err)
	}

	first, err := store.Latest(ctx, "a/b")
	if err != nil {
		t.Fatalf("latest a/b: %v", err)
	}
	if first.StepIndex != 0 {
		t.Errorf("session %q resolved to step %d, which belongs to the other session", "a/b", first.StepIndex)
	}

	second, err := store.Latest(ctx, "a b")
	if err != nil {
		t.Fatalf("latest a b: %v", err)
	}
	if second.StepIndex != 9 {
		t.Errorf("session %q resolved to step %d, which belongs to the other session", "a b", second.StepIndex)
	}
}
