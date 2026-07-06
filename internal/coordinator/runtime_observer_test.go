package coordinator

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/castwell/forge/internal/storage"
)

type recordingRuntimeObserver struct {
	events []*storage.Event
	err    error
}

func (o *recordingRuntimeObserver) ObserveEvent(ctx context.Context, event *storage.Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	o.events = append(o.events, event)
	return o.err
}

func TestCoordinatorSaveEventNotifiesRuntimeObserver(t *testing.T) {
	ctx := context.Background()
	store, err := storage.NewBoltStorage(filepath.Join(t.TempDir(), "forge.db"))
	if err != nil {
		t.Fatalf("NewBoltStorage: %v", err)
	}
	defer store.Close()

	coord := NewCoordinator(store)
	observer := &recordingRuntimeObserver{}
	coord.SetRuntimeObserver(observer)

	coord.saveEvent(ctx, "wf-observed", "task-1", storage.EventTaskStarted, nil)

	if len(observer.events) != 1 {
		t.Fatalf("observer event count = %d, want 1", len(observer.events))
	}
	got := observer.events[0]
	if got.WorkflowID != "wf-observed" || got.TaskID != "task-1" || got.Type != storage.EventTaskStarted || got.SequenceNum == 0 {
		t.Fatalf("observer event mismatch: %+v", got)
	}

	history, err := store.GetWorkflowHistory(ctx, "wf-observed")
	if err != nil {
		t.Fatalf("GetWorkflowHistory: %v", err)
	}
	if len(history) != 1 || history[0].Type != storage.EventTaskStarted {
		t.Fatalf("history mismatch: %+v", history)
	}
}

func TestCoordinatorSaveEventIgnoresRuntimeObserverError(t *testing.T) {
	ctx := context.Background()
	store, err := storage.NewBoltStorage(filepath.Join(t.TempDir(), "forge.db"))
	if err != nil {
		t.Fatalf("NewBoltStorage: %v", err)
	}
	defer store.Close()

	coord := NewCoordinator(store)
	coord.SetRuntimeObserver(&recordingRuntimeObserver{err: fmt.Errorf("observer failed")})

	coord.saveEvent(ctx, "wf-observer-error", "", storage.EventWorkflowStarted, nil)

	history, err := store.GetWorkflowHistory(ctx, "wf-observer-error")
	if err != nil {
		t.Fatalf("GetWorkflowHistory: %v", err)
	}
	if len(history) != 1 || history[0].Type != storage.EventWorkflowStarted {
		t.Fatalf("history mismatch after observer error: %+v", history)
	}
}
