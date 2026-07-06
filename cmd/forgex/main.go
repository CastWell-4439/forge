package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/castwell/forge/internal/forgex/cases"
	forgexcontext "github.com/castwell/forge/internal/forgex/context"
	"github.com/castwell/forge/internal/forgex/demo"
	forgexeval "github.com/castwell/forge/internal/forgex/eval"
	"github.com/castwell/forge/internal/forgex/model"
	forgexpolicy "github.com/castwell/forge/internal/forgex/policy"
	"github.com/castwell/forge/internal/forgex/productapi"
	"github.com/castwell/forge/internal/forgex/promotion"
	"github.com/castwell/forge/internal/forgex/reliability"
	"github.com/castwell/forge/internal/forgex/scorecard"
	"github.com/castwell/forge/internal/forgex/storage"
	"github.com/castwell/forge/internal/forgex/toolgw"
)

const version = "ForgeX v0.1.0"

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		printHelp()
		return
	}

	switch args[0] {
	case "version", "--version", "-v":
		fmt.Println(version)
	case "help", "--help", "-h":
		printHelp()
	case "run-demo":
		if err := runDemo(args[1:]); err != nil {
			fmt.Fprintf(os.Stderr, "run-demo: %v\n", err)
			os.Exit(1)
		}
	case "eval":
		if err := runEval(args[1:]); err != nil {
			fmt.Fprintf(os.Stderr, "eval: %v\n", err)
			os.Exit(1)
		}
	case "eval-repeat":
		if err := runEvalRepeat(args[1:]); err != nil {
			fmt.Fprintf(os.Stderr, "eval-repeat: %v\n", err)
			os.Exit(1)
		}
	case "promote-badcase":
		if err := promoteBadcase(args[1:]); err != nil {
			fmt.Fprintf(os.Stderr, "promote-badcase: %v\n", err)
			os.Exit(1)
		}
	case "index-run":
		if err := indexRun(args[1:]); err != nil {
			fmt.Fprintf(os.Stderr, "index-run: %v\n", err)
			os.Exit(1)
		}
	case "runs":
		if err := listRuns(args[1:]); err != nil {
			fmt.Fprintf(os.Stderr, "runs: %v\n", err)
			os.Exit(1)
		}
	case "context":
		if err := runContext(args[1:]); err != nil {
			fmt.Fprintf(os.Stderr, "context: %v\n", err)
			os.Exit(1)
		}
	case "policy":
		if err := runPolicy(args[1:]); err != nil {
			fmt.Fprintf(os.Stderr, "policy: %v\n", err)
			os.Exit(1)
		}
	case "lessons":
		if err := runLessons(args[1:]); err != nil {
			fmt.Fprintf(os.Stderr, "lessons: %v\n", err)
			os.Exit(1)
		}
	case "cases":
		if err := runCases(args[1:]); err != nil {
			fmt.Fprintf(os.Stderr, "cases: %v\n", err)
			os.Exit(1)
		}
	case "serve":
		if err := runServe(args[1:]); err != nil {
			fmt.Fprintf(os.Stderr, "serve: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", args[0])
		printHelp()
		os.Exit(2)
	}
}

// runDemo parses run-demo flags and dispatches to the requested demo case.
func runDemo(args []string) error {
	fs := flag.NewFlagSet("run-demo", flag.ContinueOnError)
	caseName := fs.String("case", "generic-contract-violation", "demo case to run")
	root := fs.String("root", ".forgex", "root directory for run artifacts")
	taxonomy := fs.String("taxonomy", demo.DefaultTaxonomyPath, "failure taxonomy YAML path")
	policy := fs.String("policy", demo.DefaultPolicyPath, "stop policy YAML path")
	packet := fs.String("packet", "", "task packet YAML path; defaults to the case's packet")
	contracts := fs.String("contracts", demo.DefaultContractsPath, "tool contracts YAML path")
	toolPolicy := fs.String("tool-policy", demo.DefaultToolPolicyPath, "tool policy YAML path")
	authority := fs.String("authority", demo.DefaultAuthorityLevel, "authority level override for tool policy decisions; defaults to task packet authority_level or L0")
	if err := fs.Parse(args); err != nil {
		return err
	}

	runID, err := runDemoCase(*caseName, *root, *taxonomy, *policy, *packet, *contracts, *toolPolicy, *authority)
	if err != nil {
		return err
	}
	fmt.Printf("demo completed: run_id=%s\n", runID)
	fmt.Printf("artifacts: %s/runs/%s/\n", *root, runID)
	return nil
}

