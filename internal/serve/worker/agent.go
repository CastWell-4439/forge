package worker

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"

	agentcore "github.com/castwell/forge/internal/agent"
	"github.com/castwell/forge/internal/agent/core"
	"github.com/castwell/forge/internal/agent/guardrails"
	"github.com/castwell/forge/internal/agent/workers"
	"github.com/castwell/forge/internal/forgex/skillpack"
	"github.com/castwell/forge/internal/worker"
	agentworker "github.com/castwell/forge/internal/workers/agent"
)

// Environment variables that configure the agent workflow worker.
//
//	FORGE_AGENT_MODE             real (default) | mock
//	FORGE_AGENT_WORKSPACE        base dir for file/shell tools (default .forge-workspace)
//	FORGE_WEB_SEARCH_PROVIDER    duckduckgo (default, no key) | brave | off
//	FORGE_WEB_SEARCH_API_KEY     key for the brave provider
//	FORGE_WEB_SEARCH_ENDPOINT    endpoint override (tests point this at httptest)
//	FORGE_WEB_FETCH_ALLOW_PRIVATE  lift the web.fetch SSRF guard (dev only)
//	FORGE_SKILLPACK_DIR          directory of published skill packs for skill.activate
//	                            (default configs/forgex/skills; unset+missing = the
//	                            tool names the gap instead of failing quietly)
//	FORGE_CONTEXT_REMIND_AT      first water line as a fraction of the window
//	                            (default 0.7; off = no reminders)
//	FORGE_CONTEXT_URGENT_AT      second, stronger line (default 0.9)
//	FORGE_CONTEXT_KEEP_MESSAGES  trailing messages kept by a compaction (default 4)
//	FORGE_CONTEXT_COMPACT_TARGET  fraction of the budget a compaction aims for
//	                            (default 0.6; the static path summarises only as
//	                            far as needed to reach it, so recent turns survive)
const (
	envAgentMode            = "FORGE_AGENT_MODE"
	envAgentWorkspace       = "FORGE_AGENT_WORKSPACE"
	envAgentGuard           = "FORGE_AGENT_GUARD"
	envLLMToolMode          = "FORGE_LLM_TOOL_MODE"
	envWebSearchProvider    = "FORGE_WEB_SEARCH_PROVIDER"
	envWebSearchAPIKey      = "FORGE_WEB_SEARCH_API_KEY"
	envWebSearchEndpoint    = "FORGE_WEB_SEARCH_ENDPOINT"
	envWebFetchAllowPriv    = "FORGE_WEB_FETCH_ALLOW_PRIVATE"
	envSkillpackDir         = "FORGE_SKILLPACK_DIR"
	envContextRemindAt      = "FORGE_CONTEXT_REMIND_AT"
	envContextUrgentAt      = "FORGE_CONTEXT_URGENT_AT"
	envContextKeepMessages  = "FORGE_CONTEXT_KEEP_MESSAGES"
	envContextCompactTarget = "FORGE_CONTEXT_COMPACT_TARGET"
	envAgentAuthority       = "FORGE_AGENT_AUTHORITY"
	defaultAgentWorkspace   = ".forge-workspace"
	defaultSkillpackDir     = "configs/forgex/skills"
)

