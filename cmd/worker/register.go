package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/castwell/forge/internal/agent/core"
	"github.com/castwell/forge/internal/agent/harness"
	"github.com/castwell/forge/internal/hitl"
	"github.com/castwell/forge/internal/worker"
	"github.com/castwell/forge/internal/workers/ai"
	"github.com/castwell/forge/internal/workers/claudecode"
	"github.com/castwell/forge/internal/workers/git"
	hitlworker "github.com/castwell/forge/internal/workers/hitl"
	"github.com/castwell/forge/internal/workers/mcp"
	"github.com/castwell/forge/internal/workers/review"
	"github.com/castwell/forge/internal/workers/shell"
)

// Environment variables that configure the built-in workers.
const (
	envProjectConfig = "FORGE_PROJECT_CONFIG"
	envLLMAPIKey     = "FORGE_LLM_API_KEY"
	envLLMBaseURL    = "FORGE_LLM_BASE_URL"
	envMCPEndpoint   = "FORGE_MCP_ENDPOINT"
	envMCPToken      = "FORGE_MCP_TOKEN"
	envPGDSN         = "FORGE_PG_DSN"
	envRedisAddr     = "FORGE_REDIS_ADDR"
)

// defaultProjectConfig is the per-repository git configuration. projects/*.yaml
// is the existing convention for it.
const defaultProjectConfig = "projects/example-project.yaml"

// registerBuiltinHandlers registers every task handler this worker advertises.
//
// Workflows declare eight worker types (ai, review, database, git, mcp, hitl,
// shell, claude_code), so all eight are registered here. They are wired to
// different depths on purpose:
//
//   - shell, claude_code, hitl need nothing external and work as-is. hitl is
//     backed by an in-process manager.
//   - git works as soon as a projects/*.yaml exists; only its create_mr action
//     additionally needs GitLab credentials, and it reports that itself.
//   - ai and review need an LLM key. The key is read when a node actually runs,
//     so a missing key fails that one node with an actionable message rather
//     than stopping the worker from starting.
//   - database and mcp need external endpoints. Until those are wired they
//     report exactly what is missing instead of failing with "unknown handler".
func registerBuiltinHandlers(r *worker.Registry) {
	// Liveness probe: lets an operator verify the whole dispatch path
	// (coordinator -> gRPC -> handler) without side effects.
	r.Register("ping", func(_ context.Context, params map[string]interface{}) (map[string]interface{}, error) {
		return map[string]interface{}{"output": "pong", "params": params}, nil
	})

	// No external dependency.
	r.Register("shell", adaptWorkflowWorker("shell", shell.NewWorker(shell.DefaultConfig())))
	r.Register("claude_code", adaptWorkflowWorker("claude_code", claudecode.NewWorker(claudecode.DefaultConfig())))
	r.Register("hitl", adaptWorkflowWorker("hitl", hitlworker.NewWorker(hitl.NewManager(hitl.ManagerConfig{Timeout: 24 * time.Hour}), nil)))

	registerGit(r)
	registerAI(r)
	registerReview(r)
	registerDatabase(r)
	registerMCP(r)

	// Guard the wiring itself: a workflow that declares a worker this build does
	// not know about must fail loudly at registration time, not silently at run
	// time.
	for _, required := range []string{"ai", "review", "database", "git", "mcp", "hitl", "shell", "claude_code"} {
		if r.Get(required) == nil {
			panic(fmt.Sprintf("worker %q is declared by workflows but was not registered", required))
		}
	}
}

// registerGit wires the git worker from a projects/*.yaml file.
func registerGit(r *worker.Registry) {
	path := os.Getenv(envProjectConfig)
	if path == "" {
		path = defaultProjectConfig
	}
	cfg, err := git.LoadProjectConfig(path)
	if err != nil {
		r.Register("git", unconfiguredHandler("git",
			fmt.Sprintf("no project config; set %s (tried %q): %v", envProjectConfig, path, err)))
		return
	}
	r.Register("git", adaptWorkflowWorker("git", git.NewWorker(cfg)))
}

// registerAI wires the AI worker. Its LLM client is built per request, so a
// missing key surfaces on the node that needs the model.
func registerAI(r *worker.Registry) {
	r.Register("ai", adaptWorkflowWorker("ai", ai.NewWorker(ai.DefaultConfig(), llmFactory)))
}