func runDemoCase(caseName, root, taxonomy, policy, packet, contracts, toolPolicy, authority string) (string, error) {
	switch caseName {
	case "generic-contract-violation":
		return demo.RunGenericContractViolationDemoWithControl(context.Background(), root, taxonomy, policy, packet, contracts, toolPolicy, authority)
	case "generic-contract-success":
		return demo.RunGenericContractSuccessDemoWithControl(context.Background(), root, taxonomy, policy, packet, contracts, toolPolicy, authority)
	default:
		return "", fmt.Errorf("unknown demo case: %s (available: generic-contract-violation, generic-contract-success)", caseName)
	}
}

func runEval(args []string) error {
	fs := flag.NewFlagSet("eval", flag.ContinueOnError)
	runDir := fs.String("run", "", "ForgeX run directory to evaluate")
	suite := fs.String("suite", "generic_contract_regression_v1", "eval suite id")
	rules := fs.String("rules", "configs/forgex/eval_rules.yaml", "eval rules YAML path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *runDir == "" {
		return fmt.Errorf("--run is required")
	}
	result, err := evaluateRunDir(*runDir, *rules, *suite)
	if err != nil {
		return err
	}
	fmt.Printf("eval completed: run_id=%s suite=%s status=%s\n", result.RunID, result.SuiteID, result.Status)
	fmt.Printf("result: %s/eval_result.json\n", *runDir)
	fmt.Printf("scorecard: %s/scorecard.json\n", *runDir)
	if card, err := readScorecard(filepath.Join(*runDir, "scorecard.json")); err == nil {
		fmt.Printf("scorecard summary: %s\n", scorecard.Format(card))
	}
	if result.Status == "failed" {
		return fmt.Errorf("eval failed")
	}
	return nil
}

func evaluateRunDir(runDir, rulesPath, suiteID string) (model.EvalResult, error) {
	result, err := forgexeval.Run(context.Background(), runDir, rulesPath, suiteID)
	if err != nil {
		return model.EvalResult{}, err
	}
	artifacts, err := forgexeval.LoadRunArtifacts(runDir)
	if err != nil {
		return model.EvalResult{}, err
	}
	card := scorecard.Build(artifacts, result, countLessons(filepath.Join(runDir, "lessons.jsonl")))
	if err := scorecard.Write(runDir, card); err != nil {
		return model.EvalResult{}, err
	}
	if err := scorecard.AppendToReport(runDir, card); err != nil {
		return model.EvalResult{}, err
	}
	return result, nil
}

func readScorecard(path string) (scorecard.Scorecard, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return scorecard.Scorecard{}, err
	}
	var card scorecard.Scorecard
	if err := json.Unmarshal(data, &card); err != nil {
		return scorecard.Scorecard{}, err
	}
	return card, nil
}

