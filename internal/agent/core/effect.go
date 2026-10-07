package core

import (
	"fmt"
	"strings"
)

// Tool effect classes and run authority (N4 + the risk gate).
//
// Two ideas, deliberately kept apart:
//
//   - EFFECT is what a tool does to the world. It is a property of the tool,
//     declared by whoever wrote it, and it is objective: reading a file does
//     not change it, writing one does, deleting one cannot be undone by
//     re-running the tool.
//   - AUTHORITY is how much a run is allowed to do. It is a property of the
//     RUN, not of the tool, and it is a ceiling: no task declaration and no
//     tool can raise it.
//
// The gate combines them: required = the stronger of (task declaration, tool
// effect), then compare against the run's authority. A stricter tool cannot be
// waved through by a lenient task, and a permissive run cannot lift a task's
// own caution — but the ceiling decides, so a low-authority run cannot delete
// a database however its tasks are written.
//
// The vocabulary mirrors ForgeX's authority ladder (L0..L4) and its "high risk
// needs approval by safe default" rule. The code is NOT shared: agent must not
// import forgex (dependency direction), so the semantics are mirrored here on
// purpose. Two planes, one meaning.

// ToolEffect is what a tool does to the world, in increasing severity.
type ToolEffect string

const (
	// EffectRead observes without changing anything. Re-running it is safe and
	// its result can be recomputed.
	EffectRead ToolEffect = "read"
	// EffectWrite changes state. Re-running may or may not be safe, but the
	// change can generally be undone by another write.
	EffectWrite ToolEffect = "write"
	// EffectDelete removes state. This is the class where "undo" stops being
	// an option: re-running cannot restore what is gone, so it is treated as
	// the strictest effect.
	EffectDelete ToolEffect = "delete"
)

// Valid reports whether the effect is one of the known classes.
func (e ToolEffect) Valid() bool {
	switch e {
	case EffectRead, EffectWrite, EffectDelete:
		return true
	default:
		return false
	}
}

// NormalizeEffect trims and lowercases an effect, returning ok=false when the
// value is not a known class. Unknown values are NOT silently mapped: a typo
// in a tool declaration must be reported, not treated as read.
func NormalizeEffect(raw string) (ToolEffect, bool) {
	e := ToolEffect(strings.ToLower(strings.TrimSpace(raw)))
	return e, e.Valid()
}

// effectRank orders effects by severity for comparison.
func effectRank(e ToolEffect) int {
	switch e {
	case EffectRead:
		return 0
	case EffectWrite:
		return 1
	case EffectDelete:
		return 2
	default:
		return -1
	}
}

// StrongerEffect returns whichever effect is more severe, treating an
// UNDECLARED effect as "no opinion" rather than as a fixed class.
//
// The distinction matters: an undeclared TASK effect must not add a
// restriction the task never asked for — if it were read as write, every
// existing workflow (which declares nothing) would suddenly need L3 and start
// pausing. An undeclared TOOL effect is a different case, and the caller
// handles it: a tool whose author said nothing is treated cautiously (see
// EffectiveToolEffect).
func StrongerEffect(a, b ToolEffect) ToolEffect {
	if !a.Valid() {
		return b
	}
	if !b.Valid() {
		return a
	}
	if effectRank(a) >= effectRank(b) {
		return a
	}
	return b
}

// EffectiveToolEffect is the effect a tool is judged at when its author
// declared nothing. The cautious middle (write) is the answer: treating it as
// read would let an undeclared tool run freely, and treating it as delete would
// block harmless tools. A tool that wants to be trusted as read says so.
func EffectiveToolEffect(declared ToolEffect) ToolEffect {
	if !declared.Valid() {
		return EffectWrite
	}
	return declared
}

// Authority is how much a run may do, from suggest-only to long-running
// autonomous. The ladder mirrors ForgeX's L0..L4.
type Authority string

const (
	// AuthorityL0 suggests only: nothing executes.
	AuthorityL0 Authority = "L0"
	// AuthorityL1 generates work for a human to confirm.
	AuthorityL1 Authority = "L1"
	// AuthorityL2 executes read-only work without asking.
	AuthorityL2 Authority = "L2"
	// AuthorityL3 executes writes, with approval for the strictest effects.
	AuthorityL3 Authority = "L3"
	// AuthorityL4 runs autonomously, including deletions.
	AuthorityL4 Authority = "L4"
)

