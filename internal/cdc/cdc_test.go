package cdc

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Interface tests ---

func TestSourceConfigMatchesEvent(t *testing.T) {
	cfg := SourceConfig{Events: []Operation{OpInsert, OpUpdate}}

	assert.True(t, cfg.MatchesEvent(Event{Operation: OpInsert}))
	assert.True(t, cfg.MatchesEvent(Event{Operation: OpUpdate}))
	assert.False(t, cfg.MatchesEvent(Event{Operation: OpDelete}))
}

func TestSourceConfigMatchesEventEmpty(t *testing.T) {
	cfg := SourceConfig{} // no event filter = match all
	assert.True(t, cfg.MatchesEvent(Event{Operation: OpDelete}))
}

// --- PG CDC tests ---

func TestPGCDCSourceSubscribe(t *testing.T) {
	// Mock query function that returns one INSERT event.
	callCount := 0
	queryFn := func(ctx context.Context, query string) ([]map[string]interface{}, error) {
		callCount++
		if callCount == 1 {
			return []map[string]interface{}{
				{
					"operation": "INSERT",
					"new_data":  map[string]interface{}{"id": "123", "status": "pending"},
					"lsn":       uint64(1),
				},
			}, nil
		}
		return nil, nil
	}

	source := NewPGCDCSource("postgres://localhost/test", SourceConfig{
		Table:    "orders",
		Events:   []Operation{OpInsert},
		SlotName: "test_slot",
	}, WithQueryFunc(queryFn))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var mu sync.Mutex
	var received []Event

	go func() {
		_ = source.Subscribe(ctx, func(event Event) {
			mu.Lock()
			received = append(received, event)
			mu.Unlock()
		})
	}()

	// Wait for at least one poll cycle.
	time.Sleep(1500 * time.Millisecond)
	cancel()

	mu.Lock()
	assert.GreaterOrEqual(t, len(received), 1)
	if len(received) > 0 {
		assert.Equal(t, OpInsert, received[0].Operation)
		assert.Equal(t, "orders", received[0].Table)
	}
	mu.Unlock()
}

func TestPGCDCSourceFilter(t *testing.T) {
	queryFn := func(ctx context.Context, query string) ([]map[string]interface{}, error) {
		return []map[string]interface{}{
			{
				"operation": "INSERT",
				"new_data":  map[string]interface{}{"id": "1", "status": "pending"},
				"lsn":       uint64(1),
			},
			{
				"operation": "INSERT",
				"new_data":  map[string]interface{}{"id": "2", "status": "completed"},
				"lsn":       uint64(2),
			},
		}, nil
	}

	source := NewPGCDCSource("postgres://localhost/test", SourceConfig{
		Table:    "orders",
		Events:   []Operation{OpInsert},
		Filter:   "status = 'pending'",
		SlotName: "test_slot",
	}, WithQueryFunc(queryFn))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var mu sync.Mutex
	var received []Event

	go func() {
		_ = source.Subscribe(ctx, func(event Event) {
			mu.Lock()
			received = append(received, event)
			mu.Unlock()
		})
	}()

	time.Sleep(1500 * time.Millisecond)
	cancel()

	mu.Lock()
	// Only "pending" events should pass the filter.
	for _, e := range received {
		assert.Equal(t, "pending", e.NewData["status"])
	}
	mu.Unlock()
}

func TestPGCDCSourceClose(t *testing.T) {
	source := NewPGCDCSource("", SourceConfig{}, WithQueryFunc(
		func(ctx context.Context, query string) ([]map[string]interface{}, error) {
			return nil, nil
		},
	))

	// Close should not panic.
	err := source.Close()
	assert.NoError(t, err)

	// Double close should not panic.
	err = source.Close()
	assert.NoError(t, err)
}

// --- Filter evaluation tests ---

func TestEvaluateSimpleFilter(t *testing.T) {
	data := map[string]interface{}{
		"status": "pending",
		"type":   "order",
	}

	assert.True(t, evaluateSimpleFilter("status = 'pending'", data))
	assert.False(t, evaluateSimpleFilter("status = 'completed'", data))
	assert.True(t, evaluateSimpleFilter("status != 'completed'", data))
	assert.False(t, evaluateSimpleFilter("status != 'pending'", data))
	assert.True(t, evaluateSimpleFilter("", data))            // empty filter = pass
	assert.False(t, evaluateSimpleFilter("missing = 'x'", data)) // missing field
}

// --- Trigger config tests ---

func TestParseTriggerConfig(t *testing.T) {
	yamlData := []byte(`
triggers:
  - name: order-created
    type: cdc
    source:
      type: postgres
      table: orders
      events: [INSERT]
      filter: "status = 'pending'"
    workflow: process-order
    params_mapping:
      order_id: "{{.new.id}}"
      amount: "{{.new.total_amount}}"
  - name: video-uploaded
    type: cdc
    source:
      type: redis
      pattern: "upload:video:*"
      events: [SET]
    workflow: video-pipeline
`)

	ts, err := ParseTriggerConfig(yamlData)
	require.NoError(t, err)
	assert.Len(t, ts.Triggers, 2)

	assert.Equal(t, "order-created", ts.Triggers[0].Name)
	assert.Equal(t, "postgres", ts.Triggers[0].Source.Type)
	assert.Equal(t, "orders", ts.Triggers[0].Source.Table)
	assert.Equal(t, []string{"INSERT"}, ts.Triggers[0].Source.Events)
	assert.Equal(t, "process-order", ts.Triggers[0].Workflow)
	assert.Equal(t, "{{.new.id}}", ts.Triggers[0].ParamsMapping["order_id"])
}

// --- Trigger Manager tests ---

func TestTriggerManagerLoadAndCount(t *testing.T) {
	submitted := make([]string, 0)
	submitter := func(ctx context.Context, workflow string, params map[string]interface{}) error {
		submitted = append(submitted, workflow)
		return nil
	}

	mgr := NewTriggerManager(submitter)

	ts := &TriggerSet{
		Triggers: []TriggerConfig{
			{Name: "t1", Type: "cdc", Workflow: "wf1", Source: TriggerSource{Type: "postgres"}},
			{Name: "t2", Type: "cdc", Workflow: "wf2", Source: TriggerSource{Type: "postgres"}},
		},
	}

	err := mgr.LoadConfig(ts, func(src TriggerSource) (Source, error) {
		return NewPGCDCSource("", SourceConfig{}, WithQueryFunc(
			func(ctx context.Context, q string) ([]map[string]interface{}, error) {
				return nil, nil
			},
		)), nil
	})

	require.NoError(t, err)
	assert.Equal(t, 2, mgr.TriggerCount())
}

// --- Template resolution tests ---

func TestResolveTemplate(t *testing.T) {
	event := Event{
		NewData: map[string]interface{}{"id": "order-123", "amount": 99.5},
		OldData: map[string]interface{}{"id": "order-123", "amount": 50.0},
	}

	assert.Equal(t, "order-123", resolveTemplate("{{.new.id}}", event))
	assert.Equal(t, 99.5, resolveTemplate("{{.new.amount}}", event))
	assert.Equal(t, 50.0, resolveTemplate("{{.old.amount}}", event))
	assert.Equal(t, "literal", resolveTemplate("literal", event))
	assert.Equal(t, ".new.missing", resolveTemplate("{{.new.missing}}", event))
}