func runEvalRepeat(args []string) error {
	fs := flag.NewFlagSet("eval-repeat", flag.ContinueOnError)
	caseID := fs.String("case", "", "registered case id to repeat")
	n := fs.Int("n", 1, "number of serial repetitions")
	root := fs.String("root", ".forgex-repeat", "root directory for repeated run artifacts")
	casesPath := fs.String("cases", "configs/forgex/cases.yaml", "case registry YAML path")
	rules := fs.String("rules", "configs/forgex/eval_rules.yaml", "eval rules YAML path")
	taxonomy := fs.String("taxonomy", demo.DefaultTaxonomyPath, "failure taxonomy YAML path")
	policy := fs.String("policy", demo.DefaultPolicyPath, "stop policy YAML path")
	contracts := fs.String("contracts", demo.DefaultContractsPath, "tool contracts YAML path")
	toolPolicy := fs.String("tool-policy", demo.DefaultToolPolicyPath, "tool policy YAML path")
	authority := fs.String("authority", demo.DefaultAuthorityLevel, "authority level override for tool policy decisions")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *caseID == "" {
		return fmt.Errorf("--case is required")
	}
	if *n <= 0 {
		return fmt.Errorf("--n must be greater than 0")
	}
	reg, err := cases.Load(*casesPath)
	if err != nil {
		return err
	}
	spec, err := reg.Find(*caseID)
	if err != nil {
		return err
	}

	runs := make([]reliability.RunResult, 0, *n)
	for i := 1; i <= *n; i++ {
		runID, err := runDemoCase(spec.ID, *root, *taxonomy, *policy, spec.TaskPacket, *contracts, *toolPolicy, *authority)
		result := reliability.RunResult{Index: i, RunID: runID, RunDir: filepath.Join(*root, "runs", runID), CreatedAt: time.Now().UTC()}
		if err != nil {
			result.Error = err.Error()
			runs = append(runs, result)
			continue
		}
		evalResult, err := evaluateRunDir(result.RunDir, *rules, spec.Suite)
		if err != nil {
			result.Error = err.Error()
		} else {
			result.EvalStatus = string(evalResult.Status)
		}
		runs = append(runs, result)
	}
	summary := reliability.Summarize(spec.ID, spec.Suite, runs)
	if err := reliability.Write(*root, summary); err != nil {
		return err
	}
	fmt.Printf("repeat completed: case=%s suite=%s total=%d passed=%d failed=%d pass_at_k=%t pass_all=%t flaky_rate=%.2f\n", summary.CaseID, summary.SuiteID, summary.Total, summary.Passed, summary.Failed, summary.PassAtK, summary.PassAll, summary.FlakyRate)
	fmt.Printf("result: %s/repeat_result.json\n", *root)
	if summary.Failed > 0 {
		return fmt.Errorf("repeat eval failed")
	}
	return nil
}

func promoteBadcase(args []string) error {
	fs := flag.NewFlagSet("promote-badcase", flag.ContinueOnError)
	runDir := fs.String("run", "", "ForgeX run directory containing badcase.yaml")
	out := fs.String("out", "", "output draft YAML path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *runDir == "" {
		return fmt.Errorf("--run is required")
	}
	if *out == "" {
		return fmt.Errorf("--out is required")
	}
	draft, err := promotion.Promote(*runDir, *out)
	if err != nil {
		return err
	}
	fmt.Printf("badcase promoted as draft: %s\n", draft.ID)
	fmt.Printf("review_required: %t\n", draft.ReviewRequired)
	fmt.Printf("review_status: %s\n", draft.ReviewStatus)
	fmt.Printf("out: %s\n", *out)
	return nil
}

func indexRun(args []string) error {
	fs := flag.NewFlagSet("index-run", flag.ContinueOnError)
	runDir := fs.String("run", "", "ForgeX run directory to index")
	root := fs.String("root", ".forgex", "ForgeX root directory")
	indexPath := fs.String("index", "", "SQLite index path; defaults to <root>/index.db")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *runDir == "" {
		return fmt.Errorf("--run is required")
	}
	path := *indexPath
	if path == "" {
		path = filepath.Join(*root, "index.db")
	}
	idx, err := storage.OpenSQLiteIndex(path)
	if err != nil {
		return err
	}
	defer idx.Close()
	if err := idx.IndexRunDir(context.Background(), *runDir); err != nil {
		return err
	}
	fmt.Printf("indexed run: %s\n", *runDir)
	fmt.Printf("index: %s\n", path)
	return nil
}