// Valid reports whether the authority is a known rung.
func (a Authority) Valid() bool {
	switch NormalizeAuthority(string(a)) {
	case AuthorityL0, AuthorityL1, AuthorityL2, AuthorityL3, AuthorityL4:
		return true
	default:
		return false
	}
}

// NormalizeAuthority uppercases and trims an authority.
func NormalizeAuthority(raw string) Authority {
	return Authority(strings.ToUpper(strings.TrimSpace(raw)))
}

// authorityRank orders authority rungs. Unknown values rank -1 (below L0).
func authorityRank(a Authority) int {
	switch NormalizeAuthority(string(a)) {
	case AuthorityL0:
		return 0
	case AuthorityL1:
		return 1
	case AuthorityL2:
		return 2
	case AuthorityL3:
		return 3
	case AuthorityL4:
		return 4
	default:
		return -1
	}
}

// CompareAuthority returns -1, 0 or 1 when a is below, equal to or above b.
func CompareAuthority(a, b Authority) int {
	ar, br := authorityRank(a), authorityRank(b)
	switch {
	case ar < br:
		return -1
	case ar > br:
		return 1
	default:
		return 0
	}
}

// DefaultAuthority is the authority a run gets when none is configured.
//
// L2 (read-only without asking) rather than L0: L0 would make the tool surface
// unusable out of the box, and L3+ would hand out write access by default. A
// deployment that wants either extreme says so explicitly.
const DefaultAuthority = AuthorityL2

// requiredAuthorityFor maps an effect onto the minimum authority that may
// perform it without a human in the loop.
//
// The mapping is the whole policy in one place: read needs L2, write needs L3,
// delete needs L4. Anything below that is not a denial — it is a pause, so a
// human can approve this one call (see CheckToolEffect).
func requiredAuthorityFor(effect ToolEffect) Authority {
	switch effect {
	case EffectRead:
		return AuthorityL2
	case EffectDelete:
		return AuthorityL4
	default: // write, and anything undetermined (already normalized to write)
		return AuthorityL3
	}
}

// EffectDecision is the outcome of the pre-execution gate.
type EffectDecision struct {
	// Allowed is true when the run's authority covers the required effect.
	Allowed bool
	// Required is the authority the call needs (after taking the stronger of
	// task declaration and tool effect).
	Required Authority
	// Effect is the effect the call was judged at.
	Effect ToolEffect
	// Reason explains a refusal in one sentence, for the pause message and the
	// audit trail. Empty when Allowed.
	Reason string
}

// CheckToolEffect is the pre-execution gate: may this run call this tool, given
// what the task declared?
//
// Three answers, not two: allowed, needs-approval, and out-of-reach. It reports
// needs-approval as Allowed=false with a reason, because the caller's job is to
// pause and ask; the distinction between "ask a human" and "never" is the
// caller's policy, and it can tell by comparing Required against the run's
// authority (see AuthorityCanApprove).
func CheckToolEffect(authority Authority, declared ToolEffect, tool *ToolDef) EffectDecision {
	if tool == nil {
		return EffectDecision{
			Effect: EffectWrite,
			Reason: "no tool definition; refusing to guess its effect",
		}
	}
	effect := StrongerEffect(declared, EffectiveToolEffect(tool.Effect))
	required := requiredAuthorityFor(effect)

	decision := EffectDecision{Required: required, Effect: effect}
	if CompareAuthority(authority, required) >= 0 {
		decision.Allowed = true
		return decision
	}

	decision.Reason = fmt.Sprintf(
		"tool %q has effect %q which needs authority %s, but this run holds %s",
		tool.Name, effect, required, NormalizeAuthority(string(authority)))
	return decision
}

// AuthorityCanApprove reports whether a human holding this authority could
// approve the refused call. A run below the requirement is refused outright
// when its holder could not approve it either — that is the difference between
// "wait for someone who can" and "this will never be allowed".
func AuthorityCanApprove(authority Authority, required Authority) bool {
	return CompareAuthority(authority, required) >= 0
}
