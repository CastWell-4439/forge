package workers

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// File handler tool definitions: file.read, file.write, file.list,
// file.edit, file.glob, file.search.
//
// All six are implemented for real mode against HandlerConfig.Workspace: file
// work needs no external service, so "real" here means the ordinary filesystem,
// bounded to the workspace - a path that escapes it is rejected.

// --- Definitions ---

func FileReadDef() *ToolDef {
	return &ToolDef{
		Name:        "file.read",
		DisplayName: "File Read",
		Category:    "file",
		Description: "Read the contents of a file. Returns the file content as text.",
		InputSchema: map[string]ParamDef{
			"path":   {Type: "string", Description: "File path to read", Required: true},
			"offset": {Type: "integer", Description: "Byte offset to start reading from (default 0)"},
			"limit":  {Type: "integer", Description: "Max bytes to read (default: entire file)"},
		},
		OutputSchema: map[string]ParamDef{
			"content":   {Type: "string", Description: "File content"},
			"size":      {Type: "integer", Description: "Total file size in bytes"},
			"truncated": {Type: "boolean", Description: "True if content was truncated by limit"},
		},
		RequiredParams: []string{"path"},
		EstimatedTime:  1 * time.Second,
	}
}

func FileWriteDef() *ToolDef {
	return &ToolDef{
		Name:        "file.write",
		DisplayName: "File Write",
		Category:    "file",
		Description: "Write content to a file. Creates parent directories if needed. Overwrites existing files.",
		InputSchema: map[string]ParamDef{
			"path":    {Type: "string", Description: "File path to write", Required: true},
			"content": {Type: "string", Description: "Content to write", Required: true},
			"append":  {Type: "boolean", Description: "Append instead of overwrite (default false)"},
		},
		OutputSchema: map[string]ParamDef{
			"bytes_written": {Type: "integer", Description: "Number of bytes written"},
		},
		RequiredParams: []string{"path", "content"},
		EstimatedTime:  1 * time.Second,
	}
}

func FileListDef() *ToolDef {
	return &ToolDef{
		Name:        "file.list",
		DisplayName: "File List",
		Category:    "file",
		Description: "List files and directories at the given path. Returns names, sizes, and types.",
		InputSchema: map[string]ParamDef{
			"path":    {Type: "string", Description: "Directory path to list", Required: true},
			"pattern": {Type: "string", Description: "Glob pattern filter (e.g. '*.go')"},
		},
		OutputSchema: map[string]ParamDef{
			"entries": {Type: "array", Description: "List of {name, size, is_dir}"},
		},
		RequiredParams: []string{"path"},
		EstimatedTime:  1 * time.Second,
	}
}

func FileEditDef() *ToolDef {
	return &ToolDef{
		Name:        "file.edit",
		DisplayName: "File Edit",
		Category:    "file",
		Description: "Replace exactly one occurrence of old_string with new_string in a file. Fails when the " +
			"old text is missing or ambiguous, so an edit can never land in the wrong place.",
		InputSchema: map[string]ParamDef{
			"path":       {Type: "string", Description: "File path to edit", Required: true},
			"old_string": {Type: "string", Description: "Exact text to replace (must match exactly once)", Required: true},
			"new_string": {Type: "string", Description: "Replacement text", Required: true},
		},
		OutputSchema: map[string]ParamDef{
			"bytes_written": {Type: "integer", Description: "New file size in bytes"},
			"replacements":  {Type: "integer", Description: "Number of replacements made (always 1)"},
		},
		RequiredParams: []string{"path", "old_string", "new_string"},
		EstimatedTime:  1 * time.Second,
	}
}

func FileGlobDef() *ToolDef {
	return &ToolDef{
		Name:        "file.glob",
		DisplayName: "File Glob",
		Category:    "file",
		Description: "Find files whose paths match a glob pattern (Go filepath.Glob semantics, e.g. '**' is not recursive; use file.search for content).",
		InputSchema: map[string]ParamDef{
			"path":    {Type: "string", Description: "Base directory to search from (default: workspace root)"},
			"pattern": {Type: "string", Description: "Glob pattern, e.g. 'internal/**/*.go' is not supported; use 'internal/*.go'", Required: true},
		},
		OutputSchema: map[string]ParamDef{
			"matches": {Type: "array", Description: "Matching file paths, workspace-relative"},
		},
		RequiredParams: []string{"pattern"},
		EstimatedTime:  1 * time.Second,
	}
}

