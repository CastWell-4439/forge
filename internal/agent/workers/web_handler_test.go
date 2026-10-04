package workers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func realCfg(over ...func(*HandlerConfig)) HandlerConfig {
	cfg := HandlerConfig{
		Mode:                 HandlerModeReal,
		WebFetchAllowPrivate: true, // httptest lives on 127.0.0.1
	}
	for _, o := range over {
		o(&cfg)
	}
	return cfg
}

// --- web.fetch ---

func TestWebFetchRealReturnsContentTitleStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<html><head><title>  Hello   World  </title></head><body>body</body></html>")
	}))
	defer srv.Close()

	h := NewWebFetchHandler(realCfg())
	out, err := h(context.Background(), map[string]interface{}{"url": srv.URL})
	require.NoError(t, err)

	assert.Contains(t, out["content"], "body")
	assert.Equal(t, "Hello World", out["title"], "title collapses whitespace")
	assert.Equal(t, 200, out["status"])
}

// The SSRF guard refuses loopback targets unless explicitly allowed —
// the classic cloud-metadata shape. No server is contacted either way.
func TestWebFetchRefusesPrivateTargetsByDefault(t *testing.T) {
	h := NewWebFetchHandler(HandlerConfig{Mode: HandlerModeReal}) // no allowPrivate
	_, err := h(context.Background(), map[string]interface{}{"url": "http://127.0.0.1:9/x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "private")

	// Numeric metadata-style address too.
	_, err = h(context.Background(), map[string]interface{}{"url": "http://169.254.169.254/latest/meta-data/"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "private")
}

func TestWebFetchRejectsNonHTTPSchemes(t *testing.T) {
	h := NewWebFetchHandler(realCfg())
	_, err := h(context.Background(), map[string]interface{}{"url": "ftp://example.com/x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not allowed")
}

func TestWebFetchReportsHTTPFailures(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusTeapot)
	}))
	defer srv.Close()

	h := NewWebFetchHandler(realCfg())
	_, err := h(context.Background(), map[string]interface{}{"url": srv.URL})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 418")
}

// A body past the cap is truncated with an explicit marker, never silently.
func TestWebFetchTruncatesLargeBodies(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, strings.Repeat("a", webFetchMaxBytes+4096))
	}))
	defer srv.Close()

	h := NewWebFetchHandler(realCfg())
	out, err := h(context.Background(), map[string]interface{}{"url": srv.URL})
	require.NoError(t, err)
	assert.Contains(t, out["content"], "...(truncated at 1MB)")
}

// Mock mode stays mock: canned page, no network.
func TestWebFetchMockModeCanned(t *testing.T) {
	h := NewWebFetchHandler(HandlerConfig{Mode: HandlerModeMock})
	out, err := h(context.Background(), map[string]interface{}{"url": "https://example.com"})
	require.NoError(t, err)
	assert.Equal(t, "Mock Page Title", out["title"])
}

// --- web.search ---

const ddgFixture = `<html><body>
<div class="links_main">
 <a rel="nofollow" class="result-link" href="https://go.dev/">Go - The Language</a>
 <span class="result-snippet">Go is an open source language.</span>
</div>
<div class="links_main">
 <a class="result-link" href="https://example.com/deep?x=1">Second Result</a>
 <span class="result-snippet">Second snippet text.</span>
</div>
</body></html>`

func TestWebSearchDuckDuckGoParsesResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		require.NoError(t, r.ParseForm())
		assert.NotEmpty(t, r.FormValue("q"))
		fmt.Fprint(w, ddgFixture)
	}))
	defer srv.Close()

	h := NewWebSearchHandler(realCfg(func(c *HandlerConfig) {
		c.WebSearchProvider = "duckduckgo"
		c.WebSearchEndpoint = srv.URL
	}))
	out, err := h(context.Background(), map[string]interface{}{"query": "golang"})
	require.NoError(t, err)

	results, ok := out["results"].([]map[string]interface{})
	require.True(t, ok, "results must be a list of maps")
	require.Len(t, results, 2)
	assert.Equal(t, "Go - The Language", results[0]["title"])
	assert.Equal(t, "https://go.dev/", results[0]["url"])
	assert.Equal(t, "Go is an open source language.", results[0]["snippet"])
	assert.Equal(t, "Second Result", results[1]["title"])
}

