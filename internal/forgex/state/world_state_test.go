package state

import (
	"testing"

	"github.com/castwell/forge/internal/forgex/model"
)

func TestAcceptClaimAddsAcceptedEntryAndStalesPrevious(t *testing.T) {
	claim1 := NewClaim("run-1", "claim-1", "reference_images.status", "validator", map[string]any{"status": "present"}, []string{"first"})
	ws := AcceptClaim(model.WorldState{RunID: "run-1"}, claim1)
	claim2 := NewClaim("run-1", "claim-2", "reference_images.status", "validator", map[string]any{"status": "missing"}, []string{"second"})
	ws = AcceptClaim(ws, claim2)
	if len(ws.Entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(ws.Entries))
	}
	if ws.Entries[0].Status != model.StateStale {
		t.Fatalf("expected first entry stale, got %s", ws.Entries[0].Status)
	}
	if ws.Entries[1].Status != model.StateAccepted || ws.Entries[1].Version != 2 {
		t.Fatalf("expected second accepted v2, got %+v", ws.Entries[1])
	}
}

func TestRejectClaim(t *testing.T) {
	claim := NewClaim("run-1", "claim-1", "key", "producer", nil, nil)
	claim = RejectClaim(claim, "bad evidence")
	if claim.Status != model.StateRejected || claim.Reason != "bad evidence" {
		t.Fatalf("unexpected rejected claim %+v", claim)
	}
}

func TestMarkConflictCreatesPlaceholder(t *testing.T) {
	ws := MarkConflict(model.WorldState{RunID: "run-1"}, "key", "conflict")
	if len(ws.Entries) != 1 || ws.Entries[0].Status != model.StateConflicted {
		t.Fatalf("expected conflicted placeholder, got %+v", ws.Entries)
	}
}
