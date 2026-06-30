package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	forgexcontext "github.com/castwell/forge/internal/forgex/context"
	"github.com/castwell/forge/internal/forgex/demo"
	forgexeval "github.com/castwell/forge/internal/forgex/eval"
	"github.com/castwell/forge/internal/forgex/storage"
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
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", args[0])
		printHelp()
		os.Exit(2)
	}
}

// runDemo parses run-demo flags and dispatches to the requested demo case.
func runDemo(args []string) error {
	fs := flag.NewFlagSet("run-demo", flag.ContinueOnError)
	caseName := fs.String("case", "aihook-empty-images-refs", "demo case to run")
	root := fs.String("root", ".forgex", "root directory for run artifacts")
	taxonomy := fs.String("taxonomy", demo.DefaultTaxonomyPath, "failure taxonomy YAML path")
	policy := fs.String("policy", demo.DefaultPolicyPath, "stop policy YAML path")
	packet := fs.String("packet", demo.DefaultPacketPath, "task packet YAML path")
	if err := fs.Parse(args); err != nil {
		return err
	}

	switch *caseName {
	case "aihook-empty-images-refs":
		runID, err := demo.RunAIHookEmptyImagesRefsDemo(context.Background(), *root, *taxonomy, *policy, *packet)
		if err != nil {
			return err
		}
		fmt.Printf("demo completed: run_id=%s\n", runID)
		fmt.Printf("artifacts: %s/runs/%s/\n", *root, runID)
		return nil
	default:
		return fmt.Errorf("unknown demo case: %s (available: aihook-empty-images-refs)", *caseName)
	}
}

func runEval(args []string) error {
	fs := flag.NewFlagSet("eval", flag.ContinueOnError)
	runDir := fs.String("run", "", "ForgeX run directory to evaluate")
	suite := fs.String("suite", "aihook_regression_v1", "eval suite id")
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
		fmt.Printf("%s\t%s\t%s\terrors=%d\tstop=%s\teval=%s\tcategory=%s\n", run.ID, run.Status, run.Name, run.ErrorCount, run.StopAction, run.EvalStatus, run.LastCategory)
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

run-demo flags:
  --case      Demo case to run (default: aihook-empty-images-refs)
  --root      Root directory for run artifacts (default: .forgex)
  --taxonomy  Failure taxonomy YAML path
  --policy    Stop policy YAML path
  --packet    Task packet YAML path

eval flags:
  --run    ForgeX run directory to evaluate, e.g. .forgex/runs/<run_id>
  --suite  Eval suite id (default: aihook_regression_v1)
  --rules  Eval rules YAML path (default: configs/forgex/eval_rules.yaml)

index flags:
  --run    ForgeX run directory to index, e.g. .forgex/runs/<run_id>
  --root   ForgeX root directory (default: .forgex)
  --index  SQLite index path (default: <root>/index.db)

context flags:
  context inspect --run .forgex/runs/<run_id>

Examples:
  forgex run-demo --case aihook-empty-images-refs --root .forgex
  forgex eval --run .forgex/runs/<run_id> --suite aihook_regression_v1
  forgex index-run --run .forgex/runs/<run_id>
  forgex runs --limit 10
  forgex context inspect --run .forgex/runs/<run_id>
`)
}
