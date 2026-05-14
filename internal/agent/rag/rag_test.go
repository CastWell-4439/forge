package rag

import (
	"context"
	"testing"

	"github.com/castwell/forge/internal/agent/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHybridRetriever_Search(t *testing.T) {
	store := NewInMemoryDocumentStore()
	embedder := &MockEmbedder{Dimension: 1536}
	retriever := NewHybridRetriever(store, embedder)

	ctx := context.Background()

	// Index some documents.
	docs := []core.Document{
		{ID: "1", Content: "FFmpeg video encoding with h264 codec"},
		{ID: "2", Content: "Face swap requires a clear face image"},
		{ID: "3", Content: "TTS text-to-speech synthesis guide"},
	}
	err := retriever.Index(ctx, docs)
	require.NoError(t, err)

	// Search should return results.
	results, err := retriever.Search(ctx, "face", 2)
	require.NoError(t, err)
	assert.NotEmpty(t, results)
	assert.LessOrEqual(t, len(results), 2)
}

func TestHybridRetriever_SearchEmpty(t *testing.T) {
	store := NewInMemoryDocumentStore()
	embedder := &MockEmbedder{Dimension: 1536}
	retriever := NewHybridRetriever(store, embedder)

	results, err := retriever.Search(context.Background(), "anything", 5)
	require.NoError(t, err)
	assert.Empty(t, results)
}

func TestHybridRetriever_Index(t *testing.T) {
	store := NewInMemoryDocumentStore()
	embedder := &MockEmbedder{Dimension: 1536}
	retriever := NewHybridRetriever(store, embedder)

	err := retriever.Index(context.Background(), []core.Document{
		{ID: "doc1", Content: "test document"},
	})
	require.NoError(t, err)
	assert.Len(t, store.docs, 1)
	assert.Equal(t, "doc1", store.docs[0].ID)
}

func TestRRF_Fusion(t *testing.T) {
	listA := []core.Document{
		{ID: "a", Content: "doc a"},
		{ID: "b", Content: "doc b"},
		{ID: "c", Content: "doc c"},
	}
	listB := []core.Document{
		{ID: "b", Content: "doc b"},
		{ID: "d", Content: "doc d"},
		{ID: "a", Content: "doc a"},
	}

	fused := reciprocalRankFusion(listA, listB, 60)

	// "b" appears in both lists at good positions, should rank highest.
	assert.True(t, len(fused) >= 2)
	// a and b should have highest scores since they appear in both lists.
	topIDs := make(map[string]bool)
	for _, doc := range fused[:2] {
		topIDs[doc.ID] = true
	}
	assert.True(t, topIDs["a"] || topIDs["b"], "top results should include docs in both lists")
}

func TestKnowledgeSearchHandler(t *testing.T) {
	store := NewInMemoryDocumentStore()
	embedder := &MockEmbedder{Dimension: 1536}
	retriever := NewHybridRetriever(store, embedder)

	ctx := context.Background()
	_ = retriever.Index(ctx, []core.Document{
		{ID: "1", Content: "video encoding guide"},
	})

	handler := NewKnowledgeSearchHandler(retriever)
	result, err := handler(ctx, map[string]interface{}{
		"query": "video",
	})
	require.NoError(t, err)
	results, ok := result["results"].([]map[string]interface{})
	require.True(t, ok)
	assert.NotEmpty(t, results)
}

func TestKnowledgeSearchHandler_NilRetriever(t *testing.T) {
	handler := NewKnowledgeSearchHandler(nil)
	_, err := handler(context.Background(), map[string]interface{}{
		"query": "test",
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not configured")
}

func TestKnowledgeSearchHandler_MissingQuery(t *testing.T) {
	store := NewInMemoryDocumentStore()
	embedder := &MockEmbedder{Dimension: 1536}
	retriever := NewHybridRetriever(store, embedder)

	handler := NewKnowledgeSearchHandler(retriever)
	_, err := handler(context.Background(), map[string]interface{}{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "missing required param")
}

func TestMockEmbedder(t *testing.T) {
	embedder := &MockEmbedder{Dimension: 768}

	vec, err := embedder.Embed(context.Background(), "test")
	require.NoError(t, err)
	assert.Len(t, vec, 768)

	vecs, err := embedder.EmbedBatch(context.Background(), []string{"a", "b"})
	require.NoError(t, err)
	assert.Len(t, vecs, 2)
	assert.Len(t, vecs[0], 768)
}
