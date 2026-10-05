package coordinator

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/castwell/forge/internal/agent/mcp"
	"github.com/castwell/forge/internal/coordinator"
	"github.com/castwell/forge/internal/registry"
	"github.com/castwell/forge/internal/scheduler"
)

// Environment variables that configure workflow triggers (the triggers: block
// already declared in workflow YAML).
//
//	FORGE_WORKFLOW_TRIGGERS   1 = wire declared triggers (default off: a
//	                          deployment without external systems starts
//	                          exactly as before)
//	FORGE_FEISHU_MCP_ENDPOINT / FORGE_FEISHU_MCP_TOKEN  required by feishu sources
//	FORGE_TRIGGER_DEDUP_TTL   poll dedup memory window (default 72h)
const (
	envWorkflowTriggers  = "FORGE_WORKFLOW_TRIGGERS"
	envFeishuMCPEndpoint = "FORGE_FEISHU_MCP_ENDPOINT"
	envFeishuMCPToken    = "FORGE_FEISHU_MCP_TOKEN"
	envTriggerDedupTTL   = "FORGE_TRIGGER_DEDUP_TTL"
	defaultDedupTTL      = 72 * time.Hour
)

// setupTriggers wires the workflow declarations from YAML — "轮询间隔与去重键
// 都在 YAML 里声明" (README 闭环①) — into the two executors that already
// existed but had no callers: the cron scheduler and the poll scheduler.
//
// Off by default (zero change), on explicitly: with the gate open, a source
// that cannot work fails startup with the missing configuration named, rather
// than registering a trigger that silently never fires.
func setupTriggers(ctx context.Context, coord *coordinator.Coordinator) (func(), error) {
	noop := func() {}
	if !envBool(envWorkflowTriggers) {
		return noop, nil
	}

	workflowsDir := envOrDefault(envWorkflowsDir, defaultWorkflows)
	reg := registry.NewRegistry()
	if err := reg.Load(workflowsDir); err != nil {
		return nil, fmt.Errorf("load workflows from %s: %w", workflowsDir, err)
	}

	cronSched, pollSched, err := buildTriggerWiring(ctx, reg, coord)
	if err != nil {
		return nil, err
	}
	if cronSched == nil && pollSched == nil {
		log.Printf("INFO: workflow triggers enabled but no cron/poll triggers declared in %s", workflowsDir)
		return noop, nil
	}

	if cronSched != nil {
		cronSched.Start()
	}
	if pollSched != nil {
		go pollSched.Start(ctx) // blocks until ctx is cancelled
	}

	var stopped bool
	return func() {
		if stopped {
			return
		}
		stopped = true
		if cronSched != nil {
			cronSched.Stop()
		}
		if pollSched != nil {
			pollSched.Stop()
		}
	}, nil
}

// buildTriggerWiring converts declared triggers into running schedulers.
// A nil/nil result means "nothing declared" — not an error.
func buildTriggerWiring(ctx context.Context, reg *registry.Registry, coord *coordinator.Coordinator) (*coordinator.CronScheduler, *scheduler.Scheduler, error) {
	var (
		cronSched   *coordinator.CronScheduler
		pollSched   *scheduler.Scheduler
		cronCount   int
		pollCount   int
		otherLogged bool
	)

	for _, name := range reg.List() {
		cw, err := reg.Get(name)
		if err != nil {
			return nil, nil, fmt.Errorf("registry: %s: %w", name, err)
		}

		for i, tr := range cw.Triggers {
			triggerName := fmt.Sprintf("%s-trigger-%d", cw.Name, i)

			switch tr.Type {
			case "cron":
				if cronSched == nil {
					cronSched = coordinator.NewCronScheduler(coord, nil) // nil lock: single process (see D-28 boundaries)
				}
				cron := &coordinator.CronTrigger{
					ID:           triggerName,
					WorkflowName: cw.Name,
					CronExpr:     tr.Expr,
					Enabled:      true,
					// The registry's stages/worker dialect cannot travel through
					// SubmitWorkflow (tasks/handler dialect): fire through the
					// same bridge the CDC path uses.
					SubmitFn: func(ctx context.Context) error {
						return submitRegistryWorkflow(ctx, reg, coord, cw, triggerInputs(tr, nil))
					},
				}
				if err := cronSched.AddTrigger(cron); err != nil {
					return nil, nil, fmt.Errorf("cron trigger %s on workflow %q: %w", triggerName, cw.Name, err)
				}
				cronCount++

			case "poll":
				if pollSched == nil {
					ttl := defaultDedupTTL
					if raw := strings.TrimSpace(os.Getenv(envTriggerDedupTTL)); raw != "" {
						if d, err := time.ParseDuration(raw); err == nil && d > 0 {
							ttl = d
						} else {
							log.Printf("WARN: invalid %s %q (want a duration like 72h); using %s", envTriggerDedupTTL, raw, defaultDedupTTL)
						}
					}
					pollSched = scheduler.NewScheduler(scheduler.SchedulerConfig{
						Dedup: scheduler.NewInMemoryDedup(ttl),
						Callback: func(ctx context.Context, workflowName string, events []scheduler.Event) error {
							cw, err := reg.Get(workflowName)
							if err != nil {
								return fmt.Errorf("trigger submit: workflow %q: %w", workflowName, err)
							}
							return submitRegistryWorkflow(ctx, reg, coord, cw, triggerInputs(tr, events))
						},
					})
				}
				pollFn, err := pollFnForSource(ctx, tr.Source)
				if err != nil {
					return nil, nil, fmt.Errorf("poll trigger %s on workflow %q: %w", triggerName, cw.Name, err)
				}
				if err := pollSched.Register(scheduler.NewPollTrigger(scheduler.PollTriggerConfig{
					Name:         triggerName,
					Interval:     tr.Interval,
					Source:       tr.Source,
					Query:        tr.Query,
					DedupKey:     tr.DedupKey,
					WorkflowName: cw.Name,
					PollFn:       pollFn,
				})); err != nil {
					return nil, nil, err
				}
				pollCount++

			default:
				// webhook/manual are recognised by the parser; wiring them is a
				// separate entry point. Say it out loud instead of registering a
				// trigger that never fires.
				if !otherLogged {
					log.Printf("INFO: trigger type %q on workflow %q is not wired yet (webhook needs an HTTP entry point; manual fires via the API)", tr.Type, cw.Name)
					otherLogged = true
				}
			}
		}
	}

	if cronCount > 0 || pollCount > 0 {
		log.Printf("INFO: workflow triggers wired (cron=%d poll=%d)", cronCount, pollCount)
	}
	return cronSched, pollSched, nil
}

