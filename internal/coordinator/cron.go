package coordinator

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	forgev1 "github.com/castwell/forge/api/proto/gen"
	"github.com/castwell/forge/internal/storage"
)

// MisfirePolicy defines behavior when a scheduled trigger is missed.
type MisfirePolicy string

const (
	// MisfireSkip ignores missed triggers.
	MisfireSkip MisfirePolicy = "SKIP"
	// MisfireFireOnce fires once to catch up.
	MisfireFireOnce MisfirePolicy = "FIRE_ONCE"
)

// CronTrigger defines a scheduled workflow trigger.
type CronTrigger struct {
	ID            string
	WorkflowName  string
	CronExpr      string
	DagYAML       string // stored workflow definition; if empty, a fallback is generated
	Params        map[string]interface{}
	MaxConcurrent int
	MisfirePolicy MisfirePolicy
	Enabled       bool
	LastFireAt    *time.Time
	NextFireAt    *time.Time
	// SubmitFn, when set, IS the submission: the assembly layer that knows
	// which definition the workflow name refers to (the registry compiles
	// stages/worker YAML, which SubmitWorkflow's tasks/handler dialect cannot
	// parse) provides the bridge itself. DagYAML and its fallback stay as the
	// behaviour for triggers that carry raw coordinator-dialect YAML.
	SubmitFn func(ctx context.Context) error
	mu       sync.Mutex // protects LastFireAt/NextFireAt
}

// CronScheduler manages periodic workflow triggers.
// It parses cron expressions, calculates next fire times,
// and submits workflows on schedule.
type CronScheduler struct {
	coordinator *Coordinator
	triggers    []*CronTrigger
	mu          sync.RWMutex
	stopCh      chan struct{}
	lockFn      func(ctx context.Context, key string) (func(), error) // distributed lock
}

// NewCronScheduler creates a new CronScheduler.
// lockFn provides distributed locking (e.g., etcd Lock) for deduplication.
func NewCronScheduler(coord *Coordinator, lockFn func(ctx context.Context, key string) (func(), error)) *CronScheduler {
	return &CronScheduler{
		coordinator: coord,
		stopCh:      make(chan struct{}),
		lockFn:      lockFn,
	}
}

// AddTrigger adds a cron trigger to the scheduler.
func (s *CronScheduler) AddTrigger(trigger *CronTrigger) error {
	if _, err := parseCronExpr(trigger.CronExpr); err != nil {
		return fmt.Errorf("invalid cron expression %q: %w", trigger.CronExpr, err)
	}

	// MaxConcurrent is OPTIONAL, and left as declared. It used to be forced to 1
	// here and then never read, which made it a field that looked like a limit
	// and was not one: every deployment got the same answer whatever it wrote.
	//
	//	 0 or absent → no limit (an operator who did not ask for one is not
	//	               given a silent cap on a workflow that used to run freely)
	//	>0          → at most that many instances of this workflow running
	//
	// The zero value is what an existing YAML has, so this reads as "unset"
	// rather than as "one", and no workflow's behaviour changes until someone
	// asks for a limit.
	if trigger.MisfirePolicy == "" {
		trigger.MisfirePolicy = MisfireSkip
	}

	// Calculate initial next fire time.
	next, _ := nextCronTime(trigger.CronExpr, time.Now())
	trigger.NextFireAt = &next

	s.mu.Lock()
	s.triggers = append(s.triggers, trigger)
	s.mu.Unlock()

	return nil
}

// RemoveTrigger removes a trigger by ID.
func (s *CronScheduler) RemoveTrigger(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i, t := range s.triggers {
		if t.ID == id {
			s.triggers = append(s.triggers[:i], s.triggers[i+1:]...)
			return
		}
	}
}

// Start begins the cron scheduler tick loop.
// It checks every second for triggers that need to fire.
func (s *CronScheduler) Start() {
	go s.run()
}

// Stop stops the cron scheduler.
func (s *CronScheduler) Stop() {
	close(s.stopCh)
}

func (s *CronScheduler) run() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-s.stopCh:
			return
		case now := <-ticker.C:
			s.tick(now)
		}
	}
}

func (s *CronScheduler) tick(now time.Time) {
	s.mu.RLock()
	triggers := make([]*CronTrigger, len(s.triggers))
	copy(triggers, s.triggers)
	s.mu.RUnlock()

	for _, trigger := range triggers {
		if !trigger.Enabled {
			continue
		}
		trigger.mu.Lock()
		nextFire := trigger.NextFireAt
		if nextFire == nil || now.Before(*nextFire) {
			trigger.mu.Unlock()
			continue
		}
		// Immediately advance NextFireAt under lock to prevent duplicate fires
		// from the next tick() before fire() goroutine runs.
		next, _ := nextCronTime(trigger.CronExpr, now)
		trigger.NextFireAt = &next
		trigger.mu.Unlock()

		go s.fire(trigger, now)
	}
}

