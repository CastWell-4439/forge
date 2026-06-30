package state

import "github.com/castwell/forge/internal/forgex/model"

// Summary is a compact view used by CLI/report rendering.
type Summary struct {
	Version           int
	AcceptedEntries   int
	ProposedEntries   int
	RejectedEntries   int
	ConflictedEntries int
	StaleEntries      int
	MissingArtifacts  int
	TotalArtifacts    int
}

// Summarize returns counts for world state entries and artifacts.
func Summarize(ws *model.WorldState, artifacts []model.ArtifactRecord) Summary {
	var summary Summary
	if ws != nil {
		summary.Version = ws.Version
		for _, entry := range ws.Entries {
			switch entry.Status {
			case model.StateAccepted:
				summary.AcceptedEntries++
			case model.StateProposed:
				summary.ProposedEntries++
			case model.StateRejected:
				summary.RejectedEntries++
			case model.StateConflicted:
				summary.ConflictedEntries++
			case model.StateStale:
				summary.StaleEntries++
			}
		}
	}
	summary.TotalArtifacts = len(artifacts)
	for _, artifact := range artifacts {
		if artifact.Status == model.ArtifactMissing {
			summary.MissingArtifacts++
		}
	}
	return summary
}