// registerAgent wires the tenth workflow worker: a task that runs the full
// agent (golden tool set, real mode) through internal/agent — the assembly
// that the audit found reachable only from tests.
//
// The knowledge stack is injected so knowledge.search really retrieves;
// data.query and the web tools get their backends from the environment via
// WithToolConfig — env reads live here, never inside the agent library.
// Without an LLM key the worker registers an explanatory handler instead of
// failing with "unknown handler", matching every other configured-optional
// worker.
func registerAgent(r *worker.Registry) {
	llm := newEnvLLMClient("", 0.7, 4096) // model/temperature: harness defaults
	if llm == nil {
		r.Register("agent", unconfiguredHandler("agent",
			fmt.Sprintf("no LLM client; set %s", envLLMAPIKey)))
		return
	}

	mode := strings.ToLower(strings.TrimSpace(os.Getenv(envAgentMode)))
	workspace := envOrDefault(envAgentWorkspace, defaultAgentWorkspace)

	opts := []agentcore.Option{
		agentcore.WithRetriever(buildKnowledgeStack()),
		agentcore.WithWorkspace(workspace),
		agentcore.WithToolConfig(func(cfg *workers.HandlerConfig) {
			cfg.DataSource = os.Getenv(envPGDSN)
			cfg.WebSearchProvider = os.Getenv(envWebSearchProvider)
			cfg.WebSearchAPIKey = os.Getenv(envWebSearchAPIKey)
			cfg.WebSearchEndpoint = os.Getenv(envWebSearchEndpoint)
			cfg.WebFetchAllowPrivate = truthy(os.Getenv(envWebFetchAllowPriv))
			// skill.activate's loader: reads a published SkillPack from disk
			// and hands the model the full document. Always set — with no
			// directory configured the error NAMES the env var, which is the
			// "缺目录=点名" rule; an unset loader would say something vaguer.
			cfg.LoadSkill = skillLoader(os.Getenv(envSkillpackDir))
		}),
	}

	// Water-line reminders, compaction keep-count and compaction target (N2c
	// S1/S4): env lives here, the loop only ever sees numbers (0 = defaults,
	// remind < 0 = off).
	opts = append(opts, agentcore.WithContextTuning(
		envFraction(envContextRemindAt),
		envFraction(envContextUrgentAt),
		envInt(envContextKeepMessages),
		envFraction(envContextCompactTarget),
	))

	// Authority is the ceiling agent runs may act under (N4 + the risk gate).
	// It is read here, at assembly, and it is a property of this DEPLOYMENT: a
	// workflow author cannot raise it, which is what stops a pipeline from
	// granting itself permission to delete. Unset means L2 (read-only without
	// asking); a tool whose effect exceeds it pauses the run for a human.
	opts = append(opts, agentcore.WithAuthority(agentAuthority()))

	// F3: lessons from the control plane, when the operator turns the channel
	// on. Default off — a cross-plane feed is opt-in — and a missing index
	// degrades to "no lessons", never to a failed registration.
	opts = applyLessonsFeed(opts)

	// Tool output is untrusted input to the model: a fetched page can say
	// "ignore your instructions", and the observation is delivered as a
	// user-role message. Screening is therefore ON by default — an agent that
	// browses without it hands the internet a channel into its own
	// instruction stream. FORGE_AGENT_GUARD=off opts out explicitly.
	guardEnabled := agentGuardEnabled()
	if guardEnabled {
		opts = append(opts, agentcore.WithToolOutputGuard(guardrails.NewInjectionDetector()))
	}

	// Native function calling (N2): on by default, off only when explicitly
	// asked. The endpoint gets the first word — a provider that refuses the
	// tools field downgrades this run to the prompt path with one warning.
	switch strings.ToLower(strings.TrimSpace(os.Getenv(envLLMToolMode))) {
	case "prompt", "off", "0", "false":
		// explicit opt-out keeps the prompt-encoded JSON convention
	default:
		opts = append(opts, agentcore.WithNativeTools(true))
	}

	switch mode {
	case "", "real":
		// Real is the zero-value default inside Agent; nothing to add.
	case "mock":
		opts = append(opts, agentcore.WithHandlerMode(workers.HandlerModeMock))
		mode = "mock"
	default:
		log.Printf("WARN: unknown %s %q (want real|mock); using real", envAgentMode, mode)
		mode = "real"
	}

	r.Register("agent", adaptWorkflowWorker("agent", agentworker.NewWorker(agentcore.New(llm, opts...))))
	log.Printf("INFO: agent worker registered (mode=%s workspace=%s guard=%v)", mode, workspace, guardEnabled)
}

// agentGuardEnabled resolves FORGE_AGENT_GUARD. Unset and unrecognised values
// both mean ON: injection screening is a security default, so the only way to
// lose it is to ask for it explicitly — a typo must never silently disable it.
func agentGuardEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(envAgentGuard))) {
	case "off", "0", "false", "no":
		return false
	case "", "on", "1", "true", "yes":
		return true
	default:
		log.Printf("WARN: unknown %s %q (want on|off); keeping the guard on", envAgentGuard, os.Getenv(envAgentGuard))
		return true
	}
}

// agentAuthority resolves FORGE_AGENT_AUTHORITY.
//
// An unrecognised value falls back to the default (L2) rather than to the most
// permissive rung: a typo must never hand out more authority than the operator
// intended. Absent means the default, which is also the safe direction.
func agentAuthority() core.Authority {
	raw := strings.TrimSpace(os.Getenv(envAgentAuthority))
	if raw == "" {
		return core.DefaultAuthority
	}
	authority := core.NormalizeAuthority(raw)
	if !authority.Valid() {
		log.Printf("WARN: unknown %s %q (want L0..L4); using %s", envAgentAuthority, raw, core.DefaultAuthority)
		return core.DefaultAuthority
	}
	return authority
}

// truthy parses the usual affirmative env values.
func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}

// skillLoader returns skill.activate's loader: a published SkillPack rendered
// as the document the model follows. With no directory the returned error
// names the env var, so "not configured" is always actionable.
func skillLoader(dir string) workers.LoadSkillFunc {
	if dir == "" {
		dir = defaultSkillpackDir
	}
	return func(_ context.Context, id string) (string, error) {
		pack, err := skillpack.NewStore(dir).Load(id)
		if err != nil {
			return "", fmt.Errorf(
				"skill %q not loadable from %s (set %s to your published skill packs): %w",
				id, dir, envSkillpackDir, err)
		}
		return skillpack.RenderDocument(pack), nil
	}
}

// envFraction parses a fraction env value: empty = 0 (use the default),
// "off"/"0" as the REMIND value means disabled (-1), anything else a number.
// Unparseable input warns and falls back to the default — a typo must not
// silently change the water lines.
func envFraction(key string) float64 {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return 0
	}
	lower := strings.ToLower(raw)
	if lower == "off" || lower == "none" {
		return -1
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		log.Printf("WARN: unknown %s %q (want a fraction or off); using the default", key, raw)
		return 0
	}
	return v
}

// envInt parses an integer env value; empty or invalid = 0 (the default).
func envInt(key string) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return 0
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		log.Printf("WARN: unknown %s %q (want an integer); using the default", key, raw)
		return 0
	}
	return v
}
