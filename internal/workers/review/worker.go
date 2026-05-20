// Package review implements the Review Worker for Forge workflows.
// It combines AI Worker capabilities with RAG retrieval to perform
// code and plan reviews against project conventions and history.
package review

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/castwell/forge/internal/agent/core"
	"github.com/castwell/forge/internal/agent/harness"
	"github.com/castwell/forge/internal/agent/rag"
)

// Config holds Review Worker configuration.
type Config struct {
	// Model config for the review LLM.
	Model       string
	Temperature float64
	MaxTokens   int

	// MaxSteps for the ReAct loop.
	MaxSteps         int
	MaxContextTokens int

	// TopK for RAG retrieval.
	RAGTopK int
}

// DefaultConfig returns sensible defaults for code review.
func DefaultConfig() Config {
	return Config{
		Model:            "claude-sonnet-4-20250514",
		Temperature:      0.1, // low creativity for reviews
		MaxTokens:        8192,
		MaxSteps:         5,
		MaxContextTokens: 100000,
		RAGTopK:          10,
	}
}

// Worker is the Review workflow worker.
type Worker struct {
	config    Config
	llm       core.LLMClient
	retriever *rag.HybridRetriever // optional, nil if no knowledge base
}

// NewWorker creates a Review Worker.
func NewWorker(cfg Config, llm core.LLMClient, retriever *rag.HybridRetriever) *Worker {
	return &Worker{
		config:    cfg,
		llm:       llm,
		retriever: retriever,
	}
}

// Execute runs a review action.
func (w *Worker) Execute(ctx context.Context, action string, params map[string]any) (string, error) {
	switch action {
	case "review_plan":
		return w.reviewPlan(ctx, params)
	case "review_code":
		return w.reviewCode(ctx, params)
	case "review_security":
		return w.reviewSecurity(ctx, params)
	default:
		return "", fmt.Errorf("review worker: unknown action %q", action)
	}
}

// reviewPlan reviews an implementation plan for completeness and risks.
func (w *Worker) reviewPlan(ctx context.Context, params map[string]any) (string, error) {
	plan, err := getParam(params, "plan")
	if err != nil {
		return "", err
	}

	context_str := w.retrieveContext(ctx, "code review conventions plan review checklist")
	prompt := buildReviewPlanPrompt(plan, context_str)
	return w.runReview(ctx, "review_plan", prompt)
}

// reviewCode reviews code changes (diff) for quality issues.
func (w *Worker) reviewCode(ctx context.Context, params map[string]any) (string, error) {
	diff, err := getParam(params, "diff")
	if err != nil {
		return "", err
	}
	planCtx := getOptionalParam(params, "plan_context", "")

	context_str := w.retrieveContext(ctx, "code review standards error handling patterns")
	prompt := buildReviewCodePrompt(diff, planCtx, context_str)
	return w.runReview(ctx, "review_code", prompt)
}

// reviewSecurity performs a security-focused review.
func (w *Worker) reviewSecurity(ctx context.Context, params map[string]any) (string, error) {
	diff, err := getParam(params, "diff")
	if err != nil {
		return "", err
	}

	context_str := w.retrieveContext(ctx, "security vulnerabilities injection XSS auth bypass")
	prompt := buildSecurityReviewPrompt(diff, context_str)
	return w.runReview(ctx, "review_security", prompt)
}

// runReview executes the agent loop for a review task.
func (w *Worker) runReview(ctx context.Context, action, prompt string) (string, error) {
	loopCfg := harness.LoopConfig{
		MaxSteps:         w.config.MaxSteps,
		MaxContextTokens: w.config.MaxContextTokens,
		SystemPrompt:     reviewSystemPrompt,
	}
	registry := core.NewToolRegistry()
	router := harness.NewToolRouter(registry)
	loop := harness.NewAgentLoop(w.llm, router, loopCfg)

	result, err := loop.Run(ctx, fmt.Sprintf("review-%s", action), prompt)
	if err != nil {
		return "", fmt.Errorf("review worker: %s: %w", action, err)
	}
	return result.Answer, nil
}

// retrieveContext uses RAG to fetch relevant conventions/history.
func (w *Worker) retrieveContext(ctx context.Context, query string) string {
	if w.retriever == nil {
		return ""
	}
	results, err := w.retriever.Search(ctx, query, w.config.RAGTopK)
	if err != nil || len(results) == 0 {
		return ""
	}
	// Format results as context block.
	var chunks []string
	for _, r := range results {
		chunks = append(chunks, r.Content)
	}
	data, _ := json.Marshal(chunks)
	return string(data)
}

// getParam extracts a required string parameter.
func getParam(params map[string]any, key string) (string, error) {
	v, ok := params[key].(string)
	if !ok || v == "" {
		return "", fmt.Errorf("missing required param %q", key)
	}
	return v, nil
}

// getOptionalParam extracts an optional string parameter.
func getOptionalParam(params map[string]any, key, defaultVal string) string {
	if v, ok := params[key].(string); ok && v != "" {
		return v
	}
	return defaultVal
}