func FileSearchDef() *ToolDef {
	return &ToolDef{
		Name:        "file.search",
		DisplayName: "File Search",
		Category:    "file",
		Description: "Search file contents under a directory for a regular expression. Returns matching lines with their file and line number.",
		InputSchema: map[string]ParamDef{
			"path":    {Type: "string", Description: "Base directory to search from (default: workspace root)"},
			"pattern": {Type: "string", Description: "Regular expression to match", Required: true},
		},
		OutputSchema: map[string]ParamDef{
			"matches": {Type: "array", Description: "List of {path, line, text}"},
			"truncated": {Type: "boolean",
				Description: "True if the match limit was reached"},
		},
		RequiredParams: []string{"pattern"},
		EstimatedTime:  2 * time.Second,
	}
}

// --- Handler constructors ---

func NewFileReadHandler(cfg HandlerConfig) HandlerFunc {
	if cfg.Mode == HandlerModeMock {
		return mockFileRead()
	}
	return realFileOps(cfg, "file.read", fileReadReal)
}

func NewFileWriteHandler(cfg HandlerConfig) HandlerFunc {
	if cfg.Mode == HandlerModeMock {
		return mockFileWrite()
	}
	return realFileOps(cfg, "file.write", fileWriteReal)
}

func NewFileListHandler(cfg HandlerConfig) HandlerFunc {
	if cfg.Mode == HandlerModeMock {
		return mockFileList()
	}
	return realFileOps(cfg, "file.list", fileListReal)
}

func NewFileEditHandler(cfg HandlerConfig) HandlerFunc {
	if cfg.Mode == HandlerModeMock {
		return mockFileEdit()
	}
	return realFileOps(cfg, "file.edit", fileEditReal)
}

func NewFileGlobHandler(cfg HandlerConfig) HandlerFunc {
	if cfg.Mode == HandlerModeMock {
		return mockFileGlob()
	}
	return realFileOps(cfg, "file.glob", fileGlobReal)
}

func NewFileSearchHandler(cfg HandlerConfig) HandlerFunc {
	if cfg.Mode == HandlerModeMock {
		return mockFileSearch()
	}
	return realFileOps(cfg, "file.search", fileSearchReal)
}

// --- Mocks ---

func mockFileRead() HandlerFunc {
	return func(_ context.Context, params map[string]interface{}) (map[string]interface{}, error) {
		path, _ := params["path"].(string)
		if path == "" {
			return nil, fmt.Errorf("file.read: missing required param 'path'")
		}
		return map[string]interface{}{
			"content":   fmt.Sprintf("// mock content of %s\npackage main\n", path),
			"size":      int64(256),
			"truncated": false,
		}, nil
	}
}

func mockFileWrite() HandlerFunc {
	return func(_ context.Context, params map[string]interface{}) (map[string]interface{}, error) {
		path, _ := params["path"].(string)
		content, _ := params["content"].(string)
		if path == "" || content == "" {
			return nil, fmt.Errorf("file.write: missing required params")
		}
		return map[string]interface{}{
			"bytes_written": int64(len(content)),
		}, nil
	}
}

func mockFileList() HandlerFunc {
	return func(_ context.Context, params map[string]interface{}) (map[string]interface{}, error) {
		path, _ := params["path"].(string)
		if path == "" {
			return nil, fmt.Errorf("file.list: missing required param 'path'")
		}
		return map[string]interface{}{
			"entries": []map[string]interface{}{
				{"name": "main.go", "size": 1024, "is_dir": false},
				{"name": "pkg", "size": 0, "is_dir": true},
			},
		}, nil
	}
}

func mockFileEdit() HandlerFunc {
	return func(_ context.Context, params map[string]interface{}) (map[string]interface{}, error) {
		path, _ := params["path"].(string)
		oldString, _ := params["old_string"].(string)
		newString, _ := params["new_string"].(string)
		if path == "" || oldString == "" || newString == "" {
			return nil, fmt.Errorf("file.edit: missing required params")
		}
		return map[string]interface{}{
			"bytes_written": int64(len(newString)),
			"replacements":  1,
		}, nil
	}
}

