package memory

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/castwell/forge/internal/agent/core"
)

// The governance fields must survive a round trip.
//
// They used to be written on save and dropped on read: Search rebuilt only id,
// content and category, so a memory's source, confidence, observation time and
// layer were lost the moment it was recalled. Every cross-run judgement about
// it — how many independent sources agree, whether it is stale, which layer it
// belongs to — then had no data, which made the evidence aggregation work on
// nothing.
func TestMetadataRoundTrip(t *testing.T) {
	observed := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	entry := core.MemoryEntry{
		ID:         "mem-1",
		Content:    "the project is written in go",
		Category:   "experience",
		Source:     core.MemorySource("run:abc"),
		Confidence: 0.8,
		ObservedAt: observed,
		CreatedAt:  observed,
		Layer:      core.LayerFact,
	}

	meta := memoryMetadata(entry)
	back := memoryEntryFromDoc(core.Document{
		ID: entry.ID, Content: entry.Content, Metadata: meta,
	})

	assert.Equal(t, entry.ID, back.ID)
	assert.Equal(t, entry.Content, back.Content)
	assert.Equal(t, entry.Category, back.Category)
	assert.Equal(t, entry.Source, back.Source, "the source survives")
	assert.InDelta(t, entry.Confidence, back.Confidence, 1e-9, "the confidence survives")
	assert.Equal(t, entry.Layer, back.Layer, "the layer survives")
	assert.True(t, back.ObservedAt.Equal(observed), "the observation time survives")
	assert.True(t, back.CreatedAt.Equal(observed))
}

// A zero field is written as an ABSENT key, so a reader has one case to handle
// (missing) rather than two (missing and zero-valued).
func TestZeroFieldsAreAbsent(t *testing.T) {
	meta := memoryMetadata(core.MemoryEntry{ID: "m", Content: "c", Category: "experience"})

	assert.NotContains(t, meta, "source")
	assert.NotContains(t, meta, "confidence")
	assert.NotContains(t, meta, "observed_at")
	assert.NotContains(t, meta, "layer")
	assert.Contains(t, meta, "category", "but the fields the store always had are present")
	assert.Contains(t, meta, "created_at")
}

// A document written before these fields existed still reads: it simply has no
// metadata beyond what it always had.
func TestOldDocumentStillReads(t *testing.T) {
	doc := core.Document{
		ID:      "old-1",
		Content: "an older memory",
		Metadata: map[string]string{
			"category":   "experience",
			"created_at": "2026-01-01T00:00:00Z",
		},
	}

	entry := memoryEntryFromDoc(doc)
	assert.Equal(t, "old-1", entry.ID)
	assert.Equal(t, "an older memory", entry.Content)
	assert.Equal(t, "experience", entry.Category)
	assert.Empty(t, entry.Source, "an absent source is empty, not an error")
	assert.Zero(t, entry.Confidence, "and reads as unset")
	// An unlabelled layer reads as episodic: an observation, not a claim.
	assert.Equal(t, core.LayerEpisodic, entry.Layer)
}

// A document with no metadata at all still yields its content.
func TestDocumentWithoutMetadata(t *testing.T) {
	entry := memoryEntryFromDoc(core.Document{ID: "m", Content: "c"})
	assert.Equal(t, "m", entry.ID)
	assert.Equal(t, "c", entry.Content)
	assert.Equal(t, core.LayerEpisodic, entry.Layer)
}

// A malformed value must not make the memory unreadable: the content is the
// valuable part, and the metadata only helps judge it.
func TestMalformedMetadataDoesNotBreakTheMemory(t *testing.T) {
	doc := core.Document{
		ID:      "m",
		Content: "still readable",
		Metadata: map[string]string{
			"category":    "experience",
			"confidence":  "not a number",
			"observed_at": "not a time",
			"created_at":  "also not a time",
		},
	}

	entry := memoryEntryFromDoc(doc)
	assert.Equal(t, "still readable", entry.Content)
	assert.Zero(t, entry.Confidence, "a bad number reads as unset")
	assert.True(t, entry.ObservedAt.IsZero())
	assert.True(t, entry.CreatedAt.IsZero())
}

// The in-memory implementation keeps the fields as given, so tests of the
// aggregation see the same values the file store would return.
func TestInMemoryKeepsMetadata(t *testing.T) {
	store := NewInMemoryLongTerm()
	entry := core.MemoryEntry{
		ID: "m", Content: "written in go", Category: "experience",
		Source: core.MemorySource("human:alice"), Confidence: 0.9, Layer: core.LayerFact,
	}
	require.NoError(t, store.Save(context.Background(), entry))

	found, err := store.Search(context.Background(), "written", 5)
	require.NoError(t, err)
	require.Len(t, found, 1)
	assert.Equal(t, core.MemorySource("human:alice"), found[0].Source)
	assert.InDelta(t, 0.9, found[0].Confidence, 1e-9)
	assert.Equal(t, core.LayerFact, found[0].Layer)
}
