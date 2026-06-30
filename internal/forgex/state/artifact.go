package state

import (
	"fmt"
	"strings"
	"time"

	"github.com/castwell/forge/internal/forgex/model"
	"github.com/google/uuid"
)

// NewArtifactRecord builds one artifact index record.
func NewArtifactRecord(runID, artifactType string, status model.ArtifactStatus, producer string, metadata map[string]string) model.ArtifactRecord {
	return model.ArtifactRecord{
		ID:        "artifact_" + uuid.NewString(),
		RunID:     runID,
		Type:      strings.TrimSpace(artifactType),
		Status:    status,
		Producer:  strings.TrimSpace(producer),
		Metadata:  cloneStringMap(metadata),
		CreatedAt: time.Now().UTC(),
	}
}

// SummarizeArtifacts counts artifact statuses for reports and inspections.
func SummarizeArtifacts(artifacts []model.ArtifactRecord) map[model.ArtifactStatus]int {
	counts := make(map[model.ArtifactStatus]int)
	for _, artifact := range artifacts {
		counts[artifact.Status]++
	}
	return counts
}

// ValidateArtifactRecord returns an error when the record is missing required fields.
func ValidateArtifactRecord(record model.ArtifactRecord) error {
	if strings.TrimSpace(record.RunID) == "" {
		return fmt.Errorf("artifact run_id is required")
	}
	if strings.TrimSpace(record.ID) == "" {
		return fmt.Errorf("artifact id is required")
	}
	if strings.TrimSpace(record.Type) == "" {
		return fmt.Errorf("artifact type is required")
	}
	switch record.Status {
	case model.ArtifactRequired, model.ArtifactProduced, model.ArtifactMissing, model.ArtifactValid, model.ArtifactInvalid:
		return nil
	default:
		return fmt.Errorf("unknown artifact status %q", record.Status)
	}
}

func cloneStringMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
