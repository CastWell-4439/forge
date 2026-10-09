package worker

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/castwell/forge/internal/agent/core"
	"github.com/castwell/forge/internal/agent/harness"
	"github.com/castwell/forge/internal/agent/rag"
	"github.com/castwell/forge/internal/hitl"
	"github.com/castwell/forge/internal/worker"
	"github.com/castwell/forge/internal/workers/ai"
	"github.com/castwell/forge/internal/workers/claudecode"
	"github.com/castwell/forge/internal/workers/database"
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
	envLLMStream     = "FORGE_LLM_STREAM"
	envMCPEndpoint   = "FORGE_MCP_ENDPOINT"
	envMCPToken      = "FORGE_MCP_TOKEN"
	envPGDSN         = "FORGE_PG_DSN"
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
//   - database is wired: fill FORGE_PG_DSN (or FORGE_PG_HOST and friends) and
//     query_pg runs; without config it reports which variable to set.
//   - mcp needs an endpoint. Until one is set it reports exactly what is
//     missing instead of failing with "unknown handler".
func registerBuiltinHandlers(r *worker.Registry) {
	// Liveness probe: lets an operator verify the whole dispatch path
	// (coordinator -> gRPC -> handler) without side effects.
	r.Register("ping", func(_ context.Context, params map[string]interface{}) (map[string]interface{}, error) {
		return map[string]interface{}{"output": "pong", "params": params}, nil
	})

	// No external dependency.
	r.Register("shell", adaptWorkflowWorker("shell", shell.NewWorker(shell.DefaultConfig())))
	r.Register("claude_code", adaptWorkflowWorker("claude_code", claudecode.NewWorker(claudecode.DefaultConfig())))
	registerHITL(r)

	registerGit(r)

	// One shared journal (D-12): its state is per session and its writes are
	// mutex-guarded, so a single instance covers every agent-backed worker.
	journal := buildRunJournal()
	registerAI(r, journal)
	registerReview(r, journal)
	registerDatabase(r)
	registerAgent(r)
	registerWasm(r)
	registerMCP(r)

	// Guard the wiring itself: a workflow that declares a worker this build does
	// not know about must fail loudly at registration time, not silently at run
	// time.
	for _, required := range []string{"ai", "review", "database", "git", "mcp", "hitl", "shell", "claude_code", "wasm", "agent"} {
		if r.Get(required) == nil {
			panic(fmt.Sprintf("worker %q is declared by workflows but was not registered", required))
		}
	}
}

// registerHITL wires the human-in-the-loop worker.
//
// The store is what makes a filed request answerable by the coordinator: the two
// run in separate processes, and the table is the only thing they share. Without
// PostgreSQL the request lives only in this process's memory, which still works
// for a single-node deployment and is reported rather than hidden.
//
// The callback stays nil on purpose: it exists to notify an external system
// (chat, ticket queue) and no provider has been chosen. A callback that silently
// does nothing would be worse than an absent one, because the deployment would
// look as though notifications were being sent.
func registerHITL(r *worker.Registry) {
	manager := hitl.NewManager(hitl.ManagerConfig{Timeout: 24 * time.Hour})

	if cfg := database.ConfigFromEnv(); cfg != nil && cfg.Postgres != nil {
		dsn := cfg.Postgres.DSN()
		pool, err := pgxpool.New(context.Background(), dsn)
		if err != nil {
			// Not fatal: the worker still runs, and requests still work within
			// this process. What is lost is the coordinator's ability to see
			// them, and the log says so.
			log.Printf("WARN: hitl: could not open PostgreSQL for request persistence: %v; "+
				"requests will not be visible to the coordinator", err)
		} else {
			manager = hitl.NewManager(hitl.ManagerConfig{
				Timeout: 24 * time.Hour,
				Store:   hitl.NewPGStore(pool),
			})
			log.Printf("INFO: hitl: requests are persisted and shared with the coordinator")
		}
	} else {
		log.Printf("INFO: hitl: no PostgreSQL, so requests are in-memory only " +
			"(single-process use; the coordinator cannot answer them)")
	}

	r.Register("hitl", adaptWorkflowWorker("hitl", hitlworker.NewWorker(manager, nil)))
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
func registerAI(r *worker.Registry, journal harness.Journal) {
	r.Register("ai", adaptWorkflowWorker("ai", ai.NewWorker(ai.DefaultConfig(), llmFactory, ai.WithJournal(journal))))
}

// registerReview wires the review worker. It needs its LLM client up front, so
// without a key it registers the explanatory handler instead.
func registerReview(r *worker.Registry, journal harness.Journal) {
	cfg := review.DefaultConfig()
	llm := newEnvLLMClient(cfg.Model, cfg.Temperature, cfg.MaxTokens)
	if llm == nil {
		r.Register("review", unconfiguredHandler("review",
			fmt.Sprintf("no LLM client; set %s", envLLMAPIKey)))
		return
	}
	// The knowledge stack is always non-nil: its layers degrade individually
	// (no embedder → BM25-only, no endpoint → fusion order), so review gets a
	// working retriever instead of the nil it used to receive unconditionally.
	r.Register("review", adaptWorkflowWorker("review",
		review.NewWorker(cfg, llm, buildKnowledgeStack(), review.WithJournal(journal))))
}

// Knowledge stack environment: one root for the document store, optional
// embedding and rerank endpoints. See internal/agent/rag for the degrade rules.
const (
	envKnowledgeDir     = "FORGE_KNOWLEDGE_DIR"
	defaultKnowledgeDir = ".forge/knowledge"
)

// buildKnowledgeStack assembles file store + embedder + reranker from the
// environment. Nothing here is fatal when absent; the pipeline reports its
// actual mode (hybrid / bm25-only / rerank) through SearchMode.
func buildKnowledgeStack() *rag.HybridRetriever {
	dir := envOrDefault(envKnowledgeDir, defaultKnowledgeDir)
	store := rag.NewFileDocumentStore(dir)
	return rag.NewHybridRetriever(store, rag.EmbedderFromEnv()).WithReranker(rag.RerankerFromEnv())
}

// registerDatabase wires the database worker. The connector is real (pgxpool,
// lazily dialled on first query), so the only requirement is configuration:
// fill in a DSN and the worker runs queries. Until then this reports exactly
// which variable would enable it instead of failing with "unknown handler".
func registerDatabase(r *worker.Registry) {
	cfg := database.ConfigFromEnv()
	if cfg == nil || cfg.Postgres == nil {
		r.Register("database", unconfiguredHandler("database",
			fmt.Sprintf("no PostgreSQL configured; set %s (a full DSN) or %s to enable query_pg",
				envPGDSN, database.EnvPGHost)))
		return
	}
	r.Register("database", adaptWorkflowWorker("database", database.NewWorker(cfg, database.NewPoolConnector())))
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
	// N2b decision 9: streaming on unless explicitly off; a typo leans toward
	// "on" because streaming degrades to buffered responses on its own, while
	// the benefit (no whole-response timeout) is what is lost when off.
	cfg.Streaming = llmStreamEnabled()
	return harness.NewLLMClient(cfg)
}

// llmStreamEnabled resolves FORGE_LLM_STREAM: off/0/false/no disable;
// anything else keeps streaming on, with a warning for values that are not
// recognised affirmative answers (a typo must not silently disable it either).
func llmStreamEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(envLLMStream))) {
	case "off", "0", "false", "no":
		return false
	case "", "on", "1", "true", "yes":
		return true
	default:
		log.Printf("WARN: unknown %s %q (want on|off); keeping streaming on", envLLMStream, os.Getenv(envLLMStream))
		return true
	}
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
