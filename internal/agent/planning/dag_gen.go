package planning

import (
	"context"
	"fmt"

	"github.com/castwell/forge/internal/agent/core"
	"github.com/castwell/forge/internal/coordinator"
)

const (
	// maxRetries is how many times a DAG that fails validation is sent back to
	// the model. Three is the spec's figure: two attempts catch formatting and
	// schema slips, the third catches a model that needed the error text twice.
	maxRetries = 3
)

// DAGGenerator produces a validated DAG from a requirement, in three strategies:
//
//  1. template  — a domain's pre-built shape (fast, always valid)
//  2. llm       — generated and validated, with errors fed back on retry
//  3. fallback  — a single executor step (always valid, loses the step breakdown)
//
// The order is by cost, and the fallback is what makes the chain total: a
// requirement that a model cannot plan still runs, as one task, rather than
// failing to start. Losing the breakdown is a real loss and the strategy is
// reported so a caller can see it happened.
type DAGGenerator struct {
	planner   *TaskPlanner
	validator *DAGValidator
	llmClient core.LLMClient
	catalog   *HandlerCatalog
}

// NewDAGGenerator creates a generator for a domain. A nil profile means the
// generic one; a nil catalog means no handlers are known, which makes every
// generated step fail L3 — so assembly is expected to supply one.
func NewDAGGenerator(llm core.LLMClient, catalog *HandlerCatalog, profile DomainProfile) *DAGGenerator {
	return &DAGGenerator{
		planner:   NewTaskPlanner(llm, catalog, profile),
		validator: NewDAGValidator(catalog),
		llmClient: llm,
		catalog:   catalog,
	}
}

// GenerateResult holds the result of DAG generation.
type GenerateResult struct {
	DAG      *coordinator.DAG
	YAML     string
	Strategy string // "template", "llm", "fallback"
	Retries  int    // number of LLM retries used
}

// Generate produces a validated DAG. The strategies are tried in order, and the
// first that validates wins.
func (g *DAGGenerator) Generate(ctx context.Context, req *Requirement) (*GenerateResult, error) {
	// Strategy 1: a domain template that fits.
	for _, tmpl := range g.planner.Templates() {
		if tmpl.Match != nil && !tmpl.Match(req) {
			continue
		}
		yamlStr, err := tmpl.Build(req)
		if err != nil {
			return nil, fmt.Errorf("generate DAG: template %s: %w", tmpl.Name, err)
		}
		result := g.validator.Validate(yamlStr)
		if result.Valid && result.DAG != nil {
			return &GenerateResult{
				DAG:      result.DAG,
				YAML:     yamlStr,
				Strategy: "template",
			}, nil
		}
		// A template that produces an invalid DAG is a defect in the template,
		// not in the requirement. Falling through to the model would hide it, so
		// it is reported as an error — but only after the model has had its
		// chance, because a requirement the template half-matches is still worth
		// planning.
		if err := g.llmFallback(ctx, req, result); err != nil {
			return nil, fmt.Errorf("generate DAG: template %s produced an invalid DAG (%s) and LLM fallback failed: %w",
				tmpl.Name, result.ErrorSummary(), err)
		}
		break
	}

	// Strategy 2: the model, with validation and error feedback.
	var lastErrors string
	for attempt := 0; attempt <= maxRetries; attempt++ {
		yamlStr, err := g.planner.planWithLLM(ctx, req, lastErrors)
		if err != nil {
			return nil, fmt.Errorf("generate DAG (attempt %d): %w", attempt, err)
		}

		result := g.validator.Validate(yamlStr)
		if result.Valid && result.DAG != nil {
			return &GenerateResult{
				DAG:      result.DAG,
				YAML:     yamlStr,
				Strategy: "llm",
				Retries:  attempt,
			}, nil
		}
		lastErrors = result.ErrorSummary()
	}

	// Strategy 3: one executor step. Always valid, because it names no handler
	// the catalog might not have beyond the executor itself.
	yamlStr := g.buildFallbackDAG(req)
	dag, err := coordinator.ParseDAG([]byte(yamlStr))
	if err != nil {
		return nil, fmt.Errorf("generate DAG: fallback parse failed: %w", err)
	}

	return &GenerateResult{
		DAG:      dag,
		YAML:     yamlStr,
		Strategy: "fallback",
		Retries:  maxRetries,
	}, nil
}

// llmFallback is a small helper so the template path can hand over to the model
// without duplicating the retry loop. It reports whether the model produced a
// usable DAG, leaving the caller to decide what a failure means.
func (g *DAGGenerator) llmFallback(ctx context.Context, req *Requirement, first *ValidationResult) error {
	lastErrors := first.ErrorSummary()
	for attempt := 0; attempt <= maxRetries; attempt++ {
		yamlStr, err := g.planner.planWithLLM(ctx, req, lastErrors)
		if err != nil {
			return err
		}
		result := g.validator.Validate(yamlStr)
		if result.Valid && result.DAG != nil {
			return nil
		}
		lastErrors = result.ErrorSummary()
	}
	return fmt.Errorf("model did not produce a valid DAG in %d attempts: %s", maxRetries+1, lastErrors)
}

// buildFallbackDAG renders the single-step DAG.
//
// It is written as a struct rather than with Sprintf so the YAML is escaped by
// the marshaller: a requirement whose description contains a colon or a quote
// would otherwise produce YAML that does not parse — and this is the path taken
// exactly when things have already gone wrong.
func (g *DAGGenerator) buildFallbackDAG(req *Requirement) string {
	acceptance := acceptanceText(req.Acceptance)
	task := req.Description
	if task == "" {
		task = "完成需求描述中要求的工作"
	}

	dag := dagYAML{
		Name: "fallback-plan",
		Tasks: map[string]taskYAML{
			"work": {
				Handler: agentHandler,
				Params:  agentParams(task, acceptance),
				Timeout: "30m",
			},
		},
	}

	out, err := marshalDAG(dag)
	if err != nil {
		// Should not happen with well-formed structs; if it does, the caller
		// gets YAML that fails to parse rather than silent nonsense.
		return fmt.Sprintf("# marshal error: %v", err)
	}
	return out
}
