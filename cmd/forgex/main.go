package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/castwell/forge/internal/forgex/demo"
	forgexeval "github.com/castwell/forge/internal/forgex/eval"
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

func printHelp() {
	fmt.Print(`ForgeX - Agent Harness Control Plane

Usage:
  forgex <command>

Commands:
  version    Print ForgeX version
  help       Print this help message
  run-demo   Run a local harness demo (no external API calls)
  eval       Evaluate a run directory against eval rules

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

Examples:
  forgex run-demo --case aihook-empty-images-refs --root .forgex
  forgex eval --run .forgex/runs/<run_id> --suite aihook_regression_v1
`)
}
