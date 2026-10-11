package worker

import (
	"os"
	"strings"

	"github.com/castwell/forge/internal/agent/core"
	"github.com/castwell/forge/internal/agent/harness"
)

// Child model routing for delegated subagents.
//
// A delegated subtask is frequently not the same kind of work as the parent's.
// Extraction, reformatting, summarising a file, checking a claim — the parent
// needs judgement, the child needs throughput. Once the child exists the parent
// cannot choose per call, so the choice has to be made at assembly time and it
// has to be declarable.
//
//	FORGE_SUBAGENT_LLM_MODEL     model for delegated children. Unset means
//	                             children inherit the parent's client, which is
//	                             the historical behaviour and costs nothing.
//	FORGE_SUBAGENT_LLM_BASE_URL  endpoint override
//	FORGE_SUBAGENT_LLM_API_KEY   key override; unset falls back to
//	                             FORGE_LLM_API_KEY, because pointing a child at
//	                             another MODEL on the same provider is the common
//	                             case and should not need a second key
//
// Only the model routing differs. The child is still assembled by the same code
// path as its parent — same handler mode, same guards, same MCP bridge, same
// tool narrowing — so this cannot widen what a child is allowed to do. That
// property is what makes the feature safe to add: it changes cost and latency,
// not authority.
//
// Unset is the default on purpose. A deployment that has not thought about
// child models should not discover it is paying for two.

// envChildModel and friends are the routing switches.
const (
	envChildModel   = "FORGE_SUBAGENT_LLM_MODEL"
	envChildBaseURL = "FORGE_SUBAGENT_LLM_BASE_URL"
	envChildAPIKey  = "FORGE_SUBAGENT_LLM_API_KEY"
)

// childTemperature is higher than a judge's and lower than a chat default:
// delegated subtasks are usually structured work with some freedom in how to
// get there.
const childTemperature = 0.3

// childMaxTokens bounds a child's reply. It is deliberately smaller than the
// parent's: a child reports back, it does not write the final answer.
const childMaxTokens = 2048

// newChildLLMClient builds the client delegated children use, or nil when no
// child model is configured.
//
// nil is the normal case and means "inherit". It is not an error and it is not
// logged as one: a deployment that has not set the variable has asked for the
// historical behaviour, not for a missing feature.
func newChildLLMClient() core.LLMClient {
	model := strings.TrimSpace(os.Getenv(envChildModel))
	if model == "" {
		// No model means no routing. A base URL or key alone would leave the
		// child on the parent's model but pointed at another endpoint, which is
		// a configuration nobody means to write.
		return nil
	}

	apiKey := strings.TrimSpace(os.Getenv(envChildAPIKey))
	if apiKey == "" {
		apiKey = strings.TrimSpace(os.Getenv(envLLMAPIKey))
	}
	if apiKey == "" {
		// The parent's client could not be built without a key either, so this
		// path is unreachable in practice; returning nil keeps the child on the
		// parent's client rather than failing the whole agent registration over
		// an optional feature.
		return nil
	}

	cfg := harness.DefaultLLMConfig()
	cfg.APIKey = apiKey
	cfg.Model = model
	if baseURL := strings.TrimSpace(os.Getenv(envChildBaseURL)); baseURL != "" {
		cfg.BaseURL = baseURL
	}
	cfg.Temperature = childTemperature
	cfg.MaxTokens = childMaxTokens
	cfg.Streaming = llmStreamEnabled()

	return harness.NewLLMClient(cfg)
}
