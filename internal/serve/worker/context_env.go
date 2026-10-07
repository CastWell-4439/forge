package worker

import (
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/castwell/forge/internal/agent/core"
)

// Tool-output slimming configuration (the S2 layer).
//
//	FORGE_CONTEXT_TOOL_SLIM            uniform | by_tool        (default uniform)
//	FORGE_CONTEXT_TOOL_SLIM_PROFILE    head_heavy | tail_heavy | balanced | head_only
//	                                   (default head_heavy = the old constants)
//	FORGE_CONTEXT_TOOL_SLIM_MAP        tool=profile,tool=profile  (by_tool mode)
//	FORGE_CONTEXT_TOOL_SLIM_THRESHOLD  characters above which slimming applies
//	FORGE_CONTEXT_TOOL_SLIM_HEAD       characters kept from the start
//	FORGE_CONTEXT_TOOL_SLIM_TAIL       characters kept from the end (0 = head only)
//
// Why this is worth configuring at all: S2 decides what the model can still see
// after an oversized tool result is evicted. A log's answer is at its END, a
// source listing's at its START, and the single 1500/500 split served the first
// case well and the second badly. The profile is a real preference about where
// the useful part of an output sits, not a knob for its own sake.
const (
	envToolSlimMode      = "FORGE_CONTEXT_TOOL_SLIM"
	envToolSlimProfile   = "FORGE_CONTEXT_TOOL_SLIM_PROFILE"
	envToolSlimMap       = "FORGE_CONTEXT_TOOL_SLIM_MAP"
	envToolSlimThreshold = "FORGE_CONTEXT_TOOL_SLIM_THRESHOLD"
	envToolSlimHead      = "FORGE_CONTEXT_TOOL_SLIM_HEAD"
	envToolSlimTail      = "FORGE_CONTEXT_TOOL_SLIM_TAIL"
)

// toolSlimConfig resolves the S2 policy.
//
// Every unrecognised value falls back to the historical default and says so.
// The default matters more here than elsewhere: a bad threshold does not break
// the run, it silently changes how much of every tool result the model sees —
// so "the operator asked for something and did not get it" must be visible.
func toolSlimConfig() core.ToolSlimConfig {
	cfg := core.DefaultToolSlimConfig()

	rawMode := strings.TrimSpace(os.Getenv(envToolSlimMode))
	switch core.SlimmingMode(rawMode) {
	case core.SlimmingByTool:
		cfg.Mode = core.SlimmingByTool
	case core.SlimmingUniform, "":
		cfg.Mode = core.SlimmingUniform
	default:
		log.Printf("WARN: unknown %s %q (want uniform|by_tool); using %s",
			envToolSlimMode, rawMode, cfg.Mode)
	}

	// A named profile, then numeric overrides on top of it — so an operator can
	// say "tail-heavy, but keep 3000 from the end" without spelling out all
	// three numbers.
	if raw := strings.TrimSpace(os.Getenv(envToolSlimProfile)); raw != "" {
		profile, ok := core.NamedProfile(raw)
		if !ok {
			log.Printf("WARN: unknown %s %q (want head_heavy|tail_heavy|balanced|head_only); using %s",
				envToolSlimProfile, raw, "head_heavy")
		} else {
			cfg.Default = profile
		}
	}
	if raw := strings.TrimSpace(os.Getenv(envToolSlimThreshold)); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			cfg.Default.Threshold = n
		} else {
			log.Printf("WARN: %s %q is not a positive integer; using %d",
				envToolSlimThreshold, raw, cfg.Default.Threshold)
		}
	}
	if raw := strings.TrimSpace(os.Getenv(envToolSlimHead)); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n >= 0 {
			cfg.Default.Head = n
		} else {
			log.Printf("WARN: %s %q is not a non-negative integer; using %d",
				envToolSlimHead, raw, cfg.Default.Head)
		}
	}
	if raw := strings.TrimSpace(os.Getenv(envToolSlimTail)); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n >= 0 {
			cfg.Default.Tail = n
		} else {
			log.Printf("WARN: %s %q is not a non-negative integer; using %d",
				envToolSlimTail, raw, cfg.Default.Tail)
		}
	}

	if cfg.Mode == core.SlimmingByTool {
		cfg.ByTool = parseSlimMap(os.Getenv(envToolSlimMap))
		if len(cfg.ByTool) == 0 {
			// by_tool with no entries is uniform with extra steps; say so
			// rather than pretending the mode did something.
			log.Printf("WARN: %s is by_tool but %s lists no valid entries; every tool uses the default profile",
				envToolSlimMode, envToolSlimMap)
		}
	}

	cfg = cfg.Normalize()
	if cfg.Mode == core.SlimmingByTool {
		log.Printf("INFO: tool slimming by_tool (default %d/%d/%d, %d tool overrides)",
			cfg.Default.Threshold, cfg.Default.Head, cfg.Default.Tail, len(cfg.ByTool))
	}
	return cfg
}

// parseSlimMap reads "tool=profile,tool=profile" pairs.
//
// A malformed entry is reported and skipped rather than aborting the whole
// map: one typo should not silently drop the other overrides, and the warning
// names the entry so it can be fixed.
func parseSlimMap(raw string) map[string]core.SlimProfile {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	out := map[string]core.SlimProfile{}
	for _, entry := range strings.Split(trimmed, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		name, profileName, ok := strings.Cut(entry, "=")
		if !ok {
			log.Printf("WARN: %s entry %q is not tool=profile; skipped", envToolSlimMap, entry)
			continue
		}
		name = strings.TrimSpace(name)
		profile, ok := core.NamedProfile(strings.TrimSpace(profileName))
		if !ok {
			log.Printf("WARN: %s entry %q names unknown profile %q; skipped",
				envToolSlimMap, entry, strings.TrimSpace(profileName))
			continue
		}
		if name == "" {
			log.Printf("WARN: %s entry %q has no tool name; skipped", envToolSlimMap, entry)
			continue
		}
		out[name] = profile
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// noProgressThreshold resolves FORGE_AGENT_NO_PROGRESS_THRESHOLD.
//
// Zero means "the harness default" and is also what an unset variable yields,
// so the two are indistinguishable by design: not configuring it must be the
// same as configuring the default.
func noProgressThreshold() int {
	raw := strings.TrimSpace(os.Getenv(envNoProgressThreshold))
	if raw == "" {
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		log.Printf("WARN: %s %q is not an integer; using the default", envNoProgressThreshold, raw)
		return 0
	}
	return n
}
