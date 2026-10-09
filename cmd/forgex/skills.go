package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/castwell/forge/internal/agent/core"
	"github.com/castwell/forge/internal/agent/harness"
	"github.com/castwell/forge/internal/forgex/cases"
	"github.com/castwell/forge/internal/forgex/demo"
	"github.com/castwell/forge/internal/forgex/skillpack"
)

const defaultSkillsDir = "configs/forgex/skills"

// runSkills dispatches the skills subcommands: the loop's last link, where a
// verified case becomes a loadable, versioned, re-checkable asset.
func runSkills(args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stdout, skillsLifecycleUsage+"\n")
		return fmt.Errorf("skills subcommand required (available: list, show, distill, publish, verify, export, deprecate, restore, review, stale)")
	}
	switch args[0] {
	case "list":
		return skillsList(args[1:])
	case "show":
		return skillsShow(args[1:])
	case "distill":
		return skillsDistill(args[1:])
	case "publish":
		return skillsPublish(args[1:])
	case "verify":
		return skillsVerify(args[1:])
	case "export":
		return skillsExport(args[1:])
	case "deprecate", "restore", "review", "stale":
		return runSkillsLifecycle(args[0], args[1:])
	default:
		return fmt.Errorf("unknown skills subcommand: %s", args[0])
	}
}

func skillsList(args []string) error {
	fs := flag.NewFlagSet("skills list", flag.ContinueOnError)
	dir := fs.String("skills-dir", defaultSkillsDir, "published skills directory")
	// Deprecated skills are hidden by default: "what skills do we have" almost
	// always means "what can an agent use", and listing retired ones would
	// invite applying something the team decided against.
	all := fs.Bool("all", false, "include deprecated skills")
	if err := fs.Parse(args); err != nil {
		return err
	}

	store := skillpack.NewStore(*dir)
	var (
		packs []skillpack.Pack
		err   error
	)
	if *all {
		packs, err = store.ListAll()
	} else {
		packs, err = store.List()
	}
	if err != nil {
		return err
	}
	if len(packs) == 0 {
		fmt.Printf("no published skills in %s\n", *dir)
		return nil
	}
	for _, p := range packs {
		status := ""
		if p.Metadata.IsDeprecated() {
			status = " [deprecated]"
		}
		fmt.Printf("  %-40s v%-8s %s%s\n",
			p.Metadata.ID, p.Metadata.Version, p.Spec.Trigger.Description, status)
	}
	return nil
}

