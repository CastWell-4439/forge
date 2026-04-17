package workers

// AgentConfig holds all configuration for agent worker handlers.
// In production, this is loaded from conf/agent.toml.
// For testing, use DefaultMockConfig().
type AgentConfig struct {
	Handler HandlerSection `yaml:"handler"`
	Media   MediaSection   `yaml:"media"`
	AI      AISection      `yaml:"ai"`
	Video   VideoSection   `yaml:"video"`
	Quality QualitySection `yaml:"quality"`
}

// HandlerSection configures the handler execution mode.
type HandlerSection struct {
	Mode      string `yaml:"mode"`      // "mock" or "real"
	Workspace string `yaml:"workspace"` // Base directory for file operations
}

// MediaSection configures media upload/download services.
type MediaSection struct {
	OSSEndpoint  string `yaml:"oss_endpoint"`
	OSSBucket    string `yaml:"oss_bucket"`
	OSSAccessKey string `yaml:"oss_access_key"`
	OSSSecretKey string `yaml:"oss_secret_key"`
}

// AISection configures AI service endpoints.
type AISection struct {
	FaceFusionURL string `yaml:"facefusion_url"`
	TTSURL        string `yaml:"tts_url"`
	LLMURL        string `yaml:"llm_url"`
	LLMAPIKey     string `yaml:"llm_api_key"`
}

// VideoSection configures video processing tools.
type VideoSection struct {
	FFmpegPath  string `yaml:"ffmpeg_path"`
	FFprobePath string `yaml:"ffprobe_path"`
}

// QualitySection configures quality check thresholds.
type QualitySection struct {
	FaceSimilarityThreshold float64 `yaml:"face_similarity_threshold"`
}

// DefaultMockConfig returns a configuration suitable for mock/testing mode.
func DefaultMockConfig() *AgentConfig {
	return &AgentConfig{
		Handler: HandlerSection{
			Mode:      "mock",
			Workspace: "/tmp/forge",
		},
		Media: MediaSection{},
		AI:    AISection{},
		Video: VideoSection{
			FFmpegPath:  "ffmpeg",
			FFprobePath: "ffprobe",
		},
		Quality: QualitySection{
			FaceSimilarityThreshold: 0.7,
		},
	}
}

// HandlerConfigFromAgent converts AgentConfig to HandlerConfig.
func HandlerConfigFromAgent(cfg *AgentConfig) HandlerConfig {
	mode := HandlerModeMock
	if cfg.Handler.Mode == "real" {
		mode = HandlerModeReal
	}
	return HandlerConfig{
		Mode:      mode,
		Workspace: cfg.Handler.Workspace,
	}
}
