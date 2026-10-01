package rag

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/castwell/forge/internal/agent/core"
)

// --- ranking primitives ---

func TestTokenizeSplitsCJKPerCharacter(t *testing.T) {
	got := tokenize("状态机恢复")
	want := []string{"状", "态", "机", "恢", "复"}
	if len(got) != len(want) {
		t.Fatalf("tokens = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tokens = %v, want %v", got, want)
		}
	}
}

func TestBM25RanksByTermFrequency(t *testing.T) {
	docs := []core.Document{
		{ID: "rare", Content: "release notes for version one"},
		{ID: "noisy", Content: "release release release process for the release pipeline"},
		{ID: "unrelated", Content: "banana bread recipe"},
	}
	ranked := bm25Rank(docs, "release")
	if len(ranked) != 2 {
		t.Fatalf("ranked = %d docs, want only the matching ones (zero-score docs are dropped)", len(ranked))
	}
	if ranked[0].ID != "noisy" {
		t.Errorf("top = %q, want the higher term-frequency doc", ranked[0].ID)
	}
	if ranked[1].ID != "rare" {
		t.Errorf("second = %q, want the single-mention doc", ranked[1].ID)
	}
	if ranked[0].Score <= ranked[1].Score {
		t.Errorf("scores should order the docs: %f <= %f", ranked[0].Score, ranked[1].Score)
	}
}

func TestCosineSimilarity(t *testing.T) {
	same := cosine([]float32{1, 0}, []float32{1, 0})
	if same < 0.999 {
		t.Errorf("identical vectors = %f, want ~1", same)
	}
	orth := cosine([]float32{1, 0}, []float32{0, 1})
	if orth != 0 {
		t.Errorf("orthogonal vectors = %f, want 0", orth)
	}
	// A zero vector (the old MockEmbedder output) must not produce a fake hit.
	if got := cosine([]float32{0, 0}, []float32{1, 1}); got != 0 {
		t.Errorf("zero vector = %f, want 0", got)
	}
	if got := cosine([]float32{1, 0}, []float32{1, 0, 0}); got != 0 {
		t.Errorf("mismatched lengths = %f, want 0", got)
	}
}

func TestMockEmbedderRanksRelatedTextsHigher(t *testing.T) {
	m := &MockEmbedder{}
	ctx := context.Background()
	q, err := m.Embed(ctx, "how to release the forge build")
	if err != nil {
		t.Fatal(err)
	}
	related, _ := m.Embed(ctx, "release checklist for the build")
	unrelated, _ := m.Embed(ctx, "banana bread baking time")

	if cosine(q, related) <= cosine(q, unrelated) {
		t.Errorf("related (%f) should outrank unrelated (%f)",
			cosine(q, related), cosine(q, unrelated))
	}
	// Identical text scores 1, not 0: the old zero-vector mock made everything 0.
	self, _ := m.Embed(ctx, "how to release the forge build")
	if cosine(q, self) < 0.999 {
		t.Errorf("identical text cosine = %f, want ~1", cosine(q, self))
	}
}

// --- file store ---

func TestFileStorePersistsAndRanks(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	m := &MockEmbedder{}

	store := NewFileDocumentStore(root)
	docs := []core.Document{
		{ID: "a", Content: "release checklist"},
		{ID: "b", Content: "release checklist and approval steps"},
		{ID: "c", Content: "banana bread"},
	}
	for _, d := range docs {
		emb, _ := m.Embed(ctx, d.Content)
		if err := store.Upsert(ctx, d, emb); err != nil {
			t.Fatalf("upsert %s: %v", d.ID, err)
		}
	}

	// A new instance reads the same store back from disk.
	fresh := NewFileDocumentStore(root)
	q, _ := m.Embed(ctx, "release process")
	hits, err := fresh.VectorSearch(ctx, q, 10)
	if err != nil {
		t.Fatalf("vector search: %v", err)
	}
	if len(hits) != 3 {
		t.Fatalf("hits = %d, want 3 (all docs persisted with embeddings)", len(hits))
	}
	// The two release docs must outrank the banana doc after a restart.
	if hits[2].ID != "c" {
		t.Errorf("third hit = %q, want the unrelated doc last", hits[2].ID)
	}
	if hits[0].ID == "c" {
		t.Errorf("unrelated doc ranked first: cosine is not being computed")
	}

	bm25, err := fresh.BM25Search(ctx, "release", 10)
	if err != nil {
		t.Fatalf("bm25: %v", err)
	}
	if len(bm25) != 2 {
		t.Errorf("bm25 hits = %d, want 2", len(bm25))
	}
}

