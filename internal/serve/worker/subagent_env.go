package worker

import (
	"log"
	"os"
	"strings"

	"github.com/castwell/forge/internal/agent/core"
)

// Subagent configuration from the environment (N5).
//
// It lives in the serve layer because that is where every other deployment knob
// is read (see agent.go); the agent package only ever sees a resolved config.
//
// Three orthogonal settings, deliberately not collapsed into one "mode" string:
//
//	FORGE_SUBAGENT_MODE            off | isolated | continuable   (default off)
//	FORGE_SUBAGENT_REPORT          answer | summary | steps       (default summary)
//	FORGE_SUBAGENT_MAX_DEPTH       integer, 0 forbids delegation  (default 1)
//	FORGE_SUBAGENT_MAX_CONCURRENT  children per parent at once    (default 1)
//	FORGE_SUBAGENT_TOOLS           comma-separated allow-list     (default: inherit)
//
// Keeping them separate is what makes "one-shot but fully transparent" or
// "continuable but answer-only" expressible; a single mode enum would have
// decided those for the operator.
//
// The env name constants live beside the other agent env names in agent.go,
// except these two, which only this file reads.
const (
	envSubagentDepth = "FORGE_SUBAGENT_MAX_DEPTH"
	envSubagentConc  = "FORGE_SUBAGENT_MAX_CONCURRENT"
)

// subagentConfig resolves the delegation settings.
//
// Every unrecognised value falls back to the DISABLED or most cautious option,
// and says so in the log. A typo must never silently widen what an agent may do,
// and it must never be silent either: the operator asked for something and did
// not get it.
func subagentConfig() core.SubagentConfig {
	rawMode := strings.TrimSpace(os.Getenv(envSubagentMode))
	mode := core.NormalizeSubagentMode(rawMode)
	if rawMode != "" && string(mode) != rawMode {
		log.Printf("WARN: unknown %s %q (want off|isolated|continuable); delegation stays %s",
			envSubagentMode, rawMode, mode)
	}
	if !mode.Enabled() {
		// Off is the default; nothing else is read. Reporting the other knobs
		// here would suggest they had an effect.
		return core.SubagentConfig{Mode: core.SubagentOff}
	}

	rawReport := strings.TrimSpace(os.Getenv(envSubagentReport))
	report := core.NormalizeSubagentReport(rawReport)
	if rawReport != "" && string(report) != rawReport {
		log.Printf("WARN: unknown %s %q (want answer|summary|steps); using %s",
			envSubagentReport, rawReport, report)
	}

	// Depth defaults to 1 (one level). A configured 0 is honoured: it means
	// "register the tool but allow no delegation", which is a legitimate way to
	// make the refusal visible. envInt already warns and falls back to 0 on a
	// malformed value, so the malformed case is reported here too.
	depth := core.DefaultSubagentMaxDepth
	if raw := strings.TrimSpace(os.Getenv(envSubagentDepth)); raw != "" {
		depth = envInt(envSubagentDepth)
	}

	concurrency := core.DefaultSubagentMaxConcurrent
	if raw := strings.TrimSpace(os.Getenv(envSubagentConc)); raw != "" {
		if n := envInt(envSubagentConc); n > 0 {
			concurrency = n
		} else {
			log.Printf("WARN: %s %q is not a positive integer; using %d",
				envSubagentConc, raw, concurrency)
		}
	}

	cfg := core.SubagentConfig{
		Mode:   mode,
		Report: report,
		Limits: core.SubagentLimits{MaxDepth: depth, MaxConcurrent: concurrency},
		Tools:  parseToolList(os.Getenv(envSubagentTools)),
	}

	// A depth of 0 with a mode that enables delegation is a contradiction: the
	// tool would be registered and refuse every call. Say so once, at startup,
	// rather than letting the model discover it.
	if depth == 0 {
		log.Printf("WARN: %s is 0 while %s=%s: the subagent tool will be registered but refuse every call",
			envSubagentDepth, envSubagentMode, mode)
	}

	log.Printf("INFO: subagent delegation enabled (mode=%s report=%s max_depth=%d max_concurrent=%d tools=%d)",
		mode, report, depth, concurrency, len(cfg.Tools))
	return cfg
}

// parseToolList splits a comma-separated allow-list, dropping blanks so a
// trailing comma is not an error.
func parseToolList(raw string) []string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	parts := strings.Split(trimmed, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if name := strings.TrimSpace(part); name != "" {
			out = append(out, name)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
