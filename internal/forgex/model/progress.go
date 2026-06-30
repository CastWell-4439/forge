package model

import "time"

// ProgressStatus describes one checklist item's current state.
type ProgressStatus string

const (
	ProgressTodo       ProgressStatus = "todo"
	ProgressInProgress ProgressStatus = "in_progress"
	ProgressDone       ProgressStatus = "done"
	ProgressBlocked    ProgressStatus = "blocked"
	ProgressFailed     ProgressStatus = "failed"
)

// ProgressItem is one auditable step in a long-running agent task.
type ProgressItem struct {
	ID       string         `json:"id" yaml:"id"`
	Title    string         `json:"title" yaml:"title"`
	Status   ProgressStatus `json:"status" yaml:"status"`
	Evidence string         `json:"evidence,omitempty" yaml:"evidence,omitempty"`
}

// ProgressLedger is the compact execution plan/progress state for a run.
type ProgressLedger struct {
	RunID        string         `json:"run_id" yaml:"run_id"`
	CurrentPhase string         `json:"current_phase" yaml:"current_phase"`
	Checklist    []ProgressItem `json:"checklist" yaml:"checklist"`
	Blockers     []string       `json:"blockers,omitempty" yaml:"blockers,omitempty"`
	Decisions    []string       `json:"decisions,omitempty" yaml:"decisions,omitempty"`
	NextActions  []string       `json:"next_actions,omitempty" yaml:"next_actions,omitempty"`
	UpdatedAt    time.Time      `json:"updated_at" yaml:"updated_at"`
}

// CompletionRatio returns done items divided by all checklist items.
func (p ProgressLedger) CompletionRatio() float64 {
	if len(p.Checklist) == 0 {
		return 0
	}
	done := 0
	for _, item := range p.Checklist {
		if item.Status == ProgressDone {
			done++
		}
	}
	return float64(done) / float64(len(p.Checklist))
}
