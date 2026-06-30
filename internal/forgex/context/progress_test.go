package context

import (
	"testing"
	"time"

	"github.com/castwell/forge/internal/forgex/model"
)

func TestProgressChangedIgnoresUpdatedAt(t *testing.T) {
	prev := model.ProgressLedger{RunID: "run", CurrentPhase: "phase", Checklist: []model.ProgressItem{{ID: "a", Status: model.ProgressTodo}}, UpdatedAt: time.Now()}
	next := prev
	next.UpdatedAt = prev.UpdatedAt.Add(time.Hour)
	if ProgressChanged(prev, next) {
		t.Fatalf("ProgressChanged() = true, want false when only UpdatedAt changes")
	}
}

func TestProgressChangedDetectsStatusChange(t *testing.T) {
	prev := model.ProgressLedger{RunID: "run", CurrentPhase: "phase", Checklist: []model.ProgressItem{{ID: "a", Status: model.ProgressTodo}}}
	next := model.ProgressLedger{RunID: "run", CurrentPhase: "phase", Checklist: []model.ProgressItem{{ID: "a", Status: model.ProgressDone}}}
	if !ProgressChanged(prev, next) {
		t.Fatalf("ProgressChanged() = false, want true")
	}
}
