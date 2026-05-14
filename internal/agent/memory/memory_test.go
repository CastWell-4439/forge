package memory

import (
	"context"
	"testing"
	"time"

	"github.com/castwell/forge/internal/agent/core"
	"github.com/castwell/forge/internal/agent/rag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- ShortTerm tests ---

func TestInMemoryShortTerm_SaveAndGet(t *testing.T) {
	mem := NewInMemoryShortTerm(time.Hour)
	ctx := context.Background()

	err := mem.Save(ctx, "sess1", "video_path", "/tmp/video.mp4")
	require.NoError(t, err)

	val, err := mem.Get(ctx, "sess1", "video_path")
	require.NoError(t, err)
	assert.Equal(t, "/tmp/video.mp4", val)
}

func TestInMemoryShortTerm_GetMissing(t *testing.T) {
	mem := NewInMemoryShortTerm(time.Hour)
	val, err := mem.Get(context.Background(), "nope", "key")
	require.NoError(t, err)
	assert.Nil(t, val)
}

func TestInMemoryShortTerm_GetAll(t *testing.T) {
	mem := NewInMemoryShortTerm(time.Hour)
	ctx := context.Background()

	_ = mem.Save(ctx, "s1", "a", 1)
	_ = mem.Save(ctx, "s1", "b", "two")

	all, err := mem.GetAll(ctx, "s1")
	require.NoError(t, err)
	assert.Len(t, all, 2)
	assert.Equal(t, 1, all["a"])
	assert.Equal(t, "two", all["b"])
}

func TestInMemoryShortTerm_GetAllEmpty(t *testing.T) {
	mem := NewInMemoryShortTerm(time.Hour)
	all, err := mem.GetAll(context.Background(), "nope")
	require.NoError(t, err)
	assert.Nil(t, all)
}

// --- LongTerm tests ---

func TestLongTermMemory_SaveAndSearch(t *testing.T) {
	store := rag.NewInMemoryDocumentStore()
	embedder := &rag.MockEmbedder{Dimension: 1536}
	ltm := NewLongTermMemory(store, embedder)
	ctx := context.Background()

	err := ltm.Save(ctx, core.MemoryEntry{
		Content:  "Face swap failed because resolution was too high. Downscale first.",
		Category: "experience",
	})
	require.NoError(t, err)

	err = ltm.Save(ctx, core.MemoryEntry{
		Content:  "TTS works best with short sentences under 200 chars.",
		Category: "experience",
	})
	require.NoError(t, err)

	results, err := ltm.Search(ctx, "face", 5)
	require.NoError(t, err)
	// MockEmbedder returns zero vectors so VectorSearch returns all docs.
	assert.NotEmpty(t, results)
}

// --- InMemoryLongTerm tests ---

func TestInMemoryLongTerm_SaveAndSearch(t *testing.T) {
	mem := NewInMemoryLongTerm()
	ctx := context.Background()

	_ = mem.Save(ctx, core.MemoryEntry{Content: "video encoding trick", Category: "tip"})
	_ = mem.Save(ctx, core.MemoryEntry{Content: "face swap lesson", Category: "experience"})

	results, err := mem.Search(ctx, "face", 5)
	require.NoError(t, err)
	assert.Len(t, results, 1)
	assert.Contains(t, results[0].Content, "face")
}

func TestInMemoryLongTerm_SearchEmpty(t *testing.T) {
	mem := NewInMemoryLongTerm()
	results, err := mem.Search(context.Background(), "anything", 5)
	require.NoError(t, err)
	assert.Empty(t, results)
}

// --- Store (unified adapter) tests ---

func TestStore_Unified(t *testing.T) {
	short := NewInMemoryShortTerm(time.Hour)
	store := rag.NewInMemoryDocumentStore()
	embedder := &rag.MockEmbedder{Dimension: 1536}
	long := NewLongTermMemory(store, embedder)

	unified := NewStore(short, long)
	ctx := context.Background()

	// Short-term.
	err := unified.SaveShortTerm(ctx, "s1", "key", "value")
	require.NoError(t, err)
	val, err := unified.GetShortTerm(ctx, "s1", "key")
	require.NoError(t, err)
	assert.Equal(t, "value", val)

	// Long-term.
	err = unified.SaveLongTerm(ctx, core.MemoryEntry{Content: "test memory", Category: "test"})
	require.NoError(t, err)
	results, err := unified.SearchLongTerm(ctx, "test", 5)
	require.NoError(t, err)
	assert.NotEmpty(t, results)
}

func TestStore_NilModules(t *testing.T) {
	unified := NewStore(nil, nil)
	ctx := context.Background()

	// Should not panic.
	err := unified.SaveShortTerm(ctx, "s", "k", "v")
	assert.NoError(t, err)
	val, err := unified.GetShortTerm(ctx, "s", "k")
	assert.NoError(t, err)
	assert.Nil(t, val)
	err = unified.SaveLongTerm(ctx, core.MemoryEntry{Content: "x"})
	assert.NoError(t, err)
	results, err := unified.SearchLongTerm(ctx, "x", 5)
	assert.NoError(t, err)
	assert.Nil(t, results)
}
