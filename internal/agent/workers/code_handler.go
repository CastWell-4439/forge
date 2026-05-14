package workers

import (
	"context"
	"fmt"
	"time"
)

// Code handler tool definition: code.execute

func CodeExecuteDef() *ToolDef {
	return &ToolDef{
		Name:           "code.execute",
		DisplayName:    "Code Execute",
		Category:       "code",
		Description:    "Execute a code snippet in a sandboxed environment. Supports Go, Python, Shell.",
		InputSchema: map[string]ParamDef{
			"language": {Type: "string", Description: "Programming language: go, python, shell", Required: true},
			"code":     {Type: "string", Description: "Source code to execute", Required: true},
			"timeout":  {Type: "integer", Description: "Execution timeout in seconds (default 30)"},
		},
		OutputSchema: map[string]ParamDef{
			"stdout":    {Type: "string", Description: "Standard output"},
			"stderr":    {Type: "string", Description: "Standard error"},
			"exit_code": {Type: "integer", Description: "Process exit code"},
		},
		RequiredParams: []string{"language", "code"},
		EstimatedTime:  10 * time.Second,
	}
}

// --- Handlers ---

func NewCodeExecuteHandler(cfg HandlerConfig) HandlerFunc {
	if cfg.Mode == HandlerModeMock {
		return mockCodeExecute()
	}
	return realCodeExecute()
}

func mockCodeExecute() HandlerFunc {
	return func(_ context.Context, params map[string]interface{}) (map[string]interface{}, error) {
		lang, _ := params["language"].(string)
		code, _ := params["code"].(string)
		if lang == "" || code == "" {
			return nil, fmt.Errorf("code.execute: missing required params")
		}
		return map[string]interface{}{
			"stdout":    fmt.Sprintf("[mock %s] executed %d chars of code", lang, len(code)),
			"stderr":    "",
			"exit_code": 0,
		}, nil
	}
}

func realCodeExecute() HandlerFunc {
	return func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
		return nil, fmt.Errorf("code.execute: %w", ErrNotConfigured)
	}
}
