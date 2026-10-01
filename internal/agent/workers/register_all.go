package workers

import "fmt"

// RegisterAll registers all 15 agent tool handlers into the given ToolRegistry.
// The HandlerConfig controls whether mock or real implementations are used.
//
// The set is the generic work every coding agent is given - files, shell, code,
// git reads, web, data, and one human channel. Domain-specific capabilities
// belong to the workflow plane's workers, not to this registry.
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
	}

	for _, r := range registrations {
		if err := registry.Register(r.def, r.handler); err != nil {
			return fmt.Errorf("register all handlers: %w", err)
		}
	}

	return nil
}
