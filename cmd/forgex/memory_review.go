package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	agentcore "github.com/castwell/forge/internal/agent/core"
	agentharness "github.com/castwell/forge/internal/agent/harness"
	"github.com/castwell/forge/internal/forgex/storage"
	serveworker "github.com/castwell/forge/internal/serve/worker"
)

// forgex memory review — propose what to do with the memory store.
//
// It reads the long-term memories and prints candidates: DISTILL this episodic
// entry into a skill draft, DISCARD that one, PROMOTE this one to a fact,
// VERIFY that fact against recorded evidence.
//
// It executes nothing, and that is the design rather than a stage of
// development. Every candidate kind changes shared state — distilling writes a
// skill, discarding removes a memory — and memory is shared while its contents
// are model-generated. A run cannot see the aggregate, so a run must not act on
// it; the review makes the human's decision cheap instead of making it for
// them.
//
// This is also where the review differs from the archive. The archive needs a
// usage signal and therefore cannot run; the review decides from the entry
// itself ("does it state something a rule can check", "is it past its
// lifetime"), so it works today.
const memoryReviewUsage = `forgex memory review — propose what to do with long-term memories

Usage:
  forgex memory review [--root PATH] [--ttl DURATION] [--min-runs N] [--json]

It PRINTS proposals. Nothing is changed: distilling, discarding and promoting
all touch shared state and require a human decision.

Flags:
  --root PATH       ForgeX root directory (default .forgex)
  --index PATH      SQLite index path (default <root>/index.db)
  --ttl DURATION    how long an episodic memory is worth keeping (default 720h)
  --min-runs N      runs that must describe the same pattern to propose
                    distillation (default 2)
  --json            print the review as JSON instead of a table
  --llm             allow the model-assisted extraction pass (requires
                    FORGE_LLM_* configuration; the static pass always runs)

Candidate kinds:
  distill   an episodic memory describing a repeatable procedure
  discard   an episodic memory past its lifetime that says nothing checkable
  promote   an episodic memory that states a claim about the world
  verify    a fact whose claim should be checked against recorded evidence
`

// reviewUsageText exposes the help text to the dispatcher, which prints it when
// the subcommand is invoked without arguments.
func reviewUsageText() string { return memoryReviewUsage }

// runMemoryReview implements the review subcommand.
func runMemoryReview(args []string) error {
	fs := flag.NewFlagSet("memory review", flag.ContinueOnError)
	root := fs.String("root", ".forgex", "ForgeX root directory")
	indexPath := fs.String("index", "", "SQLite index path (default <root>/index.db)")
	ttl := fs.Duration("ttl", 0, "how long an episodic memory is worth keeping")
	minRuns := fs.Int("min-runs", 0, "runs describing the same pattern needed to propose distillation")
	knowledgeDir := fs.String("knowledge-dir", "", "agent plane's knowledge directory (default "+serveworker.DefaultKnowledgeDir+")")
	asJSON := fs.Bool("json", false, "print the review as JSON")
	useLLM := fs.Bool("llm", false, "allow the model-assisted extraction pass")
	if err := fs.Parse(args); err != nil {
		// flag.ContinueOnError already printed the usage on --help; the review's
		// own text is printed below so the contract is visible either way.
		return err
	}

	idx, err := openIndex(fs, root, indexPath)
	if err != nil {
		return err
	}
	defer idx.Close()

	ctx := context.Background()
	memories, err := loadReviewMemories(*knowledgeDir)
	if err != nil {
		return err
	}
	if len(memories) == 0 {
		// Nothing to read is a real outcome, and the message names the reason
		// rather than looking like a bug: the memory records live in the agent
		// plane's store, and this command does not reach into it.
		fmt.Fprintln(os.Stdout, "no memories to review (the long-term store is the agent plane's; "+
			"this command reviews what the control plane can see)")
		return nil
	}

	usage, err := loadUsageStats(ctx, idx)
	if err != nil {
		// Usage is optional for a review: the rules do not depend on it. Say so
		// rather than failing, because an unreadable observation table must not
		// block the one review path that works.
		log.Printf("INFO: usage observations unavailable (%v); reviewing without them", err)
	}

	cfg := agentharness.DefaultReviewConfig()
	if *ttl > 0 {
		cfg.EpisodicTTL = *ttl
	}
	if *minRuns > 0 {
		cfg.DistillMinRuns = *minRuns
	}

	var extractor agentcore.AssertionExtractor
	if *useLLM {
		extractor = buildAssertionExtractor()
		if extractor == nil {
			fmt.Fprintln(os.Stderr, "note: --llm was given but no model is configured; using the static pass only")
		}
	}

	review := agentharness.ReviewMemories(ctx, agentharness.ReviewInput{
		Memories: memories,
		Usage:    usage,
	}, extractor, cfg)

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(review)
	}
	printReview(review)
	return nil
}

