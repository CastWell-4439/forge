package scheduler

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// --- Trigger Tests ---

func TestPollTrigger_Check(t *testing.T) {
	called := false
	pt := NewPollTrigger(PollTriggerConfig{
		Name:         "test-trigger",
		Interval:     1 * time.Second,
		Source:       "feishu_mcp",
		Query:        "status = 'open'",
		WorkflowName: "bug_fix",
		PollFn: func(ctx context.Context, source, query string) ([]Event, error) {
			called = true
			if source != "feishu_mcp" {
				t.Errorf("source = %q, want feishu_mcp", source)
			}
			if query != "status = 'open'" {
				t.Errorf("query = %q", query)
			}
			return []Event{
				{ID: "WI-1", Payload: map[string]any{"name": "bug1"}},
				{ID: "WI-2", Payload: map[string]any{"name": "bug2"}},
			}, nil
		},
	})

	if pt.Name() != "test-trigger" {
		t.Errorf("Name() = %q", pt.Name())
	}
	if pt.Interval() != time.Second {
		t.Errorf("Interval() = %v", pt.Interval())
	}
	if pt.WorkflowName() != "bug_fix" {
		t.Errorf("WorkflowName() = %q", pt.WorkflowName())
	}

	events, err := pt.Check(context.Background())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !called {
		t.Fatal("PollFn was not called")
	}
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2", len(events))
	}
}

func TestPollTrigger_NilPollFn(t *testing.T) {
	pt := NewPollTrigger(PollTriggerConfig{
		Name:         "empty",
		Interval:     time.Second,
		WorkflowName: "wf",
	})
	events, err := pt.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Errorf("expected no events, got %d", len(events))
	}
}

// --- Dedup Tests ---

func TestInMemoryDedup(t *testing.T) {
	d := NewInMemoryDedup(time.Hour)
	ctx := context.Background()

	// First time: not duplicate
	dup, err := d.IsDuplicate(ctx, "t1", "e1")
	if err != nil {
		t.Fatal(err)
	}
	if dup {
		t.Fatal("should not be duplicate on first check")
	}

	// Mark processed
	if err := d.MarkProcessed(ctx, "t1", "e1"); err != nil {
		t.Fatal(err)
	}

	// Second time: is duplicate
	dup, err = d.IsDuplicate(ctx, "t1", "e1")
	if err != nil {
		t.Fatal(err)
	}
	if !dup {
		t.Fatal("should be duplicate after marking")
	}

	// Different trigger: not duplicate
	dup, err = d.IsDuplicate(ctx, "t2", "e1")
	if err != nil {
		t.Fatal(err)
	}
	if dup {
		t.Fatal("different trigger should not be duplicate")
	}

	if d.Count() != 1 {
		t.Errorf("count = %d, want 1", d.Count())
	}
}

func TestInMemoryDedup_GC(t *testing.T) {
	d := NewInMemoryDedup(10 * time.Millisecond)
	ctx := context.Background()

	d.MarkProcessed(ctx, "t1", "e1")
	time.Sleep(20 * time.Millisecond)
	d.GC()

	if d.Count() != 0 {
		t.Errorf("count after GC = %d, want 0", d.Count())
	}
}

// --- Scheduler Tests ---

func TestScheduler_RegisterAndCount(t *testing.T) {
	s := NewScheduler(SchedulerConfig{
		Dedup: NewInMemoryDedup(time.Hour),
	})
	pt := NewPollTrigger(PollTriggerConfig{
		Name:         "t1",
		Interval:     time.Second,
		WorkflowName: "wf1",
	})
	if err := s.Register(pt); err != nil {
		t.Fatal(err)
	}
	if s.TriggerCount() != 1 {
		t.Errorf("count = %d", s.TriggerCount())
	}

	// Duplicate registration should fail
	if err := s.Register(pt); err == nil {
		t.Fatal("expected error for duplicate registration")
	}
}

func TestScheduler_PollAndDedup(t *testing.T) {
	var callCount atomic.Int32
	var mu sync.Mutex
	var receivedEvents []Event

	dedup := NewInMemoryDedup(time.Hour)
	s := NewScheduler(SchedulerConfig{
		Dedup: dedup,
		Callback: func(ctx context.Context, workflowName string, events []Event) error {
			mu.Lock()
			receivedEvents = append(receivedEvents, events...)
			mu.Unlock()
			return nil
		},
	})

	pt := NewPollTrigger(PollTriggerConfig{
		Name:     "poll1",
		Interval: 50 * time.Millisecond,
		Source:   "test",
		Query:    "",
		WorkflowName: "wf1",
		PollFn: func(ctx context.Context, source, query string) ([]Event, error) {
			callCount.Add(1)
			// Always return same events — dedup should filter after first call
			return []Event{
				{ID: "ev-1", Payload: map[string]any{"x": 1}},
				{ID: "ev-2", Payload: map[string]any{"x": 2}},
			}, nil
		},
	})

	s.Register(pt)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	go s.Start(ctx)
	<-ctx.Done()
	time.Sleep(10 * time.Millisecond) // let goroutines finish

	// Should have polled multiple times
	if callCount.Load() < 2 {
		t.Errorf("poll count = %d, expected >= 2", callCount.Load())
	}

	// But events should only be delivered once (dedup)
	mu.Lock()
	defer mu.Unlock()
	if len(receivedEvents) != 2 {
		t.Errorf("received events = %d, want 2 (dedup should block repeats)", len(receivedEvents))
	}
}

func TestScheduler_StartStop(t *testing.T) {
	s := NewScheduler(SchedulerConfig{
		Dedup: NewInMemoryDedup(time.Hour),
	})
	pt := NewPollTrigger(PollTriggerConfig{
		Name:         "t1",
		Interval:     10 * time.Millisecond,
		WorkflowName: "wf",
		PollFn: func(ctx context.Context, source, query string) ([]Event, error) {
			return nil, nil
		},
	})
	s.Register(pt)

	done := make(chan struct{})
	go func() {
		ctx := context.Background()
		s.Start(ctx)
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)
	s.Stop()

	select {
	case <-done:
		// OK
	case <-time.After(time.Second):
		t.Fatal("scheduler did not stop in time")
	}
}

// --- Feishu Poller Tests ---

func TestParseFeishuResult_Array(t *testing.T) {
	input := `[{"work_item_id": "123", "name": "bug"}, {"work_item_id": "456", "name": "feat"}]`
	events, err := parseFeishuResult(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2", len(events))
	}
	if events[0].ID != "123" {
		t.Errorf("event[0].ID = %q", events[0].ID)
	}
	if events[1].ID != "456" {
		t.Errorf("event[1].ID = %q", events[1].ID)
	}
}

func TestParseFeishuResult_Wrapper(t *testing.T) {
	input := `{"list": [{"id": "A1"}, {"id": "A2"}]}`
	events, err := parseFeishuResult(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2", len(events))
	}
	if events[0].ID != "A1" {
		t.Errorf("event[0].ID = %q", events[0].ID)
	}
}

func TestMapSourceToTool(t *testing.T) {
	cases := []struct {
		source string
		want   string
	}{
		{"feishu_mcp", "list_todo"},
		{"feishu_todo", "list_todo"},
		{"feishu_mql", "search_by_mql"},
		{"custom_tool", "custom_tool"},
	}
	for _, c := range cases {
		got := mapSourceToTool(c.source)
		if got != c.want {
			t.Errorf("mapSourceToTool(%q) = %q, want %q", c.source, got, c.want)
		}
	}
}
