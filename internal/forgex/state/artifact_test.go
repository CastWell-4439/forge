package state

import (
	"testing"

	"github.com/castwell/forge/internal/forgex/model"
)

func TestNewArtifactRecord(t *testing.T) {
	record := NewArtifactRecord("run-1", "reference_image", model.ArtifactMissing, "validator", map[string]string{"reason": "empty"})
	if record.ID == "" || record.RunID != "run-1" || record.Status != model.ArtifactMissing {
		t.Fatalf("unexpected record %+v", record)
	}
	if err := ValidateArtifactRecord(record); err != nil {
		t.Fatalf("expected valid artifact: %v", err)
	}
}

func TestValidateArtifactRecordRejectsMissingType(t *testing.T) {
	record := model.ArtifactRecord{ID: "a1", RunID: "run-1", Status: model.ArtifactMissing}
	if err := ValidateArtifactRecord(record); err == nil {
		t.Fatalf("expected missing type error")
	}
}

func TestSummarize(t *testing.T) {
	ws := &model.WorldState{Version: 3, Entries: []model.StateEntry{{Status: model.StateAccepted}, {Status: model.StateConflicted}}}
	summary := Summarize(ws, []model.ArtifactRecord{{Status: model.ArtifactMissing}, {Status: model.ArtifactValid}})
	if summary.Version != 3 || summary.AcceptedEntries != 1 || summary.ConflictedEntries != 1 || summary.MissingArtifacts != 1 || summary.TotalArtifacts != 2 {
		t.Fatalf("unexpected summary %+v", summary)
	}
}