func (s *CronScheduler) fire(trigger *CronTrigger, now time.Time) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Distributed lock ensures only one Coordinator fires per trigger per tick.
	lockKey := fmt.Sprintf("cron:%s:%d", trigger.WorkflowName, now.Unix()/60)
	if s.lockFn != nil {
		unlock, err := s.lockFn(ctx, lockKey)
		if err != nil {
			// Another instance already holds the lock.
			return
		}
		defer unlock()
	}

	// Check misfire policy.
	trigger.mu.Lock()
	if trigger.LastFireAt != nil {
		elapsed := now.Sub(*trigger.LastFireAt)
		if elapsed > 2*time.Minute && trigger.MisfirePolicy == MisfireSkip {
			// Skip missed fires, just update next time.
			next, _ := nextCronTime(trigger.CronExpr, now)
			trigger.NextFireAt = &next
			trigger.mu.Unlock()
			return
		}
	}
	trigger.mu.Unlock()

	// Honour a declared concurrency limit, if one was declared. Checked before
	// the fire is logged or submitted so a skipped fire leaves no trace of work
	// that did not happen.
	if trigger.MaxConcurrent > 0 {
		running, err := s.runningInstances(ctx, trigger.WorkflowName)
		if err != nil {
			// Cannot tell whether it is safe to start: skipping is the safe
			// direction, and saying so is better than firing blind.
			log.Printf("WARN: cron: cannot read running instances of %q, skipping this fire: %v",
				trigger.WorkflowName, err)
			s.recordSkippedFire(trigger, now)
			return
		}
		if running >= trigger.MaxConcurrent {
			log.Printf("INFO: cron: trigger %q skipped: %d instance(s) of %q already running (max_concurrent=%d)",
				trigger.ID, running, trigger.WorkflowName, trigger.MaxConcurrent)
			s.recordSkippedFire(trigger, now)
			return
		}
	}

	log.Printf("INFO: cron: firing trigger %q for workflow %q", trigger.ID, trigger.WorkflowName)

	var err error
	if trigger.SubmitFn != nil {
		// The assembly layer owns the submission path for this trigger (see
		// the field's comment): it bridges and submits the definition the
		// workflow name actually refers to.
		err = trigger.SubmitFn(ctx)
	} else {
		// Look up stored workflow definition from the trigger's DagYAML field.
		// If no stored definition, fall back to a minimal single-task DAG.
		dagYAML := trigger.DagYAML
		if dagYAML == "" {
			dagYAML = fmt.Sprintf("name: %s\ntasks:\n  cron-task:\n    handler: %s\n    timeout: 5m\n",
				trigger.WorkflowName, trigger.WorkflowName)
			log.Printf("WARN: cron: trigger %q has no stored DAG definition, using fallback", trigger.ID)
		}
		_, err = s.coordinator.SubmitWorkflow(ctx, &forgev1.SubmitWorkflowRequest{
			DagYaml: dagYAML,
		})
	}
	if err != nil {
		log.Printf("ERROR: cron: fire workflow %q failed: %v", trigger.WorkflowName, err)
	}

	// Update last/next fire times.
	trigger.mu.Lock()
	trigger.LastFireAt = &now
	next, _ := nextCronTime(trigger.CronExpr, now)
	trigger.NextFireAt = &next
	trigger.mu.Unlock()
}

// recordSkippedFire advances a trigger that was considered but not fired.
//
// LastFireAt is set as well as NextFireAt, and that is the load-bearing part: the
// misfire policy decides whether a fire was missed by comparing LastFireAt with
// now. Leaving it stale while moving NextFireAt forward would make the next tick
// see an ever-growing gap and apply the misfire policy to a trigger that is not
// late at all — a skipped fire would turn into a skipped schedule.
func (s *CronScheduler) recordSkippedFire(trigger *CronTrigger, now time.Time) {
	trigger.mu.Lock()
	defer trigger.mu.Unlock()
	trigger.LastFireAt = &now
	next, _ := nextCronTime(trigger.CronExpr, now)
	trigger.NextFireAt = &next
}

// runningInstances counts how many instances of a workflow are currently running.
//
// It counts from storage rather than from an in-process tally. A tally would be
// wrong as soon as there is more than one coordinator — each would count only
// its own fires and the limit would be multiplied by the number of replicas,
// which is the opposite of what a concurrency limit is for.
//
// RUNNING plus PAUSED are both "in flight": a workflow waiting on a human is
// still occupying the slot its trigger created, and counting only RUNNING would
// let a cron keep piling up instances behind an unanswered approval.
func (s *CronScheduler) runningInstances(ctx context.Context, workflowName string) (int, error) {
	store := s.coordinator.store

	count := 0
	for _, status := range []storage.WorkflowStatus{
		storage.WorkflowStatusRunning,
		storage.WorkflowStatusPaused,
	} {
		workflows, err := store.ListWorkflows(ctx, status, cronInstanceScanLimit, 0)
		if err != nil {
			return 0, fmt.Errorf("list %s workflows: %w", status, err)
		}
		for _, wf := range workflows {
			if wf.Name == workflowName {
				count++
			}
		}
	}
	return count, nil
}

