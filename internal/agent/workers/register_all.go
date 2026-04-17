package workers

import "fmt"

// RegisterAll registers all 18 agent tool handlers into the given ToolRegistry.
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
	}

	for _, r := range registrations {
		if err := registry.Register(r.def, r.handler); err != nil {
			return fmt.Errorf("register all handlers: %w", err)
		}
	}

	return nil
}
