package runtimegate

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/castwell/forge/internal/forgex/model"
	"github.com/castwell/forge/internal/forgex/policy"
	"github.com/castwell/forge/internal/forgex/storage"
	"github.com/castwell/forge/internal/forgex/toolgw"
	"github.com/castwell/forge/internal/worker"
)

// Config wires ForgeX policy/gate decisions into Forge worker execution.
type ReviewResolver interface {
	LatestReview(ctx context.Context, runID string, gateID string) (model.HITLReview, bool, error)
}

type Config struct {
	Mode       model.GateMode
	Authority  policy.AuthorityLevel
	Contracts  map[string]toolgw.ToolContract
	Store      storage.Store
	Reviews    ReviewResolver
	CreateHITL bool
	Now        func() time.Time
}

// Gate is the concrete RuntimeGate adapter used by Forge workers.
type Gate struct {
	cfg    Config
	engine *policy.Engine
}

// New creates a runtime gate. Nil/empty config defaults to shadow mode.
func New(cfg Config) *Gate {
	if cfg.Mode == "" {
		cfg.Mode = model.GateModeShadow
	}
	if cfg.Authority == "" {
		cfg.Authority = policy.AuthorityL0
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Gate{cfg: cfg, engine: policy.NewEngine(nil)}
}

// BeforeExecute evaluates one worker task before the handler is invoked.
func (g *Gate) BeforeExecute(ctx context.Context, req worker.GateRequest) (worker.GateDecision, error) {
	if g == nil {
		return worker.GateDecision{Action: worker.GateActionAllow}, nil
	}
	runID := runID(req)
	gateID := gateID(runID, req.TaskID)
	if decision, ok, err := g.reviewDecision(ctx, runID, gateID); err != nil || ok {
		return decision, err
	}
	contract := g.contractFor(req)
	policyDecision := g.engine.Decide(runID, g.cfg.Authority, contract)
	gateDecision := model.GateDecision{
		ID:         gateID,
		RunID:      runID,
		Mode:       g.cfg.Mode,
		Action:     gateActionFromPolicy(policyDecision.Action),
		Scope:      "worker_task",
		SubjectID:  req.TaskID,
		Reason:     policyDecision.Reason,
		Evidence:   []string{policyDecision.ID},
		Source:     "forge_worker_runtime_gate",
		NeedsHuman: policyDecision.RequiresHITL,
		CreatedAt:  g.cfg.Now().UTC(),
	}
	if err := g.persist(ctx, gateDecision); err != nil {
		return worker.GateDecision{}, err
	}
	return worker.GateDecision{
		ID:      gateDecision.ID,
		Action:  workerAction(gateDecision.Action),
		Reason:  gateDecision.Reason,
		Enforce: g.cfg.Mode == model.GateModeEnforce,
	}, nil
}

func (g *Gate) contractFor(req worker.GateRequest) toolgw.ToolContract {
	if g.cfg.Contracts != nil {
		if contract, ok := g.cfg.Contracts[req.Handler]; ok {
			return contract
		}
	}
	return toolgw.ToolContract{
		Name:       req.Handler,
		Capability: "forge_worker_handler",
		RiskLevel:  toolgw.RiskLow,
		SideEffect: toolgw.SideEffectLocalWrite,
		Idempotent: false,
	}
}

func (g *Gate) reviewDecision(ctx context.Context, runID string, gateID string) (worker.GateDecision, bool, error) {
	if g.cfg.Reviews == nil {
		return worker.GateDecision{}, false, nil
	}
	review, ok, err := g.cfg.Reviews.LatestReview(ctx, runID, gateID)
	if err != nil || !ok {
		return worker.GateDecision{}, ok, err
	}
	switch review.Status {
	case model.HITLReviewApproved, model.HITLReviewContinued:
		return worker.GateDecision{ID: gateID, Action: worker.GateActionAllow, Reason: "approved by HITL review", Enforce: g.cfg.Mode == model.GateModeEnforce}, true, nil
	case model.HITLReviewRejected:
		return worker.GateDecision{ID: gateID, Action: worker.GateActionBlock, Reason: "rejected by HITL review", Enforce: g.cfg.Mode == model.GateModeEnforce}, true, nil
	case model.HITLReviewPending:
		return worker.GateDecision{ID: gateID, Action: worker.GateActionPause, Reason: "waiting for HITL review", Enforce: g.cfg.Mode == model.GateModeEnforce}, true, nil
	default:
		return worker.GateDecision{}, false, nil
	}
}

func (g *Gate) persist(ctx context.Context, decision model.GateDecision) error {
	if g.cfg.Store == nil {
		return nil
	}
	if err := g.cfg.Store.AppendGateDecision(ctx, decision); err != nil {
		return fmt.Errorf("append gate decision: %w", err)
	}
	if decision.NeedsHuman && g.cfg.CreateHITL {
		if err := g.cfg.Store.AppendHITLReview(ctx, model.HITLReview{RunID: decision.RunID, GateID: decision.ID, Status: model.HITLReviewPending, Reason: "runtime gate requires human review", CreatedAt: decision.CreatedAt}); err != nil {
			return fmt.Errorf("append hitl review: %w", err)
		}
	}
	return nil
}

func gateID(runID string, taskID string) string {
	base := strings.TrimSpace(taskID)
	if base == "" {
		base = "task"
	}
	return "gate-" + sanitizeID(runID) + "-" + sanitizeID(base)
}

func sanitizeID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	replacer := strings.NewReplacer("\\", "-", "/", "-", " ", "-", ":", "-", "\t", "-")
	return replacer.Replace(value)
}

func runID(req worker.GateRequest) string {
	if strings.TrimSpace(req.WorkflowID) != "" {
		return req.WorkflowID
	}
	if strings.TrimSpace(req.TaskID) != "" {
		return req.TaskID
	}
	return "adhoc"
}

func gateActionFromPolicy(action policy.Action) model.GateAction {
	switch action {
	case policy.ActionAllow, policy.ActionDryRunOnly:
		return model.GateActionAllow
	case policy.ActionDeny:
		return model.GateActionBlock
	case policy.ActionRequireApproval, policy.ActionPause:
		return model.GateActionPause
	case policy.ActionEscalate:
		return model.GateActionEscalate
	default:
		return model.GateActionPause
	}
}

func workerAction(action model.GateAction) worker.GateAction {
	switch action {
	case model.GateActionAllow:
		return worker.GateActionAllow
	case model.GateActionBlock:
		return worker.GateActionBlock
	case model.GateActionRetry:
		return worker.GateActionRetry
	case model.GateActionEscalate:
		return worker.GateActionEscalate
	case model.GateActionPause:
		return worker.GateActionPause
	default:
		return worker.GateActionBlock
	}
}

var _ worker.RuntimeGate = (*Gate)(nil)
