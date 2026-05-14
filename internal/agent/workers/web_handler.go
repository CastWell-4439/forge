package workers

import (
	"context"
	"fmt"
	"time"
)

// Web handler tool definitions: web.search, web.fetch

func WebSearchDef() *ToolDef {
	return &ToolDef{
		Name:           "web.search",
		DisplayName:    "Web Search",
		Category:       "web",
		Description:    "Search the web using a query string. Returns titles, URLs, and snippets.",
		InputSchema: map[string]ParamDef{
			"query": {Type: "string", Description: "Search query", Required: true},
			"count": {Type: "integer", Description: "Number of results (default 5, max 10)"},
		},
		OutputSchema: map[string]ParamDef{
			"results": {Type: "array", Description: "List of {title, url, snippet}"},
		},
		RequiredParams: []string{"query"},
		EstimatedTime:  3 * time.Second,
	}
}

func WebFetchDef() *ToolDef {
	return &ToolDef{
		Name:           "web.fetch",
		DisplayName:    "Web Fetch",
		Category:       "web",
		Description:    "Fetch and extract readable content from a URL. Returns text/markdown.",
		InputSchema: map[string]ParamDef{
			"url":       {Type: "string", Description: "URL to fetch", Required: true},
			"max_chars": {Type: "integer", Description: "Max characters to return (default 10000)"},
		},
		OutputSchema: map[string]ParamDef{
			"content": {Type: "string", Description: "Extracted page content"},
			"title":   {Type: "string", Description: "Page title"},
		},
		RequiredParams: []string{"url"},
		EstimatedTime:  5 * time.Second,
	}
}

// --- Handlers ---

func NewWebSearchHandler(cfg HandlerConfig) HandlerFunc {
	if cfg.Mode == HandlerModeMock {
		return mockWebSearch()
	}
	return realWebSearch()
}

func NewWebFetchHandler(cfg HandlerConfig) HandlerFunc {
	if cfg.Mode == HandlerModeMock {
		return mockWebFetch()
	}
	return realWebFetch()
}

func mockWebSearch() HandlerFunc {
	return func(_ context.Context, params map[string]interface{}) (map[string]interface{}, error) {
		query, _ := params["query"].(string)
		if query == "" {
			return nil, fmt.Errorf("web.search: missing required param 'query'")
		}
		return map[string]interface{}{
			"results": []map[string]interface{}{
				{"title": "Result 1 for " + query, "url": "https://example.com/1", "snippet": "First result snippet."},
				{"title": "Result 2 for " + query, "url": "https://example.com/2", "snippet": "Second result snippet."},
			},
		}, nil
	}
}

func mockWebFetch() HandlerFunc {
	return func(_ context.Context, params map[string]interface{}) (map[string]interface{}, error) {
		url, _ := params["url"].(string)
		if url == "" {
			return nil, fmt.Errorf("web.fetch: missing required param 'url'")
		}
		return map[string]interface{}{
			"content": fmt.Sprintf("# Mock Page\n\nContent fetched from %s\n\nLorem ipsum dolor sit amet.", url),
			"title":   "Mock Page Title",
		}, nil
	}
}

func realWebSearch() HandlerFunc {
	return func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
		return nil, fmt.Errorf("web.search: %w", ErrNotConfigured)
	}
}

func realWebFetch() HandlerFunc {
	return func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
		return nil, fmt.Errorf("web.fetch: %w", ErrNotConfigured)
	}
}
