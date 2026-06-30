package state

import (
	"strings"
	"time"

	"github.com/castwell/forge/internal/forgex/model"
)

// AcceptClaim returns a new WorldState with the claim accepted as the latest
// state entry for its key. Existing entries for the same key are marked stale.
func AcceptClaim(ws model.WorldState, claim model.StateClaim) model.WorldState {
	now := time.Now().UTC()
	if ws.Version == 0 {
		ws.Version = 1
	}
	for i := range ws.Entries {
		if ws.Entries[i].Key == claim.Key && ws.Entries[i].Status == model.StateAccepted {
			ws.Entries[i].Status = model.StateStale
		}
	}
	entryVersion := nextEntryVersion(ws, claim.Key)
	ws.Entries = append(ws.Entries, model.StateEntry{
		Key:        claim.Key,
		Value:      cloneAnyMap(claim.Value),
		Status:     model.StateAccepted,
		Producer:   claim.Producer,
		Confidence: 1.0,
		Evidence:   append([]string(nil), claim.Evidence...),
		Version:    entryVersion,
	})
	ws.Version++
	ws.UpdatedAt = now
	if ws.RunID == "" {
		ws.RunID = claim.RunID
	}
	return ws
}

// RejectClaim marks a claim as rejected with a reason.
func RejectClaim(claim model.StateClaim, reason string) model.StateClaim {
	claim.Status = model.StateRejected
	claim.Reason = strings.TrimSpace(reason)
	return claim
}

// MarkConflict marks matching entries as conflicted, appending a conflict reason
// to the entry evidence. If no entry exists, it creates a conflicted placeholder.
func MarkConflict(ws model.WorldState, key string, reason string) model.WorldState {
	now := time.Now().UTC()
	key = strings.TrimSpace(key)
	matched := false
	for i := range ws.Entries {
		if ws.Entries[i].Key == key {
			ws.Entries[i].Status = model.StateConflicted
			if reason != "" {
				ws.Entries[i].Evidence = append(ws.Entries[i].Evidence, reason)
			}
			matched = true
		}
	}
	if !matched {
		ws.Entries = append(ws.Entries, model.StateEntry{
			Key:      key,
			Value:    map[string]any{"reason": reason},
			Status:   model.StateConflicted,
			Producer: "forgex.state",
			Evidence: []string{reason},
			Version:  1,
		})
	}
	if ws.Version == 0 {
		ws.Version = 1
	}
	ws.Version++
	ws.UpdatedAt = now
	return ws
}

// NewClaim creates a proposed claim with the current UTC timestamp.
func NewClaim(runID, id, key, producer string, value map[string]any, evidence []string) model.StateClaim {
	return model.StateClaim{
		ID:        id,
		RunID:     runID,
		Key:       strings.TrimSpace(key),
		Value:     cloneAnyMap(value),
		Producer:  strings.TrimSpace(producer),
		Evidence:  append([]string(nil), evidence...),
		Status:    model.StateProposed,
		CreatedAt: time.Now().UTC(),
	}
}

func nextEntryVersion(ws model.WorldState, key string) int {
	maxVersion := 0
	for _, entry := range ws.Entries {
		if entry.Key == key && entry.Version > maxVersion {
			maxVersion = entry.Version
		}
	}
	return maxVersion + 1
}

func cloneAnyMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