func mockFileGlob() HandlerFunc {
	return func(_ context.Context, params map[string]interface{}) (map[string]interface{}, error) {
		pattern, _ := params["pattern"].(string)
		if pattern == "" {
			return nil, fmt.Errorf("file.glob: missing required param 'pattern'")
		}
		return map[string]interface{}{
			"matches": []string{"main.go", "go.mod"},
		}, nil
	}
}

func mockFileSearch() HandlerFunc {
	return func(_ context.Context, params map[string]interface{}) (map[string]interface{}, error) {
		pattern, _ := params["pattern"].(string)
		if pattern == "" {
			return nil, fmt.Errorf("file.search: missing required param 'pattern'")
		}
		return map[string]interface{}{
			"matches": []map[string]interface{}{
				{"path": "main.go", "line": 12, "text": "// match for " + pattern},
			},
			"truncated": false,
		}, nil
	}
}

// --- Real implementations ---

// realFileOps wires a workspace-bounded implementation. Every file tool shares
// the same rule: the resolved path must stay inside the workspace, so a tool
// call cannot read or write anywhere else on the machine.
func realFileOps(cfg HandlerConfig, tool string, fn func(workspace string, params map[string]interface{}) (map[string]interface{}, error)) HandlerFunc {
	return func(_ context.Context, params map[string]interface{}) (map[string]interface{}, error) {
		out, err := fn(cfg.Workspace, params)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", tool, err)
		}
		return out, nil
	}
}

// resolveInWorkspace resolves a user-supplied path and rejects any that escapes
// the workspace.
func resolveInWorkspace(workspace, path string) (string, error) {
	if strings.TrimSpace(workspace) == "" {
		return "", fmt.Errorf("workspace is not configured")
	}
	absWorkspace, err := filepath.Abs(workspace)
	if err != nil {
		return "", err
	}
	candidate := path
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(absWorkspace, candidate)
	}
	abs, err := filepath.Abs(candidate)
	if err != nil {
		return "", err
	}
	if abs != absWorkspace && !strings.HasPrefix(abs, absWorkspace+string(os.PathSeparator)) {
		return "", fmt.Errorf("path %q resolves outside the workspace", path)
	}
	return abs, nil
}

// toInt reads a numeric param that may have arrived as int, int64 or float64.
func toInt(v interface{}, fallback int) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	default:
		return fallback, false
	}
}

func fileReadReal(workspace string, params map[string]interface{}) (map[string]interface{}, error) {
	path, _ := params["path"].(string)
	if path == "" {
		return nil, fmt.Errorf("missing required param 'path'")
	}
	target, err := resolveInWorkspace(workspace, path)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(target)
	if err != nil {
		return nil, err
	}
	offset, _ := toInt(params["offset"], 0)
	limit, hasLimit := toInt(params["limit"], 0)
	if offset < 0 || offset > len(data) {
		return nil, fmt.Errorf("offset %d out of range (file is %d bytes)", offset, len(data))
	}
	content := data[offset:]
	truncated := false
	if hasLimit && limit > 0 && limit < len(content) {
		content = content[:limit]
		truncated = true
	}
	return map[string]interface{}{
		"content":   string(content),
		"size":      int64(len(data)),
		"truncated": truncated,
	}, nil
}

func fileWriteReal(workspace string, params map[string]interface{}) (map[string]interface{}, error) {
	path, _ := params["path"].(string)
	content, _ := params["content"].(string)
	if path == "" {
		return nil, fmt.Errorf("missing required param 'path'")
	}
	target, err := resolveInWorkspace(workspace, path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return nil, err
	}
	appendMode, _ := params["append"].(bool)
	flag := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	if appendMode {
		flag = os.O_CREATE | os.O_WRONLY | os.O_APPEND
	}
	f, err := os.OpenFile(target, flag, 0o644)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		return nil, err
	}
	return map[string]interface{}{"bytes_written": int64(len(content))}, nil
}

func fileListReal(workspace string, params map[string]interface{}) (map[string]interface{}, error) {
	path, _ := params["path"].(string)
	if path == "" {
		path = "."
	}
	target, err := resolveInWorkspace(workspace, path)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		return nil, err
	}
	pattern, _ := params["pattern"].(string)
	out := []map[string]interface{}{}
	for _, entry := range entries {
		if pattern != "" {
			ok, err := filepath.Match(pattern, entry.Name())
			if err != nil {
				return nil, fmt.Errorf("invalid pattern %q: %w", pattern, err)
			}
			if !ok {
				continue
			}
		}
		item := map[string]interface{}{"name": entry.Name(), "is_dir": entry.IsDir()}
		if !entry.IsDir() {
			if info, err := entry.Info(); err == nil {
				item["size"] = info.Size()
			}
		}
		out = append(out, item)
	}
	return map[string]interface{}{"entries": out}, nil
}