// triggerInputs shapes what a fired trigger passes to the workflow: raw
// payloads for the batch, plus the workflow's declared inputs rendered
// against them (declared inputs win on collision — same rule as the CDC path).
func triggerInputs(tr registry.CompiledTrigger, events []scheduler.Event) map[string]any {
	payloads := make([]any, 0, len(events))
	for _, e := range events {
		payloads = append(payloads, e.Payload)
	}
	return map[string]any{
		"trigger_source": tr.Source,
		"events":         payloads,
		"event_count":    len(payloads),
	}
}

// submitRegistryWorkflow is the shared fire path: registry dialect in,
// bridged to the coordinator dialect, declared inputs rendered, submitted
// through SubmitDAG (the same persistence body the gRPC path uses).
func submitRegistryWorkflow(ctx context.Context, reg *registry.Registry, coord *coordinator.Coordinator, cw *registry.CompiledWorkflow, rawParams map[string]any) error {
	dag, err := bridgeDAG(cw)
	if err != nil {
		return fmt.Errorf("bridge workflow %q: %w", cw.Name, err)
	}

	merged := make(map[string]any, len(rawParams)+len(cw.Inputs))
	for k, v := range rawParams {
		merged[k] = v
	}
	if len(cw.Inputs) > 0 {
		rendered, err := registry.RenderInputs(cw.Inputs, registry.TemplateContext{"events": rawParams["events"], "trigger_source": rawParams["trigger_source"]})
		if err != nil {
			return fmt.Errorf("render inputs of workflow %q: %w", cw.Name, err)
		}
		for k, v := range rendered {
			merged[k] = v
		}
	}
	body, err := json.Marshal(merged)
	if err != nil {
		return fmt.Errorf("marshal workflow input: %w", err)
	}
	if _, err := coord.SubmitDAG(ctx, dag, body); err != nil {
		return fmt.Errorf("submit workflow %q: %w", cw.Name, err)
	}
	return nil
}

// pollFnForSource resolves a declared source to a PollFunc. A source that
// cannot be polled — unknown name, missing credentials — is an error naming
// what is missing: a trigger registered with a nil poller would fire forever
// and do nothing.
func pollFnForSource(ctx context.Context, source string) (scheduler.PollFunc, error) {
	switch source {
	case "feishu_mcp", "feishu_todo", "feishu_mql":
		endpoint := strings.TrimSpace(os.Getenv(envFeishuMCPEndpoint))
		token := strings.TrimSpace(os.Getenv(envFeishuMCPToken))
		if endpoint == "" || token == "" {
			return nil, fmt.Errorf("feishu source %q needs %s and %s", source, envFeishuMCPEndpoint, envFeishuMCPToken)
		}
		transport := mcp.NewHTTPTransport(mcp.HTTPTransportConfig{Endpoint: endpoint, Token: token})
		poller, err := scheduler.NewFeishuMCPPoller(ctx, transport)
		if err != nil {
			return nil, fmt.Errorf("feishu mcp: %w", err)
		}
		return scheduler.NewFeishuPollFunc(poller), nil
	default:
		return nil, fmt.Errorf("unknown trigger source %q (supported: feishu_mcp, feishu_todo, feishu_mql)", source)
	}
}
