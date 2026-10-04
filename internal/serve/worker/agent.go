package worker

import (
	"fmt"
	"log"
	"os"
	"strings"

	agentcore "github.com/castwell/forge/internal/agent"
	"github.com/castwell/forge/internal/agent/guardrails"
	"github.com/castwell/forge/internal/agent/workers"
	"github.com/castwell/forge/internal/worker"
	agentworker "github.com/castwell/forge/internal/workers/agent"
)

// Environment variables that configure the agent workflow worker.
//
//	FORGE_AGENT_MODE             real (default) | mock
//	FORGE_AGENT_WORKSPACE        base dir for file/shell tools (default .forge-workspace)
//	FORGE_WEB_SEARCH_PROVIDER    duckduckgo (default, no key) | brave | off
//	FORGE_WEB_SEARCH_API_KEY     key for the brave provider
//	FORGE_WEB_SEARCH_ENDPOINT    endpoint override (tests point this at httptest)
//	FORGE_WEB_FETCH_ALLOW_PRIVATE  lift the web.fetch SSRF guard (dev only)
const (
	envAgentMode          = "FORGE_AGENT_MODE"
	envAgentWorkspace     = "FORGE_AGENT_WORKSPACE"
	envAgentGuard         = "FORGE_AGENT_GUARD"
	envWebSearchProvider  = "FORGE_WEB_SEARCH_PROVIDER"
	envWebSearchAPIKey    = "FORGE_WEB_SEARCH_API_KEY"
	envWebSearchEndpoint  = "FORGE_WEB_SEARCH_ENDPOINT"
	envWebFetchAllowPriv  = "FORGE_WEB_FETCH_ALLOW_PRIVATE"
	defaultAgentWorkspace = ".forge-workspace"
)

// registerAgent wires the tenth workflow worker: a task that runs the full
// agent (golden tool set, real mode) through internal/agent — the assembly
// that the audit found reachable only from tests.
//
// The knowledge stack is injected so knowledge.search really retrieves;
// data.query and the web tools get their backends from the environment via
// WithToolConfig — env reads live here, never inside the agent library.
// Without an LLM key the worker registers an explanatory handler instead of
// failing with "unknown handler", matching every other configured-optional
// worker.
func registerAgent(r *worker.Registry) {
	llm := newEnvLLMClient("", 0.7, 4096) // model/temperature: harness defaults
	if llm == nil {
		r.Register("agent", unconfiguredHandler("agent",
			fmt.Sprintf("no LLM client; set %s", envLLMAPIKey)))
		return
	}

	mode := strings.ToLower(strings.TrimSpace(os.Getenv(envAgentMode)))
	workspace := envOrDefault(envAgentWorkspace, defaultAgentWorkspace)

	opts := []agentcore.Option{
		agentcore.WithRetriever(buildKnowledgeStack()),
		agentcore.WithWorkspace(workspace),
		agentcore.WithToolConfig(func(cfg *workers.HandlerConfig) {
			cfg.DataSource = os.Getenv(envPGDSN)
			cfg.WebSearchProvider = os.Getenv(envWebSearchProvider)
			cfg.WebSearchAPIKey = os.Getenv(envWebSearchAPIKey)
			cfg.WebSearchEndpoint = os.Getenv(envWebSearchEndpoint)
			cfg.WebFetchAllowPrivate = truthy(os.Getenv(envWebFetchAllowPriv))
		}),
	}

	// Tool output is untrusted input to the model: a fetched page can say
	// "ignore your instructions", and the observation is delivered as a
	// user-role message. Screening is therefore ON by default — an agent that
	// browses without it hands the internet a channel into its own
	// instruction stream. FORGE_AGENT_GUARD=off opts out explicitly.
	guardEnabled := agentGuardEnabled()
	if guardEnabled {
		opts = append(opts, agentcore.WithToolOutputGuard(guardrails.NewInjectionDetector()))
	}

	switch mode {
	case "", "real":
		// Real is the zero-value default inside Agent; nothing to add.
	case "mock":
		opts = append(opts, agentcore.WithHandlerMode(workers.HandlerModeMock))
		mode = "mock"
	default:
		log.Printf("WARN: unknown %s %q (want real|mock); using real", envAgentMode, mode)
		mode = "real"
	}

	r.Register("agent", adaptWorkflowWorker("agent", agentworker.NewWorker(agentcore.New(llm, opts...))))
	log.Printf("INFO: agent worker registered (mode=%s workspace=%s guard=%v)", mode, workspace, guardEnabled)
}

// agentGuardEnabled resolves FORGE_AGENT_GUARD. Unset and unrecognised values
// both mean ON: injection screening is a security default, so the only way to
// lose it is to ask for it explicitly — a typo must never silently disable it.
func agentGuardEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(envAgentGuard))) {
	case "off", "0", "false", "no":
		return false
	case "", "on", "1", "true", "yes":
		return true
	default:
		log.Printf("WARN: unknown %s %q (want on|off); keeping the guard on", envAgentGuard, os.Getenv(envAgentGuard))
		return true
	}
}

// truthy parses the usual affirmative env values.
func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}
