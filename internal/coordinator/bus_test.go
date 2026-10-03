package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/castwell/forge/internal/storage"
)

// fakePublisher captures publishes; can be told to fail.
type fakePublisher struct {
	mu       sync.Mutex
	channel  string
	payloads []string
	err      error
}

func (f *fakePublisher) Publish(_ context.Context, channel, payload string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.channel = channel
	f.payloads = append(f.payloads, payload)
	return nil
}

func newBusTestCoordinator(t *testing.T) *Coordinator {
	t.Helper()
	store, err := storage.NewBoltStorage(filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	return NewCoordinator(store)
}

// The notification side of the pipeline: a persisted event fans out on the
// bus channel, while storage stays the source of truth.
func TestSaveEventPublishesToTheBus(t *testing.T) {
	coord := newBusTestCoordinator(t)
	pub := &fakePublisher{}
	coord.SetEventBus(pub)

	coord.saveEvent(context.Background(), "wf-bus-1", "", storage.EventWorkflowStarted,
		json.RawMessage(`{"x":1}`))

	require.Len(t, pub.payloads, 1, "the event must be published exactly once")
	assert.Equal(t, EventChannel, pub.channel)
	body := pub.payloads[0]
	assert.Contains(t, body, `"type":"WORKFLOW_STARTED"`)
	assert.Contains(t, body, `"workflow_id":"wf-bus-1"`)
	assert.Contains(t, body, `"sequence_num"`)

	// Truth layer: the event is still in storage regardless of the bus.
	history, err := coord.store.GetWorkflowHistory(context.Background(), "wf-bus-1")
	require.NoError(t, err)
	require.Len(t, history, 1, "storage remains the source of truth")
}

// Notification semantics: a bus outage logs and swallows — it must never
// fail the workflow that produced the event.
func TestSaveEventBusFailureIsNotFatal(t *testing.T) {
	coord := newBusTestCoordinator(t)
	pub := &fakePublisher{err: errors.New("bus down")}
	coord.SetEventBus(pub)

	// saveEvent returns nothing; the contract is "no panic, event stored".
	coord.saveEvent(context.Background(), "wf-bus-2", "task-1", storage.EventTaskCompleted,
		json.RawMessage(`{"ok":true}`))

	history, err := coord.store.GetWorkflowHistory(context.Background(), "wf-bus-2")
	require.NoError(t, err)
	require.Len(t, history, 1, "the event must be durable even when the bus is down")
}

// Without a configured bus, events are storage-only — the pre-wiring
// behaviour, untouched.
func TestSaveEventWithoutABusStaysStorageOnly(t *testing.T) {
	coord := newBusTestCoordinator(t)

	coord.saveEvent(context.Background(), "wf-nobus", "", storage.EventWorkflowCompleted, nil)

	history, err := coord.store.GetWorkflowHistory(context.Background(), "wf-nobus")
	require.NoError(t, err)
	require.Len(t, history, 1)
}

// The published payload must be valid JSON carrying the event identity —
// that is the contract subscribers (CDC trigger, forge events CLI) rely on.
func TestPublishedPayloadIsParseable(t *testing.T) {
	coord := newBusTestCoordinator(t)
	pub := &fakePublisher{}
	coord.SetEventBus(pub)

	coord.saveEvent(context.Background(), "wf-json", "task-9", storage.EventTaskFailed,
		json.RawMessage(`{"error":"boom"}`))

	require.Len(t, pub.payloads, 1)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal([]byte(pub.payloads[0]), &decoded))
	assert.Equal(t, "wf-json", decoded["workflow_id"])
	assert.Equal(t, "task-9", decoded["task_id"])
	assert.Equal(t, "TASK_FAILED", decoded["type"])
	assert.True(t, strings.Contains(pub.payloads[0], "boom"), "nested payload travels intact")
}
