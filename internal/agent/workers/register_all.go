package workers

import "fmt"

// RegisterAll registers all 27 agent tool handlers into the given ToolRegistry.
// The HandlerConfig controls whether mock or real implementations are used.
func RegisterAll(registry *ToolRegistry, cfg HandlerConfig) error {
	registrations := []struct {
		def     *ToolDef
		handler HandlerFunc
	}{
		// Media handlers (2)
		{MediaDownloadDef(), NewMediaDownloadHandler(cfg)},
		{MediaUploadDef(), NewMediaUploadHandler(cfg)},

		// Video probe/preprocess handlers (2)
		{VideoProbeDef(), NewVideoProbeHandler(cfg)},
		{VideoPreprocessDef(), NewVideoPreprocessHandler(cfg)},

		// AI handlers (6)
		{AIFaceSwapDef(), NewAIFaceSwapHandler(cfg)},
		{AIMultiFaceSwapDef(), NewAIMultiFaceSwapHandler(cfg)},
		{AILipSyncDef(), NewAILipSyncHandler(cfg)},
		{AITTSDef(), NewAITTSHandler(cfg)},
		{AIScriptDef(), NewAIScriptHandler(cfg)},
		{AISubtitleGenDef(), NewAISubtitleGenHandler(cfg)},

		// FFmpeg handlers (6)
		{VideoEncodeDef(), NewVideoEncodeHandler(cfg)},
		{VideoTrimDef(), NewVideoTrimHandler(cfg)},
		{VideoConcatDef(), NewVideoConcatHandler(cfg)},
		{VideoSubtitlesDef(), NewVideoSubtitlesHandler(cfg)},
		{AudioMixDef(), NewAudioMixHandler(cfg)},
		{AudioBGMSelectDef(), NewAudioBGMSelectHandler(cfg)},

		// Quality handlers (2)
		{QualityVideoCheckDef(), NewQualityVideoCheckHandler(cfg)},
		{QualityFaceCheckDef(), NewQualityFaceCheckHandler(cfg)},

		// --- General-purpose handlers (9) ---

		// File handlers (3)
		{FileReadDef(), NewFileReadHandler(cfg)},
		{FileWriteDef(), NewFileWriteHandler(cfg)},
		{FileListDef(), NewFileListHandler(cfg)},

		// Web handlers (2)
		{WebSearchDef(), NewWebSearchHandler(cfg)},
		{WebFetchDef(), NewWebFetchHandler(cfg)},

		// Code handler (1)
		{CodeExecuteDef(), NewCodeExecuteHandler(cfg)},

		// Data handler (1)
		{DataQueryDef(), NewDataQueryHandler(cfg)},

		// LLM handler (1)
		{LLMSummarizeDef(), NewLLMSummarizeHandler(cfg)},

		// Image handler (1)
		{ImageGenerateDef(), NewImageGenerateHandler(cfg)},
	}

	for _, r := range registrations {
		if err := registry.Register(r.def, r.handler); err != nil {
			return fmt.Errorf("register all handlers: %w", err)
		}
	}

	return nil
}
