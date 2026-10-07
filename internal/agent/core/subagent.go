package core

// Subagent modes (N5).
//
// The point of this file is to NOT pick a shape. Delegation has at least three
// independent questions, and answering them with one enum would decide for the
// deployment:
//
//	1. How does one delegation run?      -> SubagentMode
//	2. How much comes back to the parent? -> SubagentReport
//	3. How may delegations nest/fan out?  -> depth + concurrency limits
//
// They are orthogonal on purpose: a deployment may want a one-shot child that
// still reports its steps (debugging), or a continuable child that reports only
// its answer (long-running work that must not flood the parent's window). Any
// single "mode" string would have made one of those combinations unexpressible.
//
// The defaults are all "off"/"minimum": not configuring this feature must cost
// nothing, the same way an unconfigured webhook listener or Kueue queue does.

// SubagentMode is how a single delegation runs.
type SubagentMode string

const (
	// SubagentOff disables delegation entirely: the tool is not registered.
	// This is the default, and it is what keeps a lightweight deployment
	// lightweight — no tool, no schema cost, no prompt cost.
	SubagentOff SubagentMode = "off"
	// SubagentIsolated runs the child once in its own session and context.
	// The child's conversation never enters the parent's: that isolation is the
	// whole point, because it is what lets a context-heavy subtask run without
	// consuming the parent's window.
	SubagentIsolated SubagentMode = "isolated"
	// SubagentContinuable keeps the child's session after the first round, so a
	// later call can give it more work. It costs a persisted session per child
	// and is meant for long-running work, not for a quick lookup.
	SubagentContinuable SubagentMode = "continuable"
)

// NormalizeSubagentMode resolves a configured value, defaulting to Off.
//
// An unrecognised value is Off, not Isolated: a typo in a deployment variable
// must not silently enable a capability the operator did not ask for. The
// caller logs the fallback (see the serve layer), so it is visible rather than
// silent.
func NormalizeSubagentMode(raw string) SubagentMode {
	switch SubagentMode(raw) {
	case SubagentIsolated:
		return SubagentIsolated
	case SubagentContinuable:
		return SubagentContinuable
	default:
		return SubagentOff
	}
}

// Enabled reports whether delegation is available at all.
func (m SubagentMode) Enabled() bool {
	return m == SubagentIsolated || m == SubagentContinuable
}

// Continuable reports whether a child session survives its first round.
func (m SubagentMode) Continuable() bool { return m == SubagentContinuable }

// SubagentReport is how much of the child's work travels back to the parent.
//
// This is the second orthogonal axis. "Return the steps" is not simply "more
// helpful": the steps ARE context, so returning them spends the parent's window
// on the child's reasoning — which is exactly what isolation was meant to
// avoid. Making it a setting lets a debugging deployment see everything and a
// production one keep its window clean.
type SubagentReport string

const (
	// SubagentReportAnswer returns only the child's final answer: maximum
	// isolation, the child's process stays entirely its own.
	SubagentReportAnswer SubagentReport = "answer"
	// SubagentReportSummary returns the answer plus a one-line trace of what
	// the child did (thought + tool name per step, no tool output). Enough to
	// audit the shape of the work without importing its bulk. This is the
	// default because "what did it do" is the question a parent actually asks.
	SubagentReportSummary SubagentReport = "summary"
	// SubagentReportSteps returns the answer plus the full step records. The
	// parent sees everything, and pays for it in its own context — choose it
	// deliberately (debugging, or a child whose work the parent must judge).
	SubagentReportSteps SubagentReport = "steps"
)

// NormalizeSubagentReport resolves a configured value, defaulting to Summary.
func NormalizeSubagentReport(raw string) SubagentReport {
	switch SubagentReport(raw) {
	case SubagentReportAnswer:
		return SubagentReportAnswer
	case SubagentReportSteps:
		return SubagentReportSteps
	default:
		return SubagentReportSummary
	}
}

// SubagentLimits bounds the delegation tree.
//
// Depth and concurrency are separate because they fail differently: unbounded
// depth is infinite recursion, unbounded concurrency is resource exhaustion.
// Both are checked at delegation time rather than at registration, so the tool
// stays visible and a refused delegation is an ordinary tool error the model can
// read and react to (the same choice DSH makes: "the tool stays visible at the
// limit; each start attempt checks the caller's current depth").
type SubagentLimits struct {
	// MaxDepth is how many levels of delegation are allowed. 0 forbids
	// delegation (the tool refuses every call); 1 allows a parent to delegate
	// but not a child; 2 allows grandchildren, and so on.
	MaxDepth int
	// MaxConcurrent is how many children one parent may run at once. 1 means
	// delegations run one at a time (a batch of subagent calls executes
	// sequentially).
	MaxConcurrent int
	// ChildMaxSteps bounds a child run's own step count. Zero means "derive
	// from the parent" (see DefaultChildSteps) — a child must not be able to
	// spend the parent's entire step budget on one subtask.
	ChildMaxSteps int
}

// Delegation budget defaults.
const (
	// DefaultSubagentMaxDepth allows one level: the top-level agent may
	// delegate, and a child may not delegate again. Recursion is the failure
	// this prevents, so the safe default is the shallow one.
	DefaultSubagentMaxDepth = 1
	// DefaultSubagentMaxConcurrent runs delegations one at a time. Fanning out
	// is a deliberate choice (it multiplies provider load), not a default.
	DefaultSubagentMaxConcurrent = 1
	// DefaultChildStepsFloor is the smallest child budget worth running. A
	// derived budget below this is raised to it: a child given one or two steps
	// cannot do anything, so the limit would be a trap rather than a guard.
	DefaultChildStepsFloor = 3
)

// DefaultChildSteps derives a child's step budget from the parent's.
//
// Half the parent's budget, floored: a child should be able to do real work,
// and must not be able to consume everything the parent has. A parent with no
// configured limit (0) leaves the child unlimited too — imposing one there
// would invent a constraint the deployment never chose.
func DefaultChildSteps(parentMaxSteps int) int {
	if parentMaxSteps <= 0 {
		return 0
	}
	half := parentMaxSteps / 2
	if half < DefaultChildStepsFloor {
		return DefaultChildStepsFloor
	}
	return half
}

// SubagentConfig is the resolved delegation configuration.
type SubagentConfig struct {
	Mode   SubagentMode
	Report SubagentReport
	Limits SubagentLimits
	// Tools narrows what a child may use. Empty means "inherit the parent's
	// tool surface", which is the least surprising default: a child is asked to
	// do a job, and the parent already knows which tools that takes.
	Tools []string
}

// NormalizeSubagentConfig fills in defaults and normalizes each field.
func NormalizeSubagentConfig(cfg SubagentConfig) SubagentConfig {
	cfg.Mode = NormalizeSubagentMode(string(cfg.Mode))
	cfg.Report = NormalizeSubagentReport(string(cfg.Report))
	if cfg.Limits.MaxDepth < 0 {
		cfg.Limits.MaxDepth = 0
	}
	if cfg.Limits.MaxConcurrent <= 0 {
		cfg.Limits.MaxConcurrent = DefaultSubagentMaxConcurrent
	}
	return cfg
}