func skillsShow(args []string) error {
	fs := flag.NewFlagSet("skills show", flag.ContinueOnError)
	id := fs.String("skill", "", "skill id")
	dir := fs.String("skills-dir", defaultSkillsDir, "published skills directory")
	jsonOut := fs.Bool("json", false, "print as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" {
		return fmt.Errorf("--skill is required")
	}
	pack, err := skillpack.NewStore(*dir).Load(*id)
	if err != nil {
		return err
	}
	if *jsonOut {
		encoded, err := json.MarshalIndent(pack, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(encoded))
		return nil
	}
	fmt.Printf("id: %s\nversion: %s\nstatus: %s\n", pack.Metadata.ID, pack.Metadata.Version, pack.Metadata.Status)
	fmt.Printf("trigger: %s\n", pack.Spec.Trigger.Description)
	fmt.Printf("steps: %d, tools: %v\n", len(pack.Spec.Steps), pack.Spec.ToolPermissions)
	fmt.Printf("constraints: %d, eval suite: %s, cases: %v\n",
		len(pack.Spec.Constraints), pack.Spec.Eval.Suite, pack.Spec.Eval.Cases)
	fmt.Printf("\n%s\n", strings.TrimSpace(pack.Readme))
	return nil
}

// skillsDistill turns a verified replay into a draft.
func skillsDistill(args []string) error {
	fs := flag.NewFlagSet("skills distill", flag.ContinueOnError)
	runDir := fs.String("run", "", "run directory that already carries replay_result.json")
	casesPath := fs.String("cases", "configs/forgex/cases.yaml", "case registry YAML path")
	dir := fs.String("skills-dir", defaultSkillsDir, "skills directory (drafts go to <dir>/drafts)")
	useLLM := fs.Bool("llm", false, "let a model draft the readme and keywords (needs FORGE_LLM_API_KEY)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *runDir == "" {
		return fmt.Errorf("--run is required (a run replayed by `forgex cases run`)")
	}
	reg, err := cases.Load(*casesPath)
	if err != nil {
		return err
	}
	src, err := skillpack.LoadSource(*runDir, reg)
	if err != nil {
		return err
	}
	draft := skillpack.Distill(src, time.Now())

	if *useLLM {
		filler, err := newLLMFiller()
		if err != nil {
			return err
		}
		filled, err := filler(context.Background(), draft)
		if err != nil {
			return fmt.Errorf("model fill: %w", err)
		}
		// The model may only have written prose; the machine half comes back
		// from the skeleton no matter what it returned.
		draft = skillpack.OverlayFacts(draft, filled)
	}

	path, err := skillpack.NewStore(*dir).Save(draft, true)
	if err != nil {
		return err
	}
	fmt.Printf("draft written: %s\n", path)
	fmt.Printf("source: case=%s run=%s tools=%d lessons=%d\n",
		src.Spec.ID, src.RunID, len(draft.Spec.ToolPermissions), len(draft.Spec.Constraints))
	if *useLLM {
		fmt.Println("prose: drafted by model, facts kept from the run artifacts")
	} else {
		fmt.Println("prose: template only - write it, or re-run with --llm")
	}
	fmt.Println("next: review the draft, then `forgex skills publish <path>`")
	return nil
}

// skillsPublish runs the three gates and moves the pack to the published dir.
func skillsPublish(args []string) error {
	fs := flag.NewFlagSet("skills publish", flag.ContinueOnError)
	casesPath := fs.String("cases", "configs/forgex/cases.yaml", "case registry YAML path")
	dir := fs.String("skills-dir", defaultSkillsDir, "published skills directory")
	// Go's flag parser stops at the first non-flag token, so a command written
	// as `publish <path> --flags` would silently ignore the flags. Reorder to
	// flags first and accept the draft path at the end.
	if err := fs.Parse(orderFlagsFirst(args)); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("usage: skills publish <draft-path> [--skills-dir dir] [--cases file]")
	}
	pack, err := skillpack.LoadPath(fs.Arg(0))
	if err != nil {
		return err
	}
	reg, err := cases.Load(*casesPath)
	if err != nil {
		return err
	}
	exists := func(id string) bool { _, err := reg.Find(id); return err == nil }

	issues := pack.Check(exists)
	if len(issues) > 0 {
		for _, issue := range issues {
			fmt.Fprintf(os.Stderr, "  blocked: %s\n", issue)
		}
		return fmt.Errorf("publish of %s blocked by %d gate(s)", pack.Metadata.ID, len(issues))
	}
	pack.Metadata.ReviewStatus = skillpack.ReviewDone
	path, err := skillpack.NewStore(*dir).Save(pack, false)
	if err != nil {
		return err
	}
	fmt.Printf("published: %s (v%s)\n", path, pack.Metadata.Version)
	fmt.Printf("gates passed: readme written, tools are workflow vocabulary, eval cases exist\n")
	return nil
}

// skillsVerify re-runs every case a skill binds and requires both gates to pass:
// the suite rules and the case's expected outcome. A skill that cannot pass its
// own cases is out of date with the system it describes.
func skillsVerify(args []string) error {
	fs := flag.NewFlagSet("skills verify", flag.ContinueOnError)
	id := fs.String("skill", "", "skill id to verify")
	dir := fs.String("skills-dir", defaultSkillsDir, "published skills directory")
	casesPath := fs.String("cases", "configs/forgex/cases.yaml", "case registry YAML path")
	root := fs.String("root", ".forgex", "root directory for run artifacts")
	rules := fs.String("rules", "configs/forgex/eval_rules.yaml", "eval rules YAML path")
	taxonomy := fs.String("taxonomy", demo.DefaultTaxonomyPath, "failure taxonomy YAML path")
	policy := fs.String("policy", demo.DefaultPolicyPath, "stop policy YAML path")
	contracts := fs.String("contracts", demo.DefaultContractsPath, "tool contracts YAML path")
	toolPolicy := fs.String("tool-policy", demo.DefaultToolPolicyPath, "tool policy YAML path")
	authority := fs.String("authority", demo.DefaultAuthorityLevel, "authority level override")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" {
		return fmt.Errorf("--skill is required")
	}
	pack, err := skillpack.NewStore(*dir).Load(*id)
	if err != nil {
		return err
	}
	reg, err := cases.Load(*casesPath)
	if err != nil {
		return err
	}
	exists := func(cid string) bool { _, err := reg.Find(cid); return err == nil }
	if issues := pack.Check(exists); len(issues) > 0 {
		return fmt.Errorf("skill %s no longer passes its own gates: %s", *id, strings.Join(issues, "; "))
	}

	var failures []string
	for _, cid := range pack.Spec.Eval.Cases {
		spec, err := reg.Find(cid)
		if err != nil {
			return err
		}
		runID, err := demo.RunScenario(context.Background(), demo.ScenarioConfig{
			Root:           *root,
			TaxonomyPath:   *taxonomy,
			PolicyPath:     *policy,
			PacketPath:     spec.TaskPacket,
			ContractsPath:  *contracts,
			ToolPolicyPath: *toolPolicy,
			AuthorityLevel: *authority,
		})
		if err != nil {
			return fmt.Errorf("case %s: %w", cid, err)
		}
		runDir := filepath.Join(*root, "runs", runID)
		result, err := evaluateRunDir(runDir, *rules, spec.Suite)
		if err != nil {
			return fmt.Errorf("case %s: %w", cid, err)
		}
		verdict, err := replayCase(runDir, spec)
		if err != nil {
			return fmt.Errorf("case %s: %w", cid, err)
		}
		ok := result.Status == "passed" && verdict.Passed
		mark := "ok  "
		if !ok {
			mark = "FAIL"
			failures = append(failures, cid)
		}
		fmt.Printf("  [%s] case=%-30s suite=%s expected=%s\n", mark, cid, result.Status, verdict.Summary())
	}
	if len(failures) > 0 {
		return fmt.Errorf("skill %s: %d/%d bound cases failed: %s",
			*id, len(failures), len(pack.Spec.Eval.Cases), strings.Join(failures, ", "))
	}
	fmt.Printf("skill %s verified against %d bound case(s)\n", *id, len(pack.Spec.Eval.Cases))
	return nil
}

