package policy

import "github.com/castwell/forge/internal/forgex/model"

// ExecutionMode is what a policy decision MEANS for the run: the single place
// that turns an Action into execution semantics.
//
// It exists because two consumers need the same answer — the worker runtime
// gate (internal/forgex/runtimegate) and the replayable demo path
// (internal/forgex/demo) — and a mapping written twice is a mapping that
// eventually disagrees with itself. The gate keeps its worker-facing
// GateAction vocabulary; this is the shared decision underneath it.
type ExecutionMode string

const (
	// ExecutionProceed: the tool may run.
	ExecutionProceed ExecutionMode = "proceed"
	// ExecutionDryRun: validation and evidence only — the side effect must not
	// happen. The caller still runs the contract checks so the recorded
	// outcome stays meaningful.
	ExecutionDryRun ExecutionMode = "dry_run"
	// ExecutionBlock: the tool must not run at all, and the run does not
	// continue on its own.
	ExecutionBlock ExecutionMode = "block"
	// ExecutionHold: a human decision is required; the run waits.
	ExecutionHold ExecutionMode = "hold"
)

// Mode maps a policy action onto execution semantics.
//
// Unknown actions map to Hold, matching the design rule that an unrecognised
// permission outcome must fail closed — continuing on an outcome nobody
// defined is exactly the silent permissiveness this type removes.
func (a Action) Mode() ExecutionMode {
	switch a {
	case ActionAllow:
		return ExecutionProceed
	case ActionDryRunOnly:
		return ExecutionDryRun
	case ActionDeny:
		return ExecutionBlock
	case ActionRequireApproval, ActionPause, ActionEscalate:
		return ExecutionHold
	default:
		return ExecutionHold
	}
}

// StopAction is the termination signal this mode implies. It is derived from
// the same action rather than decided again by each caller.
func (a Action) StopAction() model.StopAction {
	switch a.Mode() {
	case ExecutionProceed, ExecutionDryRun:
		return model.StopActionContinue
	case ExecutionBlock:
		return model.StopActionStop
	default:
		return model.StopActionPause
	}
}

// AllowsExecution reports whether the caller may invoke the tool. Dry runs do
// not execute: they keep the validation half and drop the side effect.
func (a Action) AllowsExecution() bool { return a.Mode() == ExecutionProceed }