// registerReview wires the review worker. It needs its LLM client up front, so
// without a key it registers the explanatory handler instead.
func registerReview(r *worker.Registry) {
	cfg := review.DefaultConfig()
	llm := newEnvLLMClient(cfg.Model, cfg.Temperature, cfg.MaxTokens)
	if llm == nil {
		r.Register("review", unconfiguredHandler("review",
			fmt.Sprintf("no LLM client; set %s", envLLMAPIKey)))
		return
	}
	// The retriever is optional: review works without a knowledge base.
	r.Register("review", adaptWorkflowWorker("review", review.NewWorker(cfg, llm, nil)))
}

// registerDatabase wires the database worker. Real PostgreSQL/Redis connectors
// are not wired yet, so this reports what it needs.
func registerDatabase(r *worker.Registry) {
	r.Register("database", unconfiguredHandler("database",
		fmt.Sprintf("no PostgreSQL/Redis connector is wired; it needs %s and %s plus a connector implementation",
			envPGDSN, envRedisAddr)))
}

// registerMCP wires the MCP worker when an endpoint is configured.
func registerMCP(r *worker.Registry) {
	endpoint := os.Getenv(envMCPEndpoint)
	if endpoint == "" {
		r.Register("mcp", unconfiguredHandler("mcp",
			fmt.Sprintf("no MCP server; set %s (and optionally %s)", envMCPEndpoint, envMCPToken)))
		return
	}
	w, err := mcp.NewWorker(context.Background(), mcp.WorkerConfig{
		Endpoint: endpoint,
		Token:    os.Getenv(envMCPToken),
	})
	if err != nil {
		r.Register("mcp", unconfiguredHandler("mcp",
			fmt.Sprintf("cannot connect to MCP endpoint %q: %v", endpoint, err)))
		return
	}
	r.Register("mcp", adaptWorkflowWorker("mcp", w))
}

// llmFactory adapts the environment-backed LLM client to the AI worker's factory.
func llmFactory(cfg ai.ModelConfig) (core.LLMClient, error) {
	llm := newEnvLLMClient(cfg.Model, cfg.Temperature, cfg.MaxTokens)
	if llm == nil {
		return nil, fmt.Errorf("LLM client is not configured: set %s", envLLMAPIKey)
	}
	return llm, nil
}

// newEnvLLMClient builds an LLM client from the environment, or returns nil when
// no API key is set. Returning nil instead of an error keeps the "not configured"
// decision at the call site, where the surrounding context is known.
func newEnvLLMClient(model string, temperature float64, maxTokens int) core.LLMClient {
	apiKey := os.Getenv(envLLMAPIKey)
	if apiKey == "" {
		return nil
	}
	cfg := harness.DefaultLLMConfig()
	cfg.APIKey = apiKey
	if baseURL := os.Getenv(envLLMBaseURL); baseURL != "" {
		cfg.BaseURL = baseURL
	}
	if model != "" {
		cfg.Model = model
	}
	cfg.Temperature = temperature
	cfg.MaxTokens = maxTokens
	return harness.NewLLMClient(cfg)
}

// unconfiguredHandler reports exactly what a worker is missing. A node that
// cannot run should say why, instead of failing with "unknown handler".
func unconfiguredHandler(name string, requirement string) worker.HandlerFunc {
	return func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
		return nil, fmt.Errorf("%s worker is not configured: %s", name, requirement)
	}
}

// workflowWorker is the shape shared by the V2 workflow workers: an action name
// plus parameters in, a textual result out.
type workflowWorker interface {
	Execute(ctx context.Context, action string, params map[string]any) (string, error)
}

// adaptWorkflowWorker adapts a V2 workflow worker to the worker.HandlerFunc
// signature. The action is read from the task's "action" parameter, matching
// how workflow YAML declares stage tasks.
func adaptWorkflowWorker(name string, w workflowWorker) worker.HandlerFunc {
	return func(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error) {
		action, _ := params["action"].(string)
		if action == "" {
			return nil, fmt.Errorf("%s worker: task params must contain a non-empty \"action\"", name)
		}
		out, err := w.Execute(ctx, action, params)
		if err != nil {
			return nil, fmt.Errorf("%s worker action %q: %w", name, action, err)
		}
		return map[string]interface{}{"output": out}, nil
	}
}