func listRuns(args []string) error {
	fs := flag.NewFlagSet("runs", flag.ContinueOnError)
	root := fs.String("root", ".forgex", "ForgeX root directory")
	indexPath := fs.String("index", "", "SQLite index path; defaults to <root>/index.db")
	limit := fs.Int("limit", 20, "maximum number of indexed runs to print")
	if err := fs.Parse(args); err != nil {
		return err
	}
	path := *indexPath
	if path == "" {
		path = filepath.Join(*root, "index.db")
	}
	idx, err := storage.OpenSQLiteIndex(path)
	if err != nil {
		return err
	}
	defer idx.Close()
	runs, err := idx.ListRuns(context.Background(), *limit)
	if err != nil {
		return err
	}
	for _, run := range runs {
		fmt.Printf("%s\t%s\t%s\terrors=%d\tstop=%s\teval=%s\tcategory=%s\t%s\n", run.ID, run.Status, run.Name, run.ErrorCount, run.StopAction, run.EvalStatus, run.LastCategory, run.Metrics.Summary())
	}
	return nil
}

func runContext(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("context subcommand required (available: inspect)")
	}
	switch args[0] {
	case "inspect":
		return inspectContext(args[1:])
	default:
		return fmt.Errorf("unknown context subcommand: %s", args[0])
	}
}

func inspectContext(args []string) error {
	fs := flag.NewFlagSet("context inspect", flag.ContinueOnError)
	runDir := fs.String("run", "", "ForgeX run directory to inspect")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *runDir == "" {
		return fmt.Errorf("--run is required")
	}
	state, err := forgexcontext.LoadRunContext(*runDir)
	if err != nil {
		return err
	}
	fmt.Print(forgexcontext.FormatInspect(state))
	return nil
}

func runPolicy(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("policy subcommand required (available: check)")
	}
	switch args[0] {
	case "check":
		return checkPolicy(args[1:])
	default:
		return fmt.Errorf("unknown policy subcommand: %s", args[0])
	}
}

func checkPolicy(args []string) error {
	fs := flag.NewFlagSet("policy check", flag.ContinueOnError)
	toolName := fs.String("tool", "", "tool name to check, e.g. demo.expensive_generation")
	authority := fs.String("authority", "L0", "authority level to evaluate, e.g. L0-L4")
	contractsPath := fs.String("contracts", demo.DefaultContractsPath, "tool contracts YAML path")
	policyPath := fs.String("policy", demo.DefaultToolPolicyPath, "tool policy YAML path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *toolName == "" {
		return fmt.Errorf("--tool is required")
	}
	contracts, err := toolgw.LoadContracts(*contractsPath)
	if err != nil {
		return err
	}
	contract, err := contracts.MustGet(*toolName)
	if err != nil {
		return err
	}
	cfg, err := forgexpolicy.LoadConfig(*policyPath)
	if err != nil {
		return err
	}
	decision := forgexpolicy.NewEngine(cfg).Decide("policy_check", forgexpolicy.AuthorityLevel(*authority), contract)
	fmt.Printf("tool=%s\tauthority=%s\taction=%s\trequires_hitl=%t\trisk=%s\tside_effect=%s\treason=%s\n", decision.ToolName, decision.Authority, decision.Action, decision.RequiresHITL, decision.RiskLevel, decision.SideEffect, decision.Reason)
	return nil
}

func runLessons(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("lessons subcommand required (available: list)")
	}
	switch args[0] {
	case "list":
		return listLessons(args[1:])
	default:
		return fmt.Errorf("unknown lessons subcommand: %s", args[0])
	}
}