func fileEditReal(workspace string, params map[string]interface{}) (map[string]interface{}, error) {
	path, _ := params["path"].(string)
	oldString, _ := params["old_string"].(string)
	newString, _ := params["new_string"].(string)
	if path == "" || oldString == "" {
		return nil, fmt.Errorf("missing required params 'path' and 'old_string'")
	}
	target, err := resolveInWorkspace(workspace, path)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(target)
	if err != nil {
		return nil, err
	}
	count := strings.Count(string(data), oldString)
	switch count {
	case 0:
		return nil, fmt.Errorf("old_string not found in %s", path)
	case 1:
		// The only safe case: an ambiguous match could land in the wrong place.
	default:
		return nil, fmt.Errorf("old_string matches %d times in %s; add more context to make it unique", count, path)
	}
	updated := strings.Replace(string(data), oldString, newString, 1)
	if err := os.WriteFile(target, []byte(updated), 0o644); err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"bytes_written": int64(len(updated)),
		"replacements":  1,
	}, nil
}

func fileGlobReal(workspace string, params map[string]interface{}) (map[string]interface{}, error) {
	pattern, _ := params["pattern"].(string)
	if pattern == "" {
		return nil, fmt.Errorf("missing required param 'pattern'")
	}
	base, _ := params["path"].(string)
	if base == "" {
		base = "."
	}
	absBase, err := resolveInWorkspace(workspace, base)
	if err != nil {
		return nil, err
	}
	// The pattern is matched against a path joined under the base directory,
	// and every hit is re-checked against the workspace, so a pattern can walk
	// sideways but never out.
	raw, err := filepath.Glob(filepath.Join(absBase, pattern))
	if err != nil {
		return nil, fmt.Errorf("invalid pattern %q: %w", pattern, err)
	}
	absWorkspace, err := filepath.Abs(workspace)
	if err != nil {
		return nil, err
	}
	matches := []string{}
	for _, hit := range raw {
		if hit != absWorkspace && !strings.HasPrefix(hit, absWorkspace+string(os.PathSeparator)) {
			continue
		}
		rel, err := filepath.Rel(absWorkspace, hit)
		if err != nil {
			continue
		}
		matches = append(matches, rel)
	}
	sort.Strings(matches)
	return map[string]interface{}{"matches": matches}, nil
}

const (
	searchMaxFileBytes = 1 << 20 // skip files larger than 1MB
	searchMaxMatches   = 200
)

func fileSearchReal(workspace string, params map[string]interface{}) (map[string]interface{}, error) {
	pattern, _ := params["pattern"].(string)
	if pattern == "" {
		return nil, fmt.Errorf("missing required param 'pattern'")
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid pattern %q: %w", pattern, err)
	}
	base, _ := params["path"].(string)
	if base == "" {
		base = "."
	}
	absBase, err := resolveInWorkspace(workspace, base)
	if err != nil {
		return nil, err
	}
	absWorkspace, err := filepath.Abs(workspace)
	if err != nil {
		return nil, err
	}

	type match struct {
		Path string `json:"path"`
		Line int    `json:"line"`
		Text string `json:"text"`
	}
	var matches []match
	truncated := false

	walkErr := filepath.WalkDir(absBase, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entry: skip, keep searching
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if truncated {
			return filepath.SkipAll
		}
		info, err := d.Info()
		if err != nil || info.Size() > searchMaxFileBytes {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(absWorkspace, p)
		if err != nil {
			rel = p
		}
		for i, line := range strings.Split(string(data), "\n") {
			if re.MatchString(line) {
				matches = append(matches, match{Path: rel, Line: i + 1, Text: strings.TrimSpace(line)})
				if len(matches) >= searchMaxMatches {
					truncated = true
					break
				}
			}
		}
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	out := make([]map[string]interface{}, 0, len(matches))
	for _, m := range matches {
		out = append(out, map[string]interface{}{"path": m.Path, "line": m.Line, "text": m.Text})
	}
	return map[string]interface{}{"matches": out, "truncated": truncated}, nil
}
