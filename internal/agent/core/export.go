package core

import (
	"sort"
	"time"
)

// Memory export: the control plane reading the agent plane's memory store.
//
// The two planes do not import each other (see the lifecycle's observation
// sink), which is why review and verification could describe memories but not
// read them. This file defines the shape of that read, and it is deliberately
// one-directional:
//
//	the agent plane WRITES the store;
//	the control plane READS a copy of the file.
//
// Nothing here writes. A control-plane command that could edit the memory store
// would cross the same boundary the whole design keeps, and the lifecycle's
// decisions are proposals precisely so that crossing never has to happen.
//
// The read is a plain file parse rather than a shared database, for the reason
// verification reads tool_calls.jsonl: the file is the record, and a second
// access path would be infrastructure with no consumer beyond this one.

// ExportedMemory is one memory as the control plane sees it.
//
// It mirrors MemoryEntry rather than reusing it, because the two planes must
// stay free to evolve their own types; the projection function is the single
// place that knows how they correspond.
type ExportedMemory struct {
	ID         string
	Content    string
	Category   string
	Source     string
	Confidence float64
	ObservedAt time.Time
	CreatedAt  time.Time
	Layer      MemoryLayer
}

// MemoryExport is a read of the agent plane's memory store.
type MemoryExport struct {
	Memories []ExportedMemory
	// Path is the file that was read, so a report can name its source.
	Path string
	// Skipped counts documents that could not be read as memories. A store also
	// holds knowledge-base documents, which are not memories; counting them
	// separately keeps "the store is mostly knowledge" from looking like
	// "memories failed to load".
	Skipped int
}

// SortedMemories returns the memories newest first, then by id, so two exports
// of one store agree.
func (e MemoryExport) SortedMemories() []ExportedMemory {
	out := append([]ExportedMemory(nil), e.Memories...)
	sort.SliceStable(out, func(i, j int) bool {
		oi, oj := out[i].ObservedAtOrCreated(), out[j].ObservedAtOrCreated()
		if !oi.Equal(oj) {
			return oi.After(oj)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// ObservedAtOrCreated is when the fact was true, falling back to when it was
// written — the same fallback the memory plane applies, so both agree about how
// old a memory is.
func (m ExportedMemory) ObservedAtOrCreated() time.Time {
	if !m.ObservedAt.IsZero() {
		return m.ObservedAt
	}
	return m.CreatedAt
}

// AssertionsFor runs the static extraction over an exported memory's content.
//
// It exists so the control plane can build a claims map for verification
// without a second implementation of "what does this memory claim". The layer
// is not consulted: a fact and an episodic entry are extracted the same way,
// and what differs is how the review routes them.
func (m ExportedMemory) AssertionsFor() []Assertion {
	return ExtractAssertionsStatic(m.Content)
}