func listLessons(args []string) error {
	fs := flag.NewFlagSet("lessons list", flag.ContinueOnError)
	runDir := fs.String("run", "", "ForgeX run directory whose lessons.jsonl should be listed")
	jsonOut := fs.Bool("json", false, "print raw lessons as JSON lines")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *runDir == "" {
		return fmt.Errorf("--run is required")
	}
	lessonsPath := filepath.Join(*runDir, "lessons.jsonl")
	lessons, err := readLessonsFile(lessonsPath)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("No lessons recorded.")
			return nil
		}
		return err
	}
	if len(lessons) == 0 {
		fmt.Println("No lessons recorded.")
		return nil
	}
	for _, lesson := range lessons {
		if *jsonOut {
			encoded, err := json.Marshal(lesson)
			if err != nil {
				return err
			}
			fmt.Println(string(encoded))
			continue
		}
		fmt.Printf("%s\tcategory=%s\tsource_run=%s\n", lesson.ID, lesson.Category, lesson.SourceRunID)
		if lesson.Title != "" {
			fmt.Printf("  title: %s\n", lesson.Title)
		}
		if lesson.Content != "" {
			fmt.Printf("  recommendation: %s\n", lesson.Content)
		}
		if trigger := strings.TrimSpace(lesson.Metadata["trigger"]); trigger != "" {
			fmt.Printf("  trigger: %s\n", trigger)
		}
		if evidence := strings.TrimSpace(lesson.Metadata["evidence"]); evidence != "" {
			fmt.Printf("  evidence: %s\n", evidence)
		}
	}
	return nil
}

func readLessonsFile(path string) ([]model.Lesson, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var lessons []model.Lesson
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var lesson model.Lesson
		if err := json.Unmarshal([]byte(line), &lesson); err != nil {
			return nil, fmt.Errorf("decode %s: %w", path, err)
		}
		lessons = append(lessons, lesson)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return lessons, nil
}

func countLessons(path string) int {
	lessons, err := readLessonsFile(path)
	if err != nil {
		return 0
	}
	return len(lessons)
}

func runCases(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("cases subcommand required (available: list, show, run)")
	}
	switch args[0] {
	case "list":
		return listCases(args[1:])
	case "show":
		return showCase(args[1:])
	case "run":
		return runRegisteredCase(args[1:])
	default:
		return fmt.Errorf("unknown cases subcommand: %s", args[0])
	}
}

