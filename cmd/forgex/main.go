package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	forgexcontext "github.com/castwell/forge/internal/forgex/context"
	"github.com/castwell/forge/internal/forgex/demo"
	forgexeval "github.com/castwell/forge/internal/forgex/eval"
	"github.com/castwell/forge/internal/forgex/model"
	forgexpolicy "github.com/castwell/forge/internal/forgex/policy"
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

	switch *caseName {
	case "generic-contract-violation":
		runID, err := demo.RunGenericContractViolationDemoWithControl(context.Background(), *root, *taxonomy, *policy, *packet, *contracts, *toolPolicy, *authority)
		if err != nil {
			return err
		}
		fmt.Printf("demo completed: run_id=%s\n", runID)
		fmt.Printf("artifacts: %s/runs/%s/\n", *root, runID)
		return nil
	case "generic-contract-success":
		runID, err := demo.RunGenericContractSuccessDemoWithControl(context.Background(), *root, *taxonomy, *policy, *packet, *contracts, *toolPolicy, *authority)
		if err != nil {
			return err
		}
		fmt.Printf("demo completed: run_id=%s\n", runID)
		fmt.Printf("artifacts: %s/runs/%s/\n", *root, runID)
		return nil
	default:
		return fmt.Errorf("unknown demo case: %s (available: generic-contract-violation, generic-contract-success)", *caseName)
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
	result, err := forgexeval.Run(context.Background(), *runDir, *rules, *suite)
	if err != nil {
		return err
	}
	fmt.Printf("eval completed: run_id=%s suite=%s status=%s\n", result.RunID, result.SuiteID, result.Status)
	fmt.Printf("result: %s/eval_result.json\n", *runDir)
	if result.Status == "failed" {
		return fmt.Errorf("eval failed")
	}
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

func printHelp() {
	fmt.Print(`ForgeX - Agent Harness Control Plane

Usage:
  forgex <command>

Commands:
  version    Print ForgeX version
  help       Print this help message
  run-demo   Run a local harness demo (no external API calls)
  eval       Evaluate a run directory against eval rules
  index-run  Index one run directory into .forgex/index.db
  runs       List indexed runs
  context    Inspect run context/progress state
  policy     Check tool policy decisions
  lessons    List lessons derived from a run

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

Examples:
  forgex run-demo --case generic-contract-violation --root .forgex
  forgex run-demo --case generic-contract-success --root .forgex
  forgex eval --run .forgex/runs/<run_id> --suite generic_contract_regression_v1
  forgex eval --run .forgex/runs/<run_id> --suite generic_contract_happy_v1
  forgex index-run --run .forgex/runs/<run_id>
  forgex runs --limit 10
  forgex context inspect --run .forgex/runs/<run_id>
  forgex policy check --tool demo.expensive_generation --authority L1
  forgex lessons list --run .forgex/runs/<run_id>
`)
}