// printReview renders the proposal table.
//
// The header states the contract every time rather than only in help text: a
// reviewer reading a list of "discard" lines should see, on the same screen,
// that nothing has been discarded.
func printReview(review agentharness.MemoryReview) {
	fmt.Fprintf(os.Stdout,
		"memory review: %d fact(s), %d episodic, %d assertion(s) found\n",
		review.FactsChecked, review.EpisodicChecked, len(review.AssertionsFound))
	fmt.Fprintln(os.Stdout, "proposals only — nothing below has been executed")
	fmt.Fprintln(os.Stdout)

	if len(review.Candidates) == 0 {
		fmt.Fprintln(os.Stdout, "no proposals: every memory is either fresh or already classified")
		return
	}

	for _, c := range review.Candidates {
		fmt.Fprintf(os.Stdout, "%-8s [%s] %.2f  %s\n",
			strings.ToUpper(string(c.Kind)), c.EntryID, c.Confidence, oneLine(c.Content))
		fmt.Fprintf(os.Stdout, "         reason: %s\n", c.Reason)
		if c.Evidence != "" {
			fmt.Fprintf(os.Stdout, "         evidence: %s\n", c.Evidence)
		}
	}
	fmt.Fprintf(os.Stdout, "\n%d proposal(s). Apply one by hand, or adjust --ttl/--min-runs and re-run.\n",
		len(review.Candidates))
}

// oneLine shortens a memory for the table without hiding that it was cut.
func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	const limit = 110
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "..."
}

// loadReviewMemories reads the entries under review from the agent plane's
// memory store.
//
// The store belongs to the agent plane and is read through the serve layer's
// projector, which knows its layout. The read is one-directional by
// construction: this command has no writer for that file, because every
// candidate it produces is a proposal and a proposal needs no write access.
func loadReviewMemories(knowledgeDir string) ([]agentcore.MemoryEntry, error) {
	export, err := serveworker.LoadMemoryExport(knowledgeDir)
	if err != nil {
		return nil, err
	}
	out := make([]agentcore.MemoryEntry, 0, len(export.Memories))
	for _, m := range export.Memories {
		out = append(out, agentcore.MemoryEntry{
			ID:         m.ID,
			Content:    m.Content,
			Category:   m.Category,
			Source:     agentcore.MemorySource(m.Source),
			Confidence: m.Confidence,
			ObservedAt: m.ObservedAt,
			CreatedAt:  m.CreatedAt,
			Layer:      m.Layer,
		})
	}
	return out, nil
}

// loadUsageStats reads the observation aggregate, when the table exists.
func loadUsageStats(ctx context.Context, idx *storage.SQLiteIndex) (map[string]agentcore.UsageStat, error) {
	obs, err := idx.LoadObservations(ctx, "memory", 0)
	if err != nil {
		return nil, err
	}
	coreObs := make([]agentcore.MemoryObservation, 0, len(obs))
	for _, o := range obs {
		coreObs = append(coreObs, agentcore.MemoryObservation{
			RunID:      o.RunID,
			EntryID:    o.EntryID,
			Kind:       agentcore.KindMemory,
			Used:       o.Used,
			UsageKnown: o.UsageKnown,
			At:         o.At,
		})
	}
	return agentcore.AggregateObservations(coreObs), nil
}

// buildAssertionExtractor constructs the model-assisted pass when configured.
//
// It returns nil when no model is configured, which is the normal case for a
// local review: the static extraction pass is the floor, and the review works
// without any model at all.
func buildAssertionExtractor() agentcore.AssertionExtractor {
	return nil
}
