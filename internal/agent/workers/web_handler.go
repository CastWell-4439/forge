package workers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Web handler tools: web.fetch and web.search.
//
// Both real paths are driven by HandlerConfig, never by env reads inside the
// library: the assembly layer (cmd/worker, via the agent worker) fills the
// config from the environment — the same injection philosophy as Retriever.
//
//	web.fetch: plain HTTP GET with timeout, size cap and an SSRF guard
//	          (private / loopback / link-local targets are refused unless
//	          WebFetchAllowPrivate is set — dev servers and tests opt in).
//	web.search: pluggable providers. Default "duckduckgo" works with zero
//	          configuration; "brave" upgrades to a real API when a key is
//	          supplied; "none" keeps the honest not-configured answer. The
//	          endpoints are overridable so tests run against httptest.

const (
	webFetchDefaultTimeout = 15 * time.Second
	webFetchMaxBytes       = 1 << 20 // 1MB
	webSearchDefaultCount  = 5
	webSearchTimeout       = 10 * time.Second
	webUserAgent           = "forge-worker/1.0"
)

// Endpoints are package-level vars so tests can point the providers at
// httptest servers without env gymnastics in the library.
var (
	webSearchBraveEndpoint    = "https://api.search.brave.com/res/v1/web/search"
	webSearchDuckDuckEndpoint = "https://html.duckduckgo.com/html/"
)

// --- web.fetch ---

func WebFetchDef() *ToolDef {
	return &ToolDef{
		Name:        "web.fetch",
		DisplayName: "Web Fetch",
		Category:    "web",
		Description: "Fetch a URL over HTTP GET and return its content plus page title. Private/internal addresses are refused by default.",
		InputSchema: map[string]ParamDef{
			"url":     {Type: "string", Description: "Absolute http(s) URL to fetch", Required: true},
			"timeout": {Type: "integer", Description: "Timeout in seconds (default 15, max 60)"},
		},
		OutputSchema: map[string]ParamDef{
			"content": {Type: "string", Description: "Response body (truncated to 1MB)"},
			"title":   {Type: "string", Description: "Page title extracted from HTML, empty otherwise"},
			"status":  {Type: "integer", Description: "HTTP status code"},
		},
		RequiredParams: []string{"url"},
		Effect:         EffectRead,
		EstimatedTime:  10 * time.Second,
	}
}

func NewWebFetchHandler(cfg HandlerConfig) HandlerFunc {
	if cfg.Mode == HandlerModeMock {
		return mockWebFetch()
	}
	return realWebFetch(cfg)
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
			"status":  200,
		}, nil
	}
}

func realWebFetch(cfg HandlerConfig) HandlerFunc {
	return func(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error) {
		raw, _ := params["url"].(string)
		parsed, err := url.Parse(strings.TrimSpace(raw))
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return nil, fmt.Errorf("web.fetch: %q is not an absolute URL", raw)
		}
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			return nil, fmt.Errorf("web.fetch: scheme %q not allowed (http/https only)", parsed.Scheme)
		}
		if err := checkFetchTarget(parsed.Hostname(), cfg.WebFetchAllowPrivate); err != nil {
			return nil, fmt.Errorf("web.fetch: %w", err)
		}

		timeout := webFetchDefaultTimeout
		if secs, ok := toInt(params["timeout"], 0); ok && secs > 0 {
			if secs > 60 {
				secs = 60
			}
			timeout = time.Duration(secs) * time.Second
		}
		fetchCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()

		req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, parsed.String(), nil)
		if err != nil {
			return nil, fmt.Errorf("web.fetch: build request: %w", err)
		}
		req.Header.Set("User-Agent", webUserAgent)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("web.fetch: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			return nil, fmt.Errorf("web.fetch: HTTP %d from %s", resp.StatusCode, parsed.Host)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, webFetchMaxBytes+1))
		if err != nil {
			return nil, fmt.Errorf("web.fetch: read body: %w", err)
		}
		truncated := len(body) > webFetchMaxBytes
		if truncated {
			body = body[:webFetchMaxBytes]
		}

		content := string(body)
		if truncated {
			content += "\n...(truncated at 1MB)"
		}
		return map[string]interface{}{
			"content": content,
			"title":   extractHTMLTitle(content),
			"status":  resp.StatusCode,
		}, nil
	}
}

