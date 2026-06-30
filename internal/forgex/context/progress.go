package context

import "github.com/castwell/forge/internal/forgex/model"

// ProgressChanged reports whether two ledgers differ in phase, checklist status,
// blockers, decisions, or next actions. It intentionally ignores UpdatedAt.
func ProgressChanged(prev, next model.ProgressLedger) bool {
	if prev.CurrentPhase != next.CurrentPhase {
		return true
	}
	if len(prev.Checklist) != len(next.Checklist) || len(prev.Blockers) != len(next.Blockers) || len(prev.Decisions) != len(next.Decisions) || len(prev.NextActions) != len(next.NextActions) {
		return true
	}
	for i := range prev.Checklist {
		if prev.Checklist[i].ID != next.Checklist[i].ID || prev.Checklist[i].Status != next.Checklist[i].Status || prev.Checklist[i].Evidence != next.Checklist[i].Evidence {
			return true
		}
	}
	return !sameStrings(prev.Blockers, next.Blockers) || !sameStrings(prev.Decisions, next.Decisions) || !sameStrings(prev.NextActions, next.NextActions)
}

func sameStrings(a, b []string) bool {
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