// skillsExport is the machine-facing surface for the external orchestrator:
// a JSON array of published packs, ready to be read when generating workflows.
func skillsExport(args []string) error {
	fs := flag.NewFlagSet("skills export", flag.ContinueOnError)
	dir := fs.String("skills-dir", defaultSkillsDir, "published skills directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	packs, err := skillpack.NewStore(*dir).List()
	if err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(packs, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(encoded))
	return nil
}

// orderFlagsFirst moves positional tokens behind the flag pairs. Every flag in
// this command takes a value, so "-x value" is unambiguous; this exists so
// `skills publish <path> --skills-dir d` works as written instead of dropping
// the flags after the path.
func orderFlagsFirst(args []string) []string {
	var flags []string
	var positionals []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "-") {
			flags = append(flags, arg)
			if !strings.Contains(arg, "=") && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
				flags = append(flags, args[i])
			}
			continue
		}
		positionals = append(positionals, arg)
	}
	return append(flags, positionals...)
}

// newLLMFiller builds the prose filler from the environment-configured model.
func newLLMFiller() (skillpack.Filler, error) {
	if os.Getenv("FORGE_LLM_API_KEY") == "" {
		return nil, fmt.Errorf("--llm needs FORGE_LLM_API_KEY (or drop --llm and write the readme during review)")
	}
	cfg := harness.DefaultLLMConfig()
	cfg.APIKey = os.Getenv("FORGE_LLM_API_KEY")
	if baseURL := os.Getenv("FORGE_LLM_BASE_URL"); baseURL != "" {
		cfg.BaseURL = baseURL
	}
	client := harness.NewLLMClient(cfg)

	return func(ctx context.Context, draft skillpack.Pack) (skillpack.Pack, error) {
		skeleton, err := yaml.Marshal(draft)
		if err != nil {
			return skillpack.Pack{}, err
		}
		system := `你是 SkillPack 编辑。这是一份从验证过的回放中蒸馏出的 SkillPack 草稿（YAML）。
补全 readme（markdown：什么时候用这个技能、步骤含义、约束为什么存在）和 trigger.keywords（3-8 个）。
只改 readme 和 trigger，其余字段原样保留。只输出完整 YAML，不要解释。`
		raw, err := client.Chat(ctx, []core.Message{
			{Role: "system", Content: system},
			{Role: "user", Content: string(skeleton)},
		})
		if err != nil {
			return skillpack.Pack{}, err
		}
		var filled skillpack.Pack
		if err := yaml.Unmarshal([]byte(stripFences(raw)), &filled); err != nil {
			return skillpack.Pack{}, fmt.Errorf("model output is not valid YAML: %w", err)
		}
		return filled, nil
	}, nil
}

// stripFences removes markdown code fences around a YAML response.
func stripFences(raw string) string {
	s := strings.TrimSpace(raw)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	if idx := strings.Index(s, "\n"); idx >= 0 {
		s = s[idx+1:]
	}
	if idx := strings.LastIndex(s, "```"); idx >= 0 {
		s = s[:idx]
	}
	return strings.TrimSpace(s)
}
