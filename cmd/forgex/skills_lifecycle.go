package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/castwell/forge/internal/forgex/skillpack"
)

// Skill lifecycle: retiring and re-reviewing published skills.
//
// The distillation path already has strong gates — a draft must pass the
// publish checks and a human must write its readme — so what a skill store
// lacks is not a higher entry bar. It is everything that happens AFTER
// publication: a skill that stops applying, and a skill nobody has looked at
// since the conventions moved.
//
// Two rules shape these commands, both inherited from the memory lifecycle:
//
//   - DEPRECATION IS NOT DELETION. A skill encodes someone's reviewed
//     judgement; retiring it is a decision that can be revisited, and the file
//     keeps the evidence that the judgement was once made.
//   - NOTHING IS AUTOMATIC. `stale` reports; `deprecate` is asked for. A skill
//     is shared state, and its status changes on a person's decision.
const skillsLifecycleUsage = `forgex skills — lifecycle for published skills

Usage:
  forgex skills deprecate --skill ID --reason TEXT [--skills-dir DIR]
  forgex skills restore   --skill ID [--skills-dir DIR]
  forgex skills review    --skill ID [--skills-dir DIR]
  forgex skills stale     [--stale-after DURATION] [--skills-dir DIR]

Flags:
  --skill ID            the skill to act on
  --reason TEXT         why it is being retired (required for deprecate)
  --skills-dir DIR      published skills directory (default ` + defaultSkillsDir + `)
  --stale-after DUR     how long a review is trusted (default 4320h = 180 days)
  --json                print as JSON where applicable

What each does:
  deprecate   status becomes "deprecated"; the file stays and the reason is kept
  restore     clears the deprecation, returning it to "published"
  review      records that a human confirmed it still applies, stamped with the time
  stale       lists reviewed skills whose confirmation has aged out

Nothing is deleted, and nothing changes status without being asked.`

// runSkillsLifecycle dispatches the lifecycle subcommands.
func runSkillsLifecycle(sub string, args []string) error {
	switch sub {
	case "deprecate":
		return skillsDeprecate(args)
	case "restore":
		return skillsRestore(args)
	case "review":
		return skillsReview(args)
	case "stale":
		return skillsStale(args)
	default:
		return fmt.Errorf("unknown skills lifecycle subcommand: %s", sub)
	}
}

// skillsDeprecate retires a published skill.
//
// The reason is required, and that is not ceremony: whoever finds a retired
// skill needs to know whether the world moved or the skill was wrong, and
// "deprecated" on its own leaves them to guess.
func skillsDeprecate(args []string) error {
	fs := flag.NewFlagSet("skills deprecate", flag.ContinueOnError)
	id := fs.String("skill", "", "skill id")
	reason := fs.String("reason", "", "why it is being retired (required)")
	dir := fs.String("skills-dir", defaultSkillsDir, "published skills directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" {
		return fmt.Errorf("--skill is required")
	}
	if strings.TrimSpace(*reason) == "" {
		return fmt.Errorf("--reason is required: a retired skill must say why")
	}

	pack, err := skillpack.NewStore(*dir).Deprecate(*id, *reason, time.Now().UTC())
	if err != nil {
		return err
	}
	fmt.Printf("deprecated %s\n", pack.Metadata.ID)
	fmt.Printf("  reason: %s\n", pack.Metadata.DeprecationReason)
	fmt.Printf("  the file is kept; restore with: forgex skills restore --skill %s\n", pack.Metadata.ID)
	return nil
}

// skillsRestore returns a deprecated skill to published.
func skillsRestore(args []string) error {
	fs := flag.NewFlagSet("skills restore", flag.ContinueOnError)
	id := fs.String("skill", "", "skill id")
	dir := fs.String("skills-dir", defaultSkillsDir, "published skills directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" {
		return fmt.Errorf("--skill is required")
	}
	pack, err := skillpack.NewStore(*dir).Restore(*id, time.Now().UTC())
	if err != nil {
		return err
	}
	fmt.Printf("restored %s to %s\n", pack.Metadata.ID, pack.Metadata.Status)
	return nil
}

// skillsReview records a fresh human confirmation.
//
// It stamps the time as well as the status: staleness is a question about WHEN
// a skill was last confirmed, and the status alone cannot answer it.
func skillsReview(args []string) error {
	fs := flag.NewFlagSet("skills review", flag.ContinueOnError)
	id := fs.String("skill", "", "skill id")
	dir := fs.String("skills-dir", defaultSkillsDir, "published skills directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" {
		return fmt.Errorf("--skill is required")
	}
	at := time.Now().UTC()
	pack, err := skillpack.NewStore(*dir).MarkReviewed(*id, at)
	if err != nil {
		return err
	}
	fmt.Printf("reviewed %s at %s\n", pack.Metadata.ID, at.Format(time.RFC3339))
	return nil
}

// skillsStale lists reviewed skills whose confirmation has aged out.
//
// It is a report and nothing else: no status changes, because a skill that has
// not been looked at recently may be perfectly fine, and treating age as
// evidence against it would retire working skills on a schedule.
func skillsStale(args []string) error {
	fs := flag.NewFlagSet("skills stale", flag.ContinueOnError)
	dir := fs.String("skills-dir", defaultSkillsDir, "published skills directory")
	staleAfter := fs.Duration("stale-after", 0, "how long a review is trusted")
	asJSON := fs.Bool("json", false, "print as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}

	window := *staleAfter
	if window <= 0 {
		window = skillpack.DefaultStaleAfter
	}
	now := time.Now().UTC()

	store := skillpack.NewStore(*dir)
	stale, err := store.StaleSkills(now, window)
	if err != nil {
		return err
	}

	if *asJSON {
		type row struct {
			ID         string    `json:"id"`
			Version    string    `json:"version"`
			ReviewedAt time.Time `json:"reviewed_at"`
			AgeDays    int       `json:"age_days"`
		}
		rows := make([]row, 0, len(stale))
		for _, p := range stale {
			rows = append(rows, row{
				ID:         p.Metadata.ID,
				Version:    p.Metadata.Version,
				ReviewedAt: p.Metadata.ReviewedAt,
				AgeDays:    int(now.Sub(p.Metadata.ReviewedAt).Hours() / 24),
			})
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
	}

	if len(stale) == 0 {
		fmt.Printf("no skills are due for review (window %s)\n", window)
		return nil
	}
	fmt.Printf("%d skill(s) reviewed more than %s ago:\n", len(stale), window)
	for _, p := range stale {
		days := int(now.Sub(p.Metadata.ReviewedAt).Hours() / 24)
		fmt.Printf("  %-40s v%-8s last reviewed %d day(s) ago\n",
			p.Metadata.ID, p.Metadata.Version, days)
	}
	fmt.Println("\nThis is a report: nothing has changed status.")
	fmt.Println("Confirm one with: forgex skills review --skill ID")
	fmt.Println("Retire one with:  forgex skills deprecate --skill ID --reason TEXT")
	return nil
}