// checkFetchTarget refuses private, loopback and link-local targets — the
// classic SSRF shape (cloud metadata endpoints, the host's own admin ports).
// Explicit opt-in (dev servers, httptest-based tests) overrides it.
func checkFetchTarget(host string, allowPrivate bool) error {
	if allowPrivate {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil {
		// A hostname: resolve and check every answer so a name that points
		// at 127.0.0.1 cannot sneak through.
		addrs, err := net.LookupIP(host)
		if err != nil {
			return fmt.Errorf("cannot resolve host %q: %w", host, err)
		}
		for _, addr := range addrs {
			if isPrivateIP(addr) {
				return fmt.Errorf("host %q resolves to a private address (%s); set WebFetchAllowPrivate to override", host, addr)
			}
		}
		return nil
	}
	if isPrivateIP(ip) {
		return fmt.Errorf("target %s is a private address; set WebFetchAllowPrivate to override", ip)
	}
	return nil
}

func isPrivateIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}

var htmlTitleRe = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)

func extractHTMLTitle(body string) string {
	if m := htmlTitleRe.FindStringSubmatch(body); m != nil {
		return strings.TrimSpace(strings.Join(strings.Fields(m[1]), " "))
	}
	return ""
}

// --- web.search ---

func WebSearchDef() *ToolDef {
	return &ToolDef{
		Name:        "web.search",
		DisplayName: "Web Search",
		Category:    "web",
		Description: "Search the web and return top results (title, url, snippet). Requires a configured search provider (default: duckduckgo, no key).",
		InputSchema: map[string]ParamDef{
			"query": {Type: "string", Description: "Search query", Required: true},
			"count": {Type: "integer", Description: "Max results (default 5, max 10)"},
		},
		OutputSchema: map[string]ParamDef{
			"results": {Type: "array", Description: "Results with title/url/snippet"},
		},
		RequiredParams: []string{"query"},
		Effect:         EffectRead,
		EstimatedTime:  5 * time.Second,
	}
}

func NewWebSearchHandler(cfg HandlerConfig) HandlerFunc {
	if cfg.Mode == HandlerModeMock {
		return mockWebSearch()
	}
	return realWebSearch(cfg)
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

func realWebSearch(cfg HandlerConfig) HandlerFunc {
	return func(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error) {
		query, _ := params["query"].(string)
		if strings.TrimSpace(query) == "" {
			return nil, fmt.Errorf("web.search: missing required param 'query'")
		}
		count := webSearchDefaultCount
		if n, ok := toInt(params["count"], 0); ok && n > 0 {
			if n > 10 {
				n = 10
			}
			count = n
		}

		provider := strings.ToLower(strings.TrimSpace(cfg.WebSearchProvider))
		if provider == "" {
			provider = "duckduckgo" // zero-config default: real search without a key
		}

		searchCtx, cancel := context.WithTimeout(ctx, webSearchTimeout)
		defer cancel()

		var (
			results []SearchResult
			err     error
		)
		switch provider {
		case "off", "none":
			return nil, fmt.Errorf("web.search: %w (provider explicitly disabled)", ErrNotConfigured)
		case "brave":
			if cfg.WebSearchAPIKey == "" {
				return nil, fmt.Errorf("web.search: brave provider needs an API key (%w)", ErrNotConfigured)
			}
			results, err = searchBrave(searchCtx, cfg, query, count)
		case "duckduckgo":
			results, err = searchDuckDuckGo(searchCtx, cfg, query, count)
		default:
			return nil, fmt.Errorf("web.search: unknown provider %q (brave|duckduckgo|off)", provider)
		}
		if err != nil {
			return nil, err
		}

		out := make([]map[string]interface{}, 0, len(results))
		for _, r := range results {
			out = append(out, map[string]interface{}{
				"title":   r.Title,
				"url":     r.URL,
				"snippet": r.Snippet,
			})
		}
		return map[string]interface{}{"results": out}, nil
	}
}

// SearchResult is the provider-neutral hit shape.
type SearchResult struct {
	Title   string
	URL     string
	Snippet string
}

// searchBrave calls the Brave Web Search API (JSON, key header).
func searchBrave(ctx context.Context, cfg HandlerConfig, query string, count int) ([]SearchResult, error) {
	endpoint := cfg.WebSearchEndpoint
	if endpoint == "" {
		endpoint = webSearchBraveEndpoint
	}
	u := fmt.Sprintf("%s?q=%s&count=%d", endpoint, url.QueryEscape(query), count)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("web.search: build request: %w", err)
	}
	req.Header.Set("X-Subscription-Token", cfg.WebSearchAPIKey)
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("web.search: brave: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("web.search: brave returned HTTP %d", resp.StatusCode)
	}

	var parsed struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("web.search: brave parse: %w", err)
	}
	out := make([]SearchResult, 0, len(parsed.Web.Results))
	for _, r := range parsed.Web.Results {
		out = append(out, SearchResult{Title: r.Title, URL: r.URL, Snippet: r.Description})
	}
	return out, nil
}

