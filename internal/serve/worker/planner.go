package worker

import (
	"log"
	"os"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/castwell/forge/internal/agent/core"
	"github.com/castwell/forge/internal/agent/harness"
	"github.com/castwell/forge/internal/agent/planning"
	"github.com/castwell/forge/internal/agent/session"
	"github.com/castwell/forge/internal/grpcauth"
	"github.com/castwell/forge/internal/worker"
	planworker "github.com/castwell/forge/internal/workers/plan"
)

// Environment variables that configure the planner worker.
//
//	FORGE_PLANNER_ENABLED        1 = register the planner handler (default off)
//	FORGE_PLANNER_LLM_API_KEY    key for the model that plans (falls back to
//	                             FORGE_LLM_API_KEY: a deployment with a model has
//	                             one key, and demanding a second to use it would
//	                             be friction with no benefit)
//	FORGE_PLANNER_LLM_BASE_URL   endpoint override
//	FORGE_PLANNER_LLM_MODEL      model name (planning often deserves a stronger
//	                             model than the executing steps need)
//
// Off by default, the same posture as every other optional capability here: the
// handler submits child workflows, so enabling it changes what a workflow can
// cause to happen.
const (
	envPlannerEnabled = "FORGE_PLANNER_ENABLED"
	envPlannerAPIKey  = "FORGE_PLANNER_LLM_API_KEY"
	envPlannerBaseURL = "FORGE_PLANNER_LLM_BASE_URL"
	envPlannerModel   = "FORGE_PLANNER_LLM_MODEL"

	// plannerTemperature is low: planning is a structured-output task with a
	// right answer, and a creative planner produces DAGs that need retrying.
	plannerTemperature = 0.2
	// plannerMaxTokens bounds the plan. A DAG is a few dozen lines; a response
	// far longer than that is the model explaining itself, which the prompt
	// already asks it not to do.
	plannerMaxTokens = 8192
)

// registerPlannerHandler wires the plan-and-execute worker when enabled.
//
// This is the entry point the design was missing: it turns a requirement into a
// DAG, submits that DAG as a child workflow, and waits for it — the
// Plan-and-Execute half of "Plan-and-Execute + ReAct", of which only the ReAct
// half had ever been reachable.
//
// Not registering when disabled is deliberate rather than lazy: a workflow
// declaring `handler: planner` on a deployment that did not enable it must fail
// with "unknown handler", which is the honest answer.
func registerPlannerHandler(r *worker.Registry, coordAddr string) {
	if !envTruthy(envPlannerEnabled) {
		return
	}

	llm := newPlannerLLM()
	if llm == nil {
		// Name what is missing rather than registering a handler that fails on
		// first use: the operator asked for this capability, so the gap belongs
		// at startup where it can be fixed.
		log.Printf("WARN: %s is set but no LLM client could be built; set %s (or %s) to enable the planner handler",
			envPlannerEnabled, envPlannerAPIKey, envLLMAPIKey)
		return
	}

	// grpc.NewClient is lazy: it does not connect here, so an unreachable
	// coordinator does not stop the worker from starting. The first plan fails
	// instead, which is the same posture as every other optional dependency.
	//
	// The secret travels with the client when the deployment has one: the planner
	// submits child workflows, so it calls the coordinator exactly like a CLI
	// operator would.
	dialOpts := append([]grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())},
		grpcauth.ClientOptions()...)
	conn, err := grpc.NewClient(coordAddr, dialOpts...)
	if err != nil {
		log.Printf("WARN: planner handler not registered: dial coordinator %s: %v", coordAddr, err)
		return
	}

	client := session.NewForgeClient(conn)

	// The catalog holds what this deployment can dispatch, and it is the same
	// list the planner prompt describes. Built from one source
	// (GeneratedHandlerSpecs) rather than written out here: two hand-kept lists
	// is how the previous design came to validate generated plans against a
	// vocabulary that had nothing to do with dispatch.
	catalog := planning.NewHandlerCatalog(planning.GeneratedHandlerSpecs())

	// The generic profile: no domain, because this handler exists for
	// requirements nobody has written a domain for. A deployment with a domain
	// supplies its own by calling the runner directly.
	generator := planning.NewDAGGenerator(llm, catalog, nil)
	runner := planning.NewRunner(generator, client.AsSubmitter())

	r.Register("planner", adaptWorkflowWorker("planner", planworker.NewWorker(runner)))
	log.Printf("INFO: planner handler registered (submits child workflows to %s)", coordAddr)
}

// newPlannerLLM builds the model client the planner uses, or nil when no key is
// configured.
func newPlannerLLM() core.LLMClient {
	apiKey := os.Getenv(envPlannerAPIKey)
	if apiKey == "" {
		apiKey = os.Getenv(envLLMAPIKey)
	}
	if apiKey == "" {
		return nil
	}

	cfg := harness.DefaultLLMConfig()
	cfg.APIKey = apiKey
	if baseURL := os.Getenv(envPlannerBaseURL); baseURL != "" {
		cfg.BaseURL = baseURL
	}
	if model := os.Getenv(envPlannerModel); model != "" {
		cfg.Model = model
	}
	cfg.Temperature = plannerTemperature
	cfg.MaxTokens = plannerMaxTokens
	// Streaming follows the same switch as every other LLM call in the worker:
	// a deployment that turned it off has a reason, and planning is not exempt.
	cfg.Streaming = llmStreamEnabled()

	return harness.NewLLMClient(cfg)
}
