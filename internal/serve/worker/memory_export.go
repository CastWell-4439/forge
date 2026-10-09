package worker

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	agentcore "github.com/castwell/forge/internal/agent/core"
)

// Reading the agent plane's memory store, without writing it.
//
// The store is a single JSON file: an array of {doc, embedding} objects, where
// a doc is {id, content, metadata}. This file knows that layout — it is the
// agent plane's storage format — and projects it into the shape the control
// plane's review and verification read.
//
// It is READ-ONLY by construction: the function opens the file with os.ReadFile
// and has no writer. That is not an oversight to be fixed later; a control-plane
// command that could edit memories would cross the boundary the whole design
// keeps, and every decision this store feeds is a proposal so that crossing is
// never necessary.

// DefaultKnowledgeDir is where the agent plane keeps its document store when
// FORGE_KNOWLEDGE_DIR is unset. The control-plane commands take the directory
// as a flag and default to the same path, so an operator running them beside a
// worker needs no configuration — and one running them elsewhere can say where
// to look instead of being guessed at.
const DefaultKnowledgeDir = ".forge/knowledge"

// documentsFileName is the store file inside the knowledge directory.
const documentsFileName = "documents.json"

// storedDocument mirrors the agent plane's on-disk shape.
//
// It is duplicated rather than imported: the two planes do not import each
// other, and the duplication is one struct. A change to the store's layout must
// be reflected here, and the export test pins the shape so that a silent
// divergence fails rather than producing empty memories.
type storedDocument struct {
	Doc struct {
		ID       string            `json:"id"`
		Content  string            `json:"content"`
		Metadata map[string]string `json:"metadata,omitempty"`
	} `json:"doc"`
}

// LoadMemoryExport reads the memory store from a knowledge directory.
//
// A missing directory or file yields an empty export and no error: a deployment
// that has never written a memory is not a failure, and the caller's report
// says "no memories" rather than "could not read".
func LoadMemoryExport(knowledgeDir string) (agentcore.MemoryExport, error) {
	if knowledgeDir == "" {
		knowledgeDir = DefaultKnowledgeDir
	}
	path := filepath.Join(knowledgeDir, documentsFileName)

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return agentcore.MemoryExport{Path: path}, nil
		}
		return agentcore.MemoryExport{Path: path}, fmt.Errorf("read memory store %s: %w", path, err)
	}

	var docs []storedDocument
	if err := json.Unmarshal(data, &docs); err != nil {
		return agentcore.MemoryExport{Path: path}, fmt.Errorf("parse memory store %s: %w", path, err)
	}

	out := agentcore.MemoryExport{Path: path}
	for _, doc := range docs {
		memory, ok := exportDocument(doc)
		if !ok {
			// The store also holds knowledge-base documents. Counting them as
			// skipped keeps "most of this store is reference material" from
			// reading as "memories failed to load".
			out.Skipped++
			continue
		}
		out.Memories = append(out.Memories, memory)
	}
	return out, nil
}

// exportDocument projects one stored document into a memory, reporting whether
// it is one at all.
//
// The discriminator is the category the memory plane writes: "experience".
// Anything else in this store is knowledge the agent retrieved, not something
// it learned, and treating the two alike would put reference documents into a
// lifecycle review.
func exportDocument(doc storedDocument) (agentcore.ExportedMemory, bool) {
	if doc.Doc.ID == "" || doc.Doc.Content == "" {
		return agentcore.ExportedMemory{}, false
	}
	if doc.Doc.Metadata["category"] != memoryCategory {
		return agentcore.ExportedMemory{}, false
	}

	out := agentcore.ExportedMemory{
		ID:       doc.Doc.ID,
		Content:  doc.Doc.Content,
		Category: doc.Doc.Metadata["category"],
		Source:   doc.Doc.Metadata["source"],
	}
	if raw := doc.Doc.Metadata["confidence"]; raw != "" {
		if v, err := strconv.ParseFloat(raw, 64); err == nil {
			out.Confidence = v
		}
	}
	out.ObservedAt = parseStoreTime(doc.Doc.Metadata["observed_at"])
	out.CreatedAt = parseStoreTime(doc.Doc.Metadata["created_at"])
	// An unlabelled memory is episodic, matching the memory plane's default:
	// an observation, not a claim about the world.
	out.Layer = agentcore.NormalizeMemoryLayer(doc.Doc.Metadata["layer"])
	return out, true
}

// memoryCategory is the category the memory plane writes for its own entries.
const memoryCategory = "experience"

// parseStoreTime parses a stored timestamp, returning the zero time on failure.
//
// A malformed timestamp yields zero rather than an error: the memory's content
// is the valuable part, and a bad date should not make it unreadable. The zero
// time then falls back to CreatedAt in ObservedAtOrCreated, which is the same
// fallback the memory plane applies.
func parseStoreTime(raw string) time.Time {
	if raw == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}
	}
	return t
}

// ClaimsFromExport builds the verification input from an export.
//
// This is the step that connects review to verification: the extractor produces
// claims per memory, and verification checks them against the recorded tool
// calls. Doing it here rather than in the CLI keeps the projection with the
// data it projects.
func ClaimsFromExport(export agentcore.MemoryExport) map[string][]agentcore.Assertion {
	out := make(map[string][]agentcore.Assertion, len(export.Memories))
	for _, m := range export.Memories {
		if claims := m.AssertionsFor(); len(claims) > 0 {
			out[m.ID] = claims
		}
	}
	return out
}
