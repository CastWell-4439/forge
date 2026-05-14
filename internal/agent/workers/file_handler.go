package workers

import (
	"context"
	"fmt"
	"time"
)

// File handler tool definitions: file.read, file.write, file.list

func FileReadDef() *ToolDef {
	return &ToolDef{
		Name:           "file.read",
		DisplayName:    "File Read",
		Category:       "file",
		Description:    "Read the contents of a file. Returns the file content as text.",
		InputSchema: map[string]ParamDef{
			"path":   {Type: "string", Description: "File path to read", Required: true},
			"offset": {Type: "integer", Description: "Byte offset to start reading from (default 0)"},
			"limit":  {Type: "integer", Description: "Max bytes to read (default: entire file)"},
		},
		OutputSchema: map[string]ParamDef{
			"content":  {Type: "string", Description: "File content"},
			"size":     {Type: "integer", Description: "Total file size in bytes"},
			"truncated": {Type: "boolean", Description: "True if content was truncated by limit"},
		},
		RequiredParams: []string{"path"},
		EstimatedTime:  1 * time.Second,
	}
}

func FileWriteDef() *ToolDef {
	return &ToolDef{
		Name:           "file.write",
		DisplayName:    "File Write",
		Category:       "file",
		Description:    "Write content to a file. Creates parent directories if needed. Overwrites existing files.",
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
		Name:           "file.list",
		DisplayName:    "File List",
		Category:       "file",
		Description:    "List files and directories at the given path. Returns names, sizes, and types.",
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

// --- Handlers ---

func NewFileReadHandler(cfg HandlerConfig) HandlerFunc {
	if cfg.Mode == HandlerModeMock {
		return mockFileRead()
	}
	return realFileRead()
}

func NewFileWriteHandler(cfg HandlerConfig) HandlerFunc {
	if cfg.Mode == HandlerModeMock {
		return mockFileWrite()
	}
	return realFileWrite()
}

func NewFileListHandler(cfg HandlerConfig) HandlerFunc {
	if cfg.Mode == HandlerModeMock {
		return mockFileList()
	}
	return realFileList()
}

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

func realFileRead() HandlerFunc {
	return func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
		return nil, fmt.Errorf("file.read: %w", ErrNotConfigured)
	}
}

func realFileWrite() HandlerFunc {
	return func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
		return nil, fmt.Errorf("file.write: %w", ErrNotConfigured)
	}
}

func realFileList() HandlerFunc {
	return func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
		return nil, fmt.Errorf("file.list: %w", ErrNotConfigured)
	}
}
