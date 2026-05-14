package rag

import (
	"context"
	"fmt"
	"time"

	"github.com/castwell/forge/internal/agent/core"
)

// KnowledgeSearchDef returns the ToolDef for the "knowledge.search" Agentic RAG tool.
// The Agent decides when to invoke this — not every turn.
func KnowledgeSearchDef() *core.ToolDef {
	return &core.ToolDef{
		Name:        "knowledge.search",
		DisplayName: "Knowledge Search",
		Category:    "knowledge",
		Description: "Search the knowledge base for relevant documents, tool usage examples, or past experience. Use when you need information about available tools, past solutions, or domain knowledge.",
		InputSchema: map[string]core.ParamDef{
			"query": {Type: "string", Description: "Search query describing what you need", Required: true},
			"top_k": {Type: "integer", Description: "Number of results to return (default 5, max 10)"},
		},
		OutputSchema: map[string]core.ParamDef{
			"results": {Type: "array", Description: "List of {id, content, score} documents"},
		},
		RequiredParams: []string{"query"},
		EstimatedTime:  2 * time.Second,
	}
}

// NewKnowledgeSearchHandler creates a handler backed by a core.Retriever.
// If retriever is nil, returns an error handler.
func NewKnowledgeSearchHandler(retriever core.Retriever) core.HandlerFunc {
	if retriever == nil {
		return func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
			return nil, fmt.Errorf("knowledge.search: retriever not configured")
		}
	}

	return func(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error) {
		query, _ := params["query"].(string)
		if query == "" {
			return nil, fmt.Errorf("knowledge.search: missing required param 'query'")
		}

		topK := 5
		if v, ok := params["top_k"].(float64); ok && v > 0 {
			topK = int(v)
		}
		if topK > 10 {
			topK = 10
		}

		docs, err := retriever.Search(ctx, query, topK)
		if err != nil {
			return nil, fmt.Errorf("knowledge.search: %w", err)
		}

		results := make([]map[string]interface{}, len(docs))
		for i, doc := range docs {
			results[i] = map[string]interface{}{
				"id":      doc.ID,
				"content": doc.Content,
				"score":   doc.Score,
			}
		}

		return map[string]interface{}{
			"results": results,
		}, nil
	}
}