func TestFileStoreSkipsDocsWithoutEmbeddings(t *testing.T) {
	ctx := context.Background()
	store := NewFileDocumentStore(t.TempDir())
	m := &MockEmbedder{}

	withVec, _ := m.Embed(ctx, "indexed with vector")
	if err := store.Upsert(ctx, core.Document{ID: "vec", Content: "indexed with vector"}, withVec); err != nil {
		t.Fatal(err)
	}
	// Indexed while the endpoint was down: BM25 must still find it.
	if err := store.Upsert(ctx, core.Document{ID: "novec", Content: "release notes without vector"}, nil); err != nil {
		t.Fatal(err)
	}

	q, _ := m.Embed(ctx, "release notes")
	vecHits, err := store.VectorSearch(ctx, q, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range vecHits {
		if h.ID == "novec" {
			t.Error("vector search returned a document it has no vector for")
		}
	}
	bm25Hits, err := store.BM25Search(ctx, "release notes", 10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, h := range bm25Hits {
		if h.ID == "novec" {
			found = true
		}
	}
	if !found {
		t.Error("BM25 should find the doc indexed without an embedding")
	}
}

// --- pipeline: degrade, towers, rerank ---

type failingEmbedder struct{}

func (failingEmbedder) Embed(_ context.Context, _ string) ([]float32, error) {
	return nil, errors.New("embedding endpoint unreachable")
}

func (failingEmbedder) EmbedBatch(_ context.Context, _ []string) ([][]float32, error) {
	return nil, errors.New("embedding endpoint unreachable")
}

func seedStore(t *testing.T) (DocumentStore, *MockEmbedder) {
	t.Helper()
	ctx := context.Background()
	m := &MockEmbedder{}
	store := NewFileDocumentStore(t.TempDir())
	for i, content := range []string{
		"release checklist",
		"banana bread",
	} {
		emb, _ := m.Embed(ctx, content)
		if err := store.Upsert(ctx, core.Document{ID: string(rune('a' + i)), Content: content}, emb); err != nil {
			t.Fatal(err)
		}
	}
	return store, m
}

func TestHybridDegradesWithoutEmbedder(t *testing.T) {
	store, _ := seedStore(t)
	r := NewHybridRetriever(store, nil)

	hits, err := r.Search(context.Background(), "release", 5)
	if err != nil {
		t.Fatalf("search must not fail without an embedder: %v", err)
	}
	if len(hits) == 0 || hits[0].ID != "a" {
		t.Errorf("hits = %+v, want the matching doc first", hits)
	}
	if got := r.SearchMode(); got != "bm25-only:no-embedder" {
		t.Errorf("mode = %q", got)
	}
}

func TestHybridDegradesWhenEmbeddingFails(t *testing.T) {
	store, _ := seedStore(t)
	r := NewHybridRetriever(store, failingEmbedder{})

	hits, err := r.Search(context.Background(), "release", 5)
	if err != nil {
		t.Fatalf("search must not fail when embedding fails: %v", err)
	}
	if len(hits) == 0 || hits[0].ID != "a" {
		t.Errorf("hits = %+v, want BM25 answers", hits)
	}
	if got := r.SearchMode(); got != "bm25-only:embed-failed" {
		t.Errorf("mode = %q", got)
	}
}

type towerEmbedder struct {
	queryCalls, docCalls int
}

func (e *towerEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	// Plain embed should not be used while tower methods exist.
	return hashEmbed(text), nil
}
func (e *towerEmbedder) EmbedBatch(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, s := range texts {
		out[i] = hashEmbed(s)
	}
	return out, nil
}
func (e *towerEmbedder) EmbedQuery(_ context.Context, _ string) ([]float32, error) {
	e.queryCalls++
	return hashEmbed("query tower: release"), nil
}
func (e *towerEmbedder) EmbedDoc(_ context.Context, text string) ([]float32, error) {
	e.docCalls++
	return hashEmbed(text), nil
}

func hashEmbed(text string) []float32 {
	m := &MockEmbedder{}
	v, _ := m.Embed(context.Background(), text)
	return v
}

func TestHybridUsesSeparateTowers(t *testing.T) {
	ctx := context.Background()
	store := NewFileDocumentStore(t.TempDir())
	tower := &towerEmbedder{}

	// Index through the tower...
	r := NewHybridRetriever(store, tower)
	if err := r.Index(ctx, []core.Document{{ID: "d", Content: "release checklist"}}); err != nil {
		t.Fatal(err)
	}
	if tower.docCalls != 1 || tower.queryCalls != 0 {
		t.Fatalf("after index: query=%d doc=%d, want doc tower used", tower.queryCalls, tower.docCalls)
	}
	// ...and search through it.
	if _, err := r.Search(ctx, "release", 5); err != nil {
		t.Fatal(err)
	}
	if tower.queryCalls != 1 {
		t.Fatalf("after search: query=%d, want query tower used (not plain Embed)", tower.queryCalls)
	}
}

type fakeReranker struct {
	fail bool
}

func (f fakeReranker) Rerank(_ context.Context, _ string, docs []core.Document, topK int) ([]core.Document, error) {
	if f.fail {
		return nil, errors.New("rerank endpoint down")
	}
	// Deliberately invert the recall order so a pass is visible.
	out := make([]core.Document, 0, len(docs))
	for i := len(docs) - 1; i >= 0; i-- {
		out = append(out, docs[i])
	}
	if topK > 0 && len(out) > topK {
		out = out[:topK]
	}
	return out, nil
}

func TestHybridRerankParticipatesAndIsExplainable(t *testing.T) {
	store, m := seedStore(t)
	_ = m
	r := NewHybridRetriever(store, m).WithReranker(fakeReranker{})

	hits, err := r.Search(context.Background(), "release", 5)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.SearchMode(); got != "hybrid+rerank" {
		t.Errorf("mode = %q, want hybrid+rerank", got)
	}
	// The fake inverts the recall order, so the unrelated doc comes first now;
	// what matters is that the reversal happened and is traceable.
	if hits[0].Metadata["rrf_score"] == "" {
		t.Errorf("reranked doc keeps no rrf_score trace: %+v", hits[0].Metadata)
	}
	// And the store must not be polluted by the trace.
	fresh := NewFileDocumentStore(store.(*FileDocumentStore).root)
	bare, err := fresh.BM25Search(context.Background(), "release", 5)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range bare {
		if _, ok := d.Metadata["rrf_score"]; ok {
			t.Errorf("rrf_score leaked into the store: %+v", d.Metadata)
		}
	}
}

func TestHybridRerankFailureFallsBackToRecallOrder(t *testing.T) {
	store, m := seedStore(t)
	r := NewHybridRetriever(store, m).WithReranker(fakeReranker{fail: true})

	hits, err := r.Search(context.Background(), "release", 5)
	if err != nil {
		t.Fatalf("a failing reranker must not fail the search: %v", err)
	}
	if got := r.SearchMode(); got != "hybrid~rerank-failed" {
		t.Errorf("mode = %q, want the failed attempt recorded", got)
	}
	if len(hits) == 0 || hits[0].ID != "a" {
		t.Errorf("hits = %+v, want fusion order preserved", hits)
	}
}

func TestHTTPRerankerRoundTrip(t *testing.T) {
	var gotReq rerankRequest
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		gotAuth = req.Header.Get("Authorization")
		_ = json.NewDecoder(req.Body).Decode(&gotReq)
		_ = json.NewEncoder(w).Encode(rerankResponse{Results: []rerankResult{
			{Index: 1, RelevanceScore: 0.9},
			{Index: 0, RelevanceScore: 0.2},
		}})
	}))
	defer srv.Close()

	h := &HTTPReranker{Endpoint: srv.URL, APIKey: "k", Model: "m"}
	docs := []core.Document{
		{ID: "first", Content: "doc-one"},
		{ID: "second", Content: "doc-two"},
	}
	out, err := h.Rerank(context.Background(), "q", docs, 5)
	if err != nil {
		t.Fatal(err)
	}
	if gotReq.Query != "q" || len(gotReq.Documents) != 2 || gotReq.Documents[0] != "doc-one" {
		t.Errorf("request body = %+v", gotReq)
	}
	if gotAuth != "Bearer k" {
		t.Errorf("auth header = %q", gotAuth)
	}
	if out[0].ID != "second" {
		t.Errorf("order = %s,%s; endpoint scores should be applied", out[0].ID, out[1].ID)
	}
}

func TestApplyRerankScoresKeepsUnscoredDocsLast(t *testing.T) {
	docs := []core.Document{
		{ID: "keep-a", Score: 0.3},
		{ID: "dropped", Score: 0.2},
		{ID: "keep-b", Score: 0.1},
	}
	// Endpoint only scored index 0; others must stay behind it in recall order.
	out := applyRerankScores(docs, []rerankResult{{Index: 0, RelevanceScore: 0.9}}, 10)
	want := []string{"keep-a", "dropped", "keep-b"}
	for i, id := range want {
		if out[i].ID != id {
			t.Fatalf("order = %v, want %v", outIDs(out), want)
		}
	}
	// topK cuts.
	short := applyRerankScores(docs, []rerankResult{{Index: 0, RelevanceScore: 0.9}}, 1)
	if len(short) != 1 {
		t.Errorf("topK ignored: %d docs", len(short))
	}
}

func outIDs(docs []core.Document) []string {
	var ids []string
	for _, d := range docs {
		ids = append(ids, d.ID)
	}
	return ids
}
