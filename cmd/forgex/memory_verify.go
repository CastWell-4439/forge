package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	agentcore "github.com/castwell/forge/internal/agent/core"
	serveworker "github.com/castwell/forge/internal/serve/worker"
)

// forgex memory verify — check stated facts against what the runs actually did.
//
// It reads the recorded tool calls and compares them with the claims extracted
// from memories. What it produces is a list of DISAGREEMENTS, each with the
// evidence behind it, and no verdict: which side is wrong is not knowable from
// this evidence, because the sample is what the runs happened to touch rather
// than a census of the project.
//
// It changes nothing. Like every other lifecycle command, the decision belongs
// to a person — memory is shared and its contents are model-generated.
const memoryVerifyUsage = `forgex memory verify — check memory claims against recorded evidence

Usage:
  forgex memory verify [--root PATH] [--claims FILE] [--min-evidence N] [--max-runs N] [--json]

It reads the runs' recorded tool calls and reports where a memory's claim
disagrees with what the tools touched. It reports the DISAGREEMENT only:
the evidence is a sample, so which side is wrong is for a person to decide.

Nothing is changed.

Flags:
  --root PATH        ForgeX root directory (default .forgex)
  --claims FILE      JSON file of {entry_id: [assertions]} to check; when
                     omitted, the command reports the evidence it found and
                     checks nothing (there is no cross-plane memory export yet)
  --min-evidence N   observations needed to report a language conflict (default 5)
  --max-runs N       how many recent runs to read (default 20)
  --json             print the result as JSON instead of a table

What can be checked today:
  language   a memory says the project is written in X, the files touched say Y
  path       a memory names a path that was tried and never worked

Services and versions are not checked yet: those need the values to be traded
across planes, which the export channel will carry.
`

// runMemoryVerify implements the verify subcommand.
func runMemoryVerify(args []string) error {
	fs := flag.NewFlagSet("memory verify", flag.ContinueOnError)
	root := fs.String("root", ".forgex", "ForgeX root directory")
	claimsPath := fs.String("claims", "", "JSON file of claims to check")
	minEvidence := fs.Int("min-evidence", 0, "observations needed to report a language conflict")
	maxRuns := fs.Int("max-runs", 0, "how many recent runs to read")
	asJSON := fs.Bool("json", false, "print the result as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg := agentcore.DefaultVerificationConfig()
	if *minEvidence > 0 {
		cfg.MinEvidence = *minEvidence
	}

	// Evidence first: without it there is nothing to check, and saying so is
	// more useful than printing an empty result that looks like agreement.
	calls, err := loadEvidenceForRoot(*root, *maxRuns, cfg.MaxCalls)
	if err != nil {
		return err
	}
	if len(calls) == 0 {
		fmt.Fprintf(os.Stdout,
			"no recorded tool calls under %s/runs; nothing to verify against\n"+
				"(evidence comes from runs that were observed by the runtime observer)\n", *root)
		return nil
	}

	claims, err := loadClaims(*claimsPath)
	if err != nil {
		return err
	}
	if len(claims) == 0 {
		// No claims to check is a real outcome and the message explains why
		// rather than looking like a bug: the memories live in the agent
		// plane's store, and this command does not reach into it.
		fmt.Fprintf(os.Stdout,
			"read %d tool call(s) from %s/runs\n", len(calls), *root)
		fmt.Fprintln(os.Stdout,
			"no claims to check: pass --claims FILE (the memory store is the agent plane's, "+
				"and this command reads what the control plane can see)")
		return nil
	}

	result := agentcore.VerifyClaims(claims, calls, cfg)

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	}
	printVerification(result, *root)
	return nil
}

// printVerification renders the disagreement table.
//
// The header repeats the contract every run rather than only in help text: a
// reviewer reading a list of disagreements should see, on the same screen, that
// no side has been declared wrong.
func printVerification(result agentcore.VerificationResult, root string) {
	fmt.Fprintf(os.Stdout, "checked %d claim(s) against %d tool call(s) from %s/runs\n",
		result.FactsChecked, result.CallsExamined, root)
	fmt.Fprintln(os.Stdout, "disagreements only — the evidence is a sample; no side is declared wrong")
	fmt.Fprintln(os.Stdout)

	if len(result.Conflicts) == 0 {
		fmt.Fprintln(os.Stdout, "no disagreements found")
		return
	}

	for _, c := range result.Conflicts {
		fmt.Fprintf(os.Stdout, "%-8s [%s] %.2f\n", strings.ToUpper(c.Kind), c.EntryID, c.Confidence)
		fmt.Fprintf(os.Stdout, "         memory says:   %s\n", c.Claimed)
		fmt.Fprintf(os.Stdout, "         evidence shows: %s\n", c.Observed)
		fmt.Fprintf(os.Stdout, "         where:         %s\n", c.Evidence)
	}
	fmt.Fprintf(os.Stdout, "\n%d disagreement(s). Decide by hand: the memory may be stale, "+
		"or the recent runs may have touched only part of the project.\n", len(result.Conflicts))
}

// loadClaims reads a claims file: {"entry_id": [{kind, value}, ...]}.
//
// The shape is the extraction passes' output, so a review can be piped into a
// verification without a transformation step. An absent path yields no claims,
// which is the honest state while the cross-plane export does not exist.
func loadClaims(path string) (map[string][]agentcore.Assertion, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read claims %s: %w", path, err)
	}
	var claims map[string][]agentcore.Assertion
	if err := json.Unmarshal(data, &claims); err != nil {
		return nil, fmt.Errorf("parse claims %s: %w (want {\"entry_id\": [{\"kind\":..., \"value\":...}]})", path, err)
	}
	return claims, nil
}

// loadEvidenceForRoot loads tool calls through the serve layer's projector.
//
// The projector lives there because it knows how a recorded call is shaped,
// which is control-plane knowledge; this file only decides which runs to read
// and what to do with the result.
func loadEvidenceForRoot(root string, maxRuns, maxCalls int) ([]agentcore.EvidenceToolCall, error) {
	return serveworker.LoadEvidence(root, maxRuns, maxCalls)
}