// The count parameter caps results (page has more rows than asked).
func TestWebSearchHonorsCount(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, ddgFixture)
	}))
	defer srv.Close()

	h := NewWebSearchHandler(realCfg(func(c *HandlerConfig) {
		c.WebSearchProvider = "duckduckgo"
		c.WebSearchEndpoint = srv.URL
	}))
	out, err := h(context.Background(), map[string]interface{}{"query": "q", "count": 1})
	require.NoError(t, err)
	assert.Len(t, out["results"].([]map[string]interface{}), 1)
}

const braveFixture = `{"web":{"results":[
 {"title":"Brave Hit","url":"https://brave.example/a","description":"brave snippet"},
 {"title":"Second","url":"https://brave.example/b","description":"second snippet"}
]}}`

func TestWebSearchBraveMapsResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "test-key", r.Header.Get("X-Subscription-Token"))
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, braveFixture)
	}))
	defer srv.Close()

	h := NewWebSearchHandler(realCfg(func(c *HandlerConfig) {
		c.WebSearchProvider = "brave"
		c.WebSearchAPIKey = "test-key"
		c.WebSearchEndpoint = srv.URL
	}))
	out, err := h(context.Background(), map[string]interface{}{"query": "q"})
	require.NoError(t, err)
	results := out["results"].([]map[string]interface{})
	require.Len(t, results, 2)
	assert.Equal(t, "Brave Hit", results[0]["title"])
	assert.Equal(t, "brave snippet", results[0]["snippet"])
}

// Brave without a key stays an honest not-configured, not fake empty.
func TestWebSearchBraveWithoutKeyIsHonest(t *testing.T) {
	h := NewWebSearchHandler(realCfg(func(c *HandlerConfig) {
		c.WebSearchProvider = "brave"
		c.WebSearchAPIKey = ""
	}))
	_, err := h(context.Background(), map[string]interface{}{"query": "q"})
	require.ErrorIs(t, err, ErrNotConfigured)
}

// "off" is the explicit opt-out; an unknown provider name is a config error.
func TestWebSearchOffAndUnknownProviders(t *testing.T) {
	h := NewWebSearchHandler(realCfg(func(c *HandlerConfig) { c.WebSearchProvider = "off" }))
	_, err := h(context.Background(), map[string]interface{}{"query": "q"})
	require.ErrorIs(t, err, ErrNotConfigured)

	h = NewWebSearchHandler(realCfg(func(c *HandlerConfig) { c.WebSearchProvider = "bing" }))
	_, err = h(context.Background(), map[string]interface{}{"query": "q"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown provider")
}

// A backend that returns nothing parseable must say so — an empty result
// list would read as "nothing exists on the web", which is a lie.
func TestWebSearchUnparsableMarkupFailsLoudly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "<html><body>maintenance page</body></html>")
	}))
	defer srv.Close()

	h := NewWebSearchHandler(realCfg(func(c *HandlerConfig) {
		c.WebSearchProvider = "duckduckgo"
		c.WebSearchEndpoint = srv.URL
	}))
	_, err := h(context.Background(), map[string]interface{}{"query": "q"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no parsable results")
}

// Mock mode returns canned results without touching the network.
func TestWebSearchMockModeCanned(t *testing.T) {
	h := NewWebSearchHandler(HandlerConfig{Mode: HandlerModeMock})
	out, err := h(context.Background(), map[string]interface{}{"query": "anything"})
	require.NoError(t, err)
	assert.Len(t, out["results"].([]map[string]interface{}), 2)
}