// searchDuckDuckGo queries the html.duckduckgo.com lite endpoint (no key).
// It is HTML scraping: zero configuration is the feature, and upstream
// markup changes surface as an honest parse error rather than fake results.
func searchDuckDuckGo(ctx context.Context, cfg HandlerConfig, query string, count int) ([]SearchResult, error) {
	endpoint := cfg.WebSearchEndpoint
	if endpoint == "" {
		endpoint = webSearchDuckDuckEndpoint
	}
	form := url.Values{"q": {query}, "kl": {"us-en"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("web.search: build request: %w", err)
	}
	req.Header.Set("User-Agent", webUserAgent)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("web.search: duckduckgo: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("web.search: duckduckgo returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("web.search: read: %w", err)
	}

	results := parseDuckDuckGoHTML(string(body))
	if len(results) == 0 {
		return nil, fmt.Errorf("web.search: duckduckgo returned no parsable results (markup change or empty answer for %q)", query)
	}
	if len(results) > count {
		results = results[:count]
	}
	return results, nil
}

// parseDuckDuckGoHTML extracts lite-result links and snippets.
//
// The anchor scan is attribute-order agnostic (class before href or after —
// both occur in the wild): every <a> whose attributes contain
// class="result-link" is a hit, its href is pulled out separately. Snippets
// are collected in page order and paired positionally — result-snippet spans
// only occur on results, so the parallel arrays stay aligned. A structure
// the parser does not understand yields zero results, and realWebSearch
// turns that into an explicit error instead of pretending the answer was
// empty. (The host machine's DNS blocks duckduckgo.com, so this shape is
// pinned by fixtures rather than a live capture — noted in D-22 boundaries.)
var (
	ddgAnchorRe  = regexp.MustCompile(`(?is)<a\s([^>]*)>(.*?)</a>`)
	ddgHrefRe    = regexp.MustCompile(`href="([^"]+)"`)
	ddgSnippetRe = regexp.MustCompile(`(?is)<span[^>]*class="result-snippet"[^>]*>(.*?)</span>`)
)

func parseDuckDuckGoHTML(body string) []SearchResult {
	snippets := ddgSnippetRe.FindAllStringSubmatch(body, -1)

	var out []SearchResult
	for _, m := range ddgAnchorRe.FindAllStringSubmatch(body, -1) {
		attrs, title := m[1], m[2]
		if !strings.Contains(attrs, "result-link") {
			continue
		}
		href := ddgHrefRe.FindStringSubmatch(attrs)
		if len(href) == 0 {
			continue
		}
		res := SearchResult{
			URL:   unwrapDuckRedirect(unescapeHTML(href[1])),
			Title: cleanText(title),
		}
		if idx := len(out); idx < len(snippets) {
			res.Snippet = cleanText(snippets[idx][1])
		}
		out = append(out, res)
		if len(out) >= 10 {
			break
		}
	}
	return out
}

// unwrapDuckRedirect extracts the real target from DDG's redirect wrapper
// (…/l/?uddg=<url-encoded target>).
func unwrapDuckRedirect(href string) string {
	if !strings.Contains(href, "uddg=") {
		return href
	}
	if u, err := url.Parse(href); err == nil {
		if target := u.Query().Get("uddg"); target != "" {
			return target
		}
	}
	return href
}

var tagRe = regexp.MustCompile(`<[^>]+>`)

func cleanText(s string) string {
	return strings.TrimSpace(joinWords(unescapeHTML(tagRe.ReplaceAllString(s, ""))))
}

func joinWords(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func unescapeHTML(s string) string {
	r := strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#x27;", "'", "&nbsp;", " ")
	return r.Replace(s)
}
