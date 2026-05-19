package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// WorkflowCallback is called when a trigger fires with new (deduplicated) events.
type WorkflowCallback func(ctx context.Context, workflowName string, events []Event) error

// Scheduler manages registered triggers and runs them on their intervals.
type Scheduler struct {
	mu       sync.Mutex
	triggers map[string]Trigger
	dedup    Deduplicator
	callback WorkflowCallback
	logger   *slog.Logger
	cancel   context.CancelFunc
	wg       sync.WaitGroup
}

// SchedulerConfig configures the Scheduler.
type SchedulerConfig struct {
	Dedup    Deduplicator
	Callback WorkflowCallback
	Logger   *slog.Logger
}

// NewScheduler creates a new Scheduler.
func NewScheduler(cfg SchedulerConfig) *Scheduler {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Scheduler{
		triggers: make(map[string]Trigger),
		dedup:    cfg.Dedup,
		callback: cfg.Callback,
		logger:   logger,
	}
}

// Register adds a trigger to the scheduler.
func (s *Scheduler) Register(t Trigger) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.triggers[t.Name()]; exists {
		return fmt.Errorf("scheduler: trigger %q already registered", t.Name())
	}
	s.triggers[t.Name()] = t
	return nil
}

// Start begins polling all registered triggers. Blocks until Stop is called.
func (s *Scheduler) Start(ctx context.Context) {
	s.mu.Lock()
	ctx, s.cancel = context.WithCancel(ctx)
	triggers := make([]Trigger, 0, len(s.triggers))
	for _, t := range s.triggers {
		triggers = append(triggers, t)
	}
	s.mu.Unlock()

	for _, t := range triggers {
		s.wg.Add(1)
		go s.runTrigger(ctx, t)
	}

	s.wg.Wait()
}

// Stop signals all trigger goroutines to stop.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
	}
}

// TriggerCount returns the number of registered triggers.
func (s *Scheduler) TriggerCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.triggers)
}

// runTrigger runs a single trigger's poll loop.
func (s *Scheduler) runTrigger(ctx context.Context, t Trigger) {
	defer s.wg.Done()

	ticker := time.NewTicker(t.Interval())
	defer ticker.Stop()

	// Run immediately on start
	s.pollOnce(ctx, t)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.pollOnce(ctx, t)
		}
	}
}

// pollOnce executes a single poll cycle for a trigger.
func (s *Scheduler) pollOnce(ctx context.Context, t Trigger) {
	events, err := t.Check(ctx)
	if err != nil {
		s.logger.Error("trigger check failed", "trigger", t.Name(), "error", err)
		return
	}
	if len(events) == 0 {
		return
	}

	// Deduplicate
	var newEvents []Event
	for _, e := range events {
		isDup, err := s.dedup.IsDuplicate(ctx, t.Name(), e.ID)
		if err != nil {
			s.logger.Error("dedup check failed", "trigger", t.Name(), "event", e.ID, "error", err)
			continue
		}
		if isDup {
			s.logger.Debug("event deduplicated", "trigger", t.Name(), "event", e.ID)
			continue
		}
		newEvents = append(newEvents, e)
	}

	if len(newEvents) == 0 {
		return
	}

	// Mark as processed
	for _, e := range newEvents {
		if err := s.dedup.MarkProcessed(ctx, t.Name(), e.ID); err != nil {
			s.logger.Error("dedup mark failed", "trigger", t.Name(), "event", e.ID, "error", err)
		}
	}

	// Invoke callback
	if s.callback != nil {
		if err := s.callback(ctx, t.WorkflowName(), newEvents); err != nil {
			s.logger.Error("workflow callback failed",
				"trigger", t.Name(), "workflow", t.WorkflowName(), "error", err)
		} else {
			s.logger.Info("workflow triggered",
				"trigger", t.Name(), "workflow", t.WorkflowName(), "events", len(newEvents))
		}
	}
}
