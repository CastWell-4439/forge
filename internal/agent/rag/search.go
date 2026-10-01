package rag

import (
	"math"
	"sort"
	"strings"

	"github.com/castwell/forge/internal/agent/core"
)

// The ranking primitives shared by every store implementation: one tokenizer,
// one BM25 scorer, one cosine. The algorithms live here so an in-memory store
// and a file-backed store cannot quietly diverge on what "ranked" means.

const (
	bm25K1 = 1.2
	bm25B  = 0.75
)

// tokenize lowercases, splits on non-alphanumerics, and additionally emits
// single CJK characters as tokens, so Chinese knowledge entries are searchable
// instead of being one giant token.
func tokenize(text string) []string {
	var tokens []string
	var cjkRun []rune
	flushCJK := func() {
		// Each CJK character becomes its own token: Chinese runs are not
		// whitespace-separated, so a whole-run token would never match a
		// query that shares only part of it.
		for _, r := range cjkRun {
			tokens = append(tokens, string(r))
		}
		cjkRun = nil
	}
	var word []rune
	flushWord := func() {
		if len(word) > 0 {
			tokens = append(tokens, strings.ToLower(string(word)))
			word = nil
		}
	}
	for _, r := range text {
		switch {
		case isCJK(r):
			flushWord()
			cjkRun = append(cjkRun, r)
		case r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9':
			flushCJK()
			word = append(word, r)
		default:
			flushCJK()
			flushWord()
		}
	}
	flushCJK()
	flushWord()
	return tokens
}

func isCJK(r rune) bool {
	return r >= 0x4E00 && r <= 0x9FFF || r >= 0x3400 && r <= 0x4DBF
}

// bm25Rank scores every document against the query with the Okapi BM25 formula
// (k1=1.2, b=0.75) and returns them ordered best-first.
func bm25Rank(docs []core.Document, query string) []core.Document {
	if len(docs) == 0 {
		return nil
	}
	qTokens := tokenize(query)
	if len(qTokens) == 0 {
		return docs
	}

	// Document frequency over the corpus.
	df := make(map[string]int)
	docTokens := make([][]string, len(docs))
	docLen := make([]int, len(docs))
	for i, doc := range docs {
		tokens := tokenize(doc.Content)
		docTokens[i] = tokens
		docLen[i] = len(tokens)
		seen := make(map[string]bool)
		for _, t := range tokens {
			if !seen[t] {
				seen[t] = true
			}
		}
		for t := range seen {
			df[t]++
		}
	}
	avgLen := 0.0
	for _, l := range docLen {
		avgLen += float64(l)
	}
	avgLen /= float64(len(docs))
	if avgLen == 0 {
		avgLen = 1
	}

	scored := make([]core.Document, 0, len(docs))
	for i, doc := range docs {
		tf := make(map[string]int)
		for _, t := range docTokens[i] {
			tf[t]++
		}
		score := 0.0
		for _, qt := range qTokens {
			f := tf[qt]
			if f == 0 {
				continue
			}
			n := float64(df[qt])
			idf := math.Log(1 + (float64(len(docs))-n+0.5)/(n+0.5))
			score += idf * (float64(f) * (bm25K1 + 1)) /
				(float64(f) + bm25K1*(1-bm25B+bm25B*float64(docLen[i])/avgLen))
		}
		if score <= 0 {
			// A document matching no query term is not a retrieval result;
			// returning it would fill topK with noise the caller never asked for.
			continue
		}
		doc.Score = score
		scored = append(scored, doc)
	}
	sort.SliceStable(scored, func(i, j int) bool { return scored[i].Score > scored[j].Score })
	return scored
}

// cosine returns the cosine similarity of two vectors. Vectors of different
// length, or zero vectors, score 0 rather than panicking: a half-configured
// store degrades to "unrelated", never to a crash.
func cosine(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