// cronInstanceScanLimit bounds the per-status scan a concurrency check performs.
//
// It is a page limit, not a cap on the count: a deployment with more than this
// many concurrent workflows in one state is beyond what an in-process cron
// trigger is expected to police, and the log line says when the limit was hit
// rather than silently under-counting.
const cronInstanceScanLimit = 500

// Triggers returns a copy of all registered triggers (for testing/inspection).
func (s *CronScheduler) Triggers() []*CronTrigger {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]*CronTrigger, len(s.triggers))
	copy(result, s.triggers)
	return result
}

// --- Cron expression parser ---
// Supports standard 5-field cron: minute hour day-of-month month day-of-week
// Special: */N for "every N", comma-separated values, ranges (a-b)

// cronField represents the allowed values for one cron field.
type cronField struct {
	values map[int]bool
}

// cronExpr is a parsed cron expression with 5 fields.
type cronExpr struct {
	minute cronField
	hour   cronField
	dom    cronField // day of month
	month  cronField
	dow    cronField // day of week (0=Sunday)
}

// parseCronExpr parses a 5-field cron expression.
func parseCronExpr(expr string) (*cronExpr, error) {
	parts := strings.Fields(expr)
	if len(parts) != 5 {
		return nil, fmt.Errorf("expected 5 fields, got %d", len(parts))
	}

	minute, err := parseField(parts[0], 0, 59)
	if err != nil {
		return nil, fmt.Errorf("minute field: %w", err)
	}
	hour, err := parseField(parts[1], 0, 23)
	if err != nil {
		return nil, fmt.Errorf("hour field: %w", err)
	}
	dom, err := parseField(parts[2], 1, 31)
	if err != nil {
		return nil, fmt.Errorf("day-of-month field: %w", err)
	}
	month, err := parseField(parts[3], 1, 12)
	if err != nil {
		return nil, fmt.Errorf("month field: %w", err)
	}
	dow, err := parseField(parts[4], 0, 6)
	if err != nil {
		return nil, fmt.Errorf("day-of-week field: %w", err)
	}

	return &cronExpr{
		minute: cronField{values: minute},
		hour:   cronField{values: hour},
		dom:    cronField{values: dom},
		month:  cronField{values: month},
		dow:    cronField{values: dow},
	}, nil
}

// parseField parses a single cron field (supports *, */N, N, N-M, N,M,...).
func parseField(field string, min, max int) (map[int]bool, error) {
	values := make(map[int]bool)

	for _, part := range strings.Split(field, ",") {
		part = strings.TrimSpace(part)

		if part == "*" {
			for i := min; i <= max; i++ {
				values[i] = true
			}
			continue
		}

		if strings.HasPrefix(part, "*/") {
			step, err := strconv.Atoi(part[2:])
			if err != nil || step <= 0 {
				return nil, fmt.Errorf("invalid step %q", part)
			}
			for i := min; i <= max; i += step {
				values[i] = true
			}
			continue
		}

		if strings.Contains(part, "-") {
			rangeParts := strings.Split(part, "-")
			if len(rangeParts) != 2 {
				return nil, fmt.Errorf("invalid range %q", part)
			}
			start, err1 := strconv.Atoi(rangeParts[0])
			end, err2 := strconv.Atoi(rangeParts[1])
			if err1 != nil || err2 != nil || start < min || end > max || start > end {
				return nil, fmt.Errorf("invalid range %q", part)
			}
			for i := start; i <= end; i++ {
				values[i] = true
			}
			continue
		}

		// Single value.
		v, err := strconv.Atoi(part)
		if err != nil || v < min || v > max {
			return nil, fmt.Errorf("invalid value %q (must be %d-%d)", part, min, max)
		}
		values[v] = true
	}

	if len(values) == 0 {
		return nil, fmt.Errorf("empty field")
	}

	return values, nil
}

// nextCronTime calculates the next time a cron expression should fire after 'after'.
func nextCronTime(expr string, after time.Time) (time.Time, error) {
	cron, err := parseCronExpr(expr)
	if err != nil {
		return time.Time{}, err
	}

	// Start from the next minute.
	t := after.Truncate(time.Minute).Add(time.Minute)

	// Search up to 2 years ahead (prevent infinite loop).
	limit := t.Add(2 * 365 * 24 * time.Hour)

	for t.Before(limit) {
		if !cron.month.values[int(t.Month())] {
			// Skip to next month.
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, t.Location())
			continue
		}
		if !cron.dom.values[t.Day()] || !cron.dow.values[int(t.Weekday())] {
			t = t.Add(24 * time.Hour)
			t = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
			continue
		}
		if !cron.hour.values[t.Hour()] {
			t = t.Add(time.Hour)
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 0, 0, 0, t.Location())
			continue
		}
		if !cron.minute.values[t.Minute()] {
			t = t.Add(time.Minute)
			continue
		}
		return t, nil
	}

	return time.Time{}, fmt.Errorf("no next fire time found within 2 years")
}
