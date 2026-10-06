package workers

import (
	"context"
	"fmt"

	"github.com/castwell/forge/internal/agent/rag"
)

// knowledgeSearchHandler wires knowledge.search to the configured retriever.
// Mock mode answers with a canned hit so the capability is exercisable without
// infrastructure; real mode without a retriever reports the gap instead of
// returning an empty result set that would read as "the knowledge base has no
// answers".
func knowledgeSearchHandler(cfg HandlerConfig) HandlerFunc {
	if cfg.Retriever != nil {
		return rag.NewKnowledgeSearchHandler(cfg.Retriever)
	}
	if cfg.Mode == HandlerModeMock {
		return func(_ context.Context, params map[string]interface{}) (map[string]interface{}, error) {
			query, _ := params["query"].(string)
			if query == "" {
				return nil, fmt.Errorf("knowledge.search: missing required param 'query'")
			}
			return map[string]interface{}{
				"results": []map[string]interface{}{
					{"id": "mock-1", "content": "[mock knowledge] " + query, "score": 1.0},
				},
				"mode": "mock",
			}, nil
		}
	}
	return func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
		return nil, fmt.Errorf("knowledge.search: %w (no retriever in HandlerConfig)", ErrNotConfigured)
	}
}

// RegisterAll registers all 20 agent tool handlers into the given ToolRegistry.
// The HandlerConfig controls whether mock or real implementations are used.
//
// The set is the generic work every coding agent is given - files, shell, code,
// git reads, web, data - plus one human channel, one skill channel, one
// knowledge channel, and three context-window operations. Domain-specific
// capabilities belong to the workflow plane's workers, not to this registry.
func RegisterAll(registry *ToolRegistry, cfg HandlerConfig) error {
	registrations := []struct {
		def     *ToolDef
		handler HandlerFunc
	}{
		// File handlers (6)
		{FileReadDef(), NewFileReadHandler(cfg)},
		{FileWriteDef(), NewFileWriteHandler(cfg)},
		{FileListDef(), NewFileListHandler(cfg)},
		{FileEditDef(), NewFileEditHandler(cfg)},
		{FileGlobDef(), NewFileGlobHandler(cfg)},
		{FileSearchDef(), NewFileSearchHandler(cfg)},

		// Shell handler (1)
		{ShellRunDef(), NewShellRunHandler(cfg)},

		// Git handlers (3, read-only)
		{GitStatusDef(), NewGitStatusHandler(cfg)},
		{GitLogDef(), NewGitLogHandler(cfg)},
		{GitDiffDef(), NewGitDiffHandler(cfg)},

		// Web handlers (2)
		{WebSearchDef(), NewWebSearchHandler(cfg)},
		{WebFetchDef(), NewWebFetchHandler(cfg)},

		// Code handler (1)
		{CodeExecuteDef(), NewCodeExecuteHandler(cfg)},

		// Data handler (1)
		{DataQueryDef(), NewDataQueryHandler(cfg)},

		// Human channel (1)
		{AskUserDef(), NewAskUserHandler(cfg)},

		// Skill channel (1)
		{SkillActivateDef(), NewSkillActivateHandler(cfg)},

		// Knowledge channel (1)
		{rag.KnowledgeSearchDef(), knowledgeSearchHandler(cfg)},

		// Context-window operations (3): intercepted by the loop before the
		// router; the handlers exist for registry shape and fail loudly if a
		// call ever reaches them (see context_handler.go).
		{ContextRemainingDef(), contextToolUnavailable(ToolContextRemaining)},
		{ContextCompactDef(), contextToolUnavailable(ToolContextCompact)},
		{ContextRecallDef(), contextToolUnavailable(ToolContextRecall)},
	}

	for _, r := range registrations {
		if err := registry.Register(r.def, r.handler); err != nil {
			return fmt.Errorf("register all handlers: %w", err)
		}
	}

	return nil
}