func listCases(args []string) error {
	fs := flag.NewFlagSet("cases list", flag.ContinueOnError)
	casesPath := fs.String("cases", "configs/forgex/cases.yaml", "case registry YAML path")
	jsonOut := fs.Bool("json", false, "print the registry as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	reg, err := cases.Load(*casesPath)
	if err != nil {
		return err
	}
	if *jsonOut {
		encoded, err := json.MarshalIndent(reg, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(encoded))
		return nil
	}
	for _, c := range reg.Cases {
		fmt.Printf("%s\tsuite=%s\tpacket=%s\n", c.ID, c.Suite, c.TaskPacket)
		if c.Description != "" {
			fmt.Printf("  %s\n", c.Description)
		}
	}
	return nil
}

func showCase(args []string) error {
	fs := flag.NewFlagSet("cases show", flag.ContinueOnError)
	caseID := fs.String("case", "", "case id to show")
	casesPath := fs.String("cases", "configs/forgex/cases.yaml", "case registry YAML path")
	jsonOut := fs.Bool("json", false, "print the case as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *caseID == "" {
		return fmt.Errorf("--case is required")
	}
	reg, err := cases.Load(*casesPath)
	if err != nil {
		return err
	}
	spec, err := reg.Find(*caseID)
	if err != nil {
		return err
	}
	if *jsonOut {
		encoded, err := json.MarshalIndent(spec, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(encoded))
		return nil
	}
	fmt.Printf("id: %s\n", spec.ID)
	if spec.Description != "" {
		fmt.Printf("description: %s\n", spec.Description)
	}
	fmt.Printf("task_packet: %s\n", spec.TaskPacket)
	fmt.Printf("suite: %s\n", spec.Suite)
	fmt.Println("expected:")
	printExpectedField("status", spec.Expected.Status)
	printExpectedField("final_decision", spec.Expected.FinalDecision)
	printExpectedInt("errors", spec.Expected.Errors)
	printExpectedInt("lessons", spec.Expected.Lessons)
	printExpectedInt("lessons_min", spec.Expected.LessonsMin)
	printExpectedInt("validation_failed", spec.Expected.ValidationFailed)
	printExpectedInt("validation_failed_min", spec.Expected.ValidationFailedMin)
	printExpectedInt("artifacts_missing", spec.Expected.ArtifactsMissing)
	printExpectedInt("artifacts_missing_min", spec.Expected.ArtifactsMissingMin)
	return nil
}

func runRegisteredCase(args []string) error {
	fs := flag.NewFlagSet("cases run", flag.ContinueOnError)
	caseID := fs.String("case", "", "registered case id to run")
	root := fs.String("root", ".forgex", "root directory for run artifacts")
	casesPath := fs.String("cases", "configs/forgex/cases.yaml", "case registry YAML path")
	rules := fs.String("rules", "configs/forgex/eval_rules.yaml", "eval rules YAML path")
	taxonomy := fs.String("taxonomy", demo.DefaultTaxonomyPath, "failure taxonomy YAML path")
	policy := fs.String("policy", demo.DefaultPolicyPath, "stop policy YAML path")
	contracts := fs.String("contracts", demo.DefaultContractsPath, "tool contracts YAML path")
	toolPolicy := fs.String("tool-policy", demo.DefaultToolPolicyPath, "tool policy YAML path")
	authority := fs.String("authority", demo.DefaultAuthorityLevel, "authority level override for tool policy decisions")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *caseID == "" {
		return fmt.Errorf("--case is required")
	}
	reg, err := cases.Load(*casesPath)
	if err != nil {
		return err
	}
	spec, err := reg.Find(*caseID)
	if err != nil {
		return err
	}
	runID, err := runDemoCase(spec.ID, *root, *taxonomy, *policy, spec.TaskPacket, *contracts, *toolPolicy, *authority)
	if err != nil {
		return err
	}
	runDir := filepath.Join(*root, "runs", runID)
	result, err := evaluateRunDir(runDir, *rules, spec.Suite)
	if err != nil {
		return err
	}
	fmt.Printf("case completed: case=%s run_id=%s suite=%s eval=%s\n", spec.ID, runID, spec.Suite, result.Status)
	fmt.Printf("artifacts: %s\n", runDir)
	fmt.Printf("result: %s\n", filepath.Join(runDir, "eval_result.json"))
	fmt.Printf("scorecard: %s\n", filepath.Join(runDir, "scorecard.json"))
	if result.Status == model.EvalFailed {
		return fmt.Errorf("case eval failed")
	}
	return nil
}

func printExpectedField(name, value string) {
	if value != "" {
		fmt.Printf("  %s: %s\n", name, value)
	}
}

func printExpectedInt(name string, value *int) {
	if value != nil {
		fmt.Printf("  %s: %d\n", name, *value)
	}
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	root := fs.String("root", ".forgex", "root directory for ForgeX run artifacts")
	addr := fs.String("addr", ":8090", "HTTP listen address")
	if err := fs.Parse(args); err != nil {
		return err
	}
	server := productapi.New(productapi.Config{Root: *root, Version: version})
	fmt.Printf("ForgeX Control Plane API listening on %s (root=%s)\n", *addr, *root)
	return http.ListenAndServe(*addr, server.Handler())
}

func printHelp() {
	fmt.Print(`ForgeX - Agent Harness Control Plane

Usage:
  forgex <command>

Commands:
  version    Print ForgeX version
  help       Print this help message
  run-demo   Run a local harness demo (no external API calls)
  eval       Evaluate a run directory against eval rules
  eval-repeat Repeat a registered case and summarize reliability
  promote-badcase Promote a run badcase into a human-review draft
  index-run  Index one run directory into .forgex/index.db
  runs       List indexed runs
  context    Inspect run context/progress state
  policy     Check tool policy decisions
  lessons    List lessons derived from a run
  cases      List, show, or run registered golden cases
  serve      Start the local ForgeX Control Plane product API server

run-demo flags:
  --case      Demo case to run: generic-contract-violation | generic-contract-success (default: generic-contract-violation)
  --root      Root directory for run artifacts (default: .forgex)
  --taxonomy  Failure taxonomy YAML path
  --policy       Stop policy YAML path
  --packet       Task packet YAML path (default: the selected case's packet)
  --contracts    Tool contracts YAML path
  --tool-policy  Tool policy YAML path
  --authority    Authority level override for policy decisions (default: task packet authority_level or L0)

eval flags:
  --run    ForgeX run directory to evaluate, e.g. .forgex/runs/<run_id>
  --suite  Eval suite id (default: generic_contract_regression_v1)
  --rules  Eval rules YAML path (default: configs/forgex/eval_rules.yaml)
  Writes eval_result.json and scorecard.json into the run directory.

eval-repeat flags:
  --case   Registered case id, e.g. generic-contract-success
  --n      Number of serial repetitions (default: 1)
  --root   Root directory for repeated run artifacts (default: .forgex-repeat)
  --cases  Case registry YAML path (default: configs/forgex/cases.yaml)
  --rules  Eval rules YAML path (default: configs/forgex/eval_rules.yaml)
  Writes repeat_result.json under --root.

promote-badcase flags:
  --run  ForgeX run directory containing badcase.yaml
  --out  Output draft YAML path, e.g. examples/forgex/cases/<case_id>.yaml

index flags:
  --run    ForgeX run directory to index, e.g. .forgex/runs/<run_id>
  --root   ForgeX root directory (default: .forgex)
  --index  SQLite index path (default: <root>/index.db)

context flags:
  context inspect --run .forgex/runs/<run_id>

policy flags:
  policy check --tool <name> --authority L1 [--contracts path] [--policy path]

lessons flags:
  lessons list --run .forgex/runs/<run_id> [--json]

cases flags:
  cases list --cases configs/forgex/cases.yaml [--json]
  cases show --case <id> --cases configs/forgex/cases.yaml [--json]
  cases run --case <id> --root .forgex [--cases configs/forgex/cases.yaml] [--rules configs/forgex/eval_rules.yaml]

serve flags:
  --root  Root directory for ForgeX run artifacts (default: .forgex)
  --addr  HTTP listen address (default: :8090)

Examples:
  forgex run-demo --case generic-contract-violation --root .forgex
  forgex run-demo --case generic-contract-success --root .forgex
  forgex eval --run .forgex/runs/<run_id> --suite generic_contract_regression_v1
  forgex eval --run .forgex/runs/<run_id> --suite generic_contract_happy_v1
  forgex eval-repeat --case generic-contract-success --n 5 --root .forgex-repeat
  forgex promote-badcase --run .forgex/runs/<run_id> --out examples/forgex/cases/draft.yaml
  forgex index-run --run .forgex/runs/<run_id>
  forgex runs --limit 10
  forgex context inspect --run .forgex/runs/<run_id>
  forgex policy check --tool demo.expensive_generation --authority L1
  forgex lessons list --run .forgex/runs/<run_id>
  forgex cases list --cases configs/forgex/cases.yaml
  forgex cases show --case generic-contract-violation --cases configs/forgex/cases.yaml
  forgex cases run --case generic-contract-success --root .forgex
  forgex serve --root .forgex --addr :8090
`)
}
