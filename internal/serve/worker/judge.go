package worker

import (
	"os"
	"strings"

	"github.com/castwell/forge/internal/agent/core"
	"github.com/castwell/forge/internal/agent/harness"
	"github.com/castwell/forge/internal/worker"
	judgeworker "github.com/castwell/forge/internal/workers/judge"
)

// Environment variables that configure the judge worker.
//
//	FORGE_JUDGE_WORKSPACE     directory artifact paths resolve against
//	FORGE_JUDGE_LLM_API_KEY   key for the scoring model (falls back to
//	                          FORGE_LLM_API_KEY — one model means one key)
//	FORGE_JUDGE_LLM_BASE_URL  endpoint override
//	FORGE_JUDGE_LLM_MODEL     model name
//
// The worker is registered unconditionally, unlike the planner. Two reasons:
// the static half needs no model and is useful on its own, and a workflow that
// declares `handler: judge` should get an answer that says which half is
// unavailable rather than "unknown handler", which would send an author looking
// for a typo.
const (
	envJudgeWorkspace = "FORGE_JUDGE_WORKSPACE"
	envJudgeAPIKey    = "FORGE_JUDGE_LLM_API_KEY"
	envJudgeBaseURL   = "FORGE_JUDGE_LLM_BASE_URL"
	envJudgeModel     = "FORGE_JUDGE_LLM_MODEL"

	// judgeTemperature is 0: scoring is a measurement, and a creative measurer
	// produces scores that move between runs of the same input.
	judgeTemperature = 0
	// judgeMaxTokens bounds the score. It is a number, a reason and a
	// suggestion; a reply far longer than that is the model explaining itself.
	judgeMaxTokens = 1024
)

// registerJudge wires the quality scorer.
func registerJudge(r *worker.Registry) {
	cfg := judgeworker.DefaultConfig()
	cfg.Workspace = strings.TrimSpace(os.Getenv(envJudgeWorkspace))
	cfg.Model = strings.TrimSpace(os.Getenv(envJudgeModel))

	r.Register("judge", adaptWorkflowWorker("judge", judgeworker.NewWorker(cfg, newJudgeLLM())))
}

// newJudgeLLM builds the scoring model client, or nil when no key is configured.
//
// Nil is a supported state rather than a misconfiguration: the worker reports
// that the quality half is unavailable and still performs the completeness
// check. A deployment with no model can therefore use `judge` as a pure
// artifact gate.
func newJudgeLLM() core.LLMClient {
	apiKey := strings.TrimSpace(os.Getenv(envJudgeAPIKey))
	if apiKey == "" {
		apiKey = strings.TrimSpace(os.Getenv(envLLMAPIKey))
	}
	if apiKey == "" {
		return nil
	}

	cfg := harness.DefaultLLMConfig()
	cfg.APIKey = apiKey
	if baseURL := strings.TrimSpace(os.Getenv(envJudgeBaseURL)); baseURL != "" {
		cfg.BaseURL = baseURL
	}
	if model := strings.TrimSpace(os.Getenv(envJudgeModel)); model != "" {
		cfg.Model = model
	}
	cfg.Temperature = judgeTemperature
	cfg.MaxTokens = judgeMaxTokens
	cfg.Streaming = llmStreamEnabled()

	return harness.NewLLMClient(cfg)
}
