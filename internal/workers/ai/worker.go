package ai

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/castwell/forge/internal/agent/core"
	"github.com/castwell/forge/internal/agent/harness"
)

// Worker is the AI workflow worker.
// It creates an Agent Session (ReAct loop) per task execution,
// using the Layer 1 harness with optional RAG, Memory, and Reflexion.
type Worker struct {
	config     Config
	llmFactory LLMFactory
}

// LLMFactory creates an LLM client with the given model config.
// This allows the worker to switch models per action.
type LLMFactory func(cfg ModelConfig) (core.LLMClient, error)

// NewWorker creates an AI Worker with the given config and LLM factory.
func NewWorker(cfg Config, factory LLMFactory) *Worker {
	return &Worker{
		config:     cfg,
		llmFactory: factory,
	}
}

// Execute runs an AI action with the given parameters.
func (w *Worker) Execute(ctx context.Context, action string, params map[string]any) (string, error) {
	switch action {
	case "analyze":
		return w.runAgent(ctx, action, params, analyzeSystemPrompt)
	case "synthesize":
		return w.runAgent(ctx, action, params, synthesizeSystemPrompt)
	case "classify":
		return w.runAgent(ctx, action, params, classifySystemPrompt)
	case "summarize":
		return w.runAgent(ctx, action, params, summarizeSystemPrompt)
	case "generate_code_plan":
		return w.runAgent(ctx, action, params, codePlanSystemPrompt)
	default:
		return "", fmt.Errorf("ai worker: unknown action %q", action)
	}
}

// runAgent creates a fresh Agent Session and runs the ReAct loop.
func (w *Worker) runAgent(ctx context.Context, action string, params map[string]any, systemPrompt string) (string, error) {
	// Resolve model config for this action.
	modelCfg := w.config.ModelForAction(action)

	// Create LLM client with action-specific model.
	llm, err := w.llmFactory(modelCfg)
	if err != nil {
		return "", fmt.Errorf("ai worker: create llm for %s: %w", action, err)
	}

	// Build the ReAct loop (no tools for pure AI tasks — just LLM reasoning).
	loopCfg := harness.LoopConfig{
		MaxSteps:         w.config.MaxSteps,
		MaxContextTokens: w.config.MaxContextTokens,
		SystemPrompt:     systemPrompt,
	}
	registry := core.NewToolRegistry() // empty registry — AI worker doesn't use tools
	router := harness.NewToolRouter(registry)
	loop := harness.NewAgentLoop(llm, router, loopCfg)

	// Extract user input from params.
	input, err := w.buildInput(params)
	if err != nil {
		return "", fmt.Errorf("ai worker: build input for %s: %w", action, err)
	}

	// Run the agent loop.
	result, err := loop.Run(ctx, fmt.Sprintf("ai-%s", action), input)
	if err != nil {
		return "", fmt.Errorf("ai worker: run %s: %w", action, err)
	}

	return result.Answer, nil
}

// buildInput constructs the user prompt from params.
func (w *Worker) buildInput(params map[string]any) (string, error) {
	// "prompt" is the primary input field.
	if prompt, ok := params["prompt"].(string); ok && prompt != "" {
		// If there's additional "context", append it.
		if ctxStr, ok := params["context"].(string); ok && ctxStr != "" {
			return fmt.Sprintf("%s\n\n---\nContext:\n%s", prompt, ctxStr), nil
		}
		return prompt, nil
	}

	// Fallback: serialize all params as JSON input.
	data, err := json.Marshal(params)
	if err != nil {
		return "", fmt.Errorf("marshal params: %w", err)
	}
	return string(data), nil
}
