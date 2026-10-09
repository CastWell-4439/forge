package skillpack

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Store reads and writes SkillPack documents under one skills root:
// published packs live in the root, drafts in a drafts/ subdirectory, so a
// pack is visible only after it passed its gates.
type Store struct {
	Root string
}

// NewStore creates a store rooted at a skills directory.
func NewStore(root string) Store {
	if strings.TrimSpace(root) == "" {
		root = "configs/forgex/skills"
	}
	return Store{Root: root}
}

// DraftsDir is where distillation writes its drafts.
func (s Store) DraftsDir() string { return filepath.Join(s.Root, "drafts") }

// Save writes a pack. draft selects the drafts directory or the published one
// and stamps the matching status, so the status field can never disagree with
// where the file lives.
func (s Store) Save(p Pack, draft bool) (string, error) {
	if p.Metadata.ID == "" {
		return "", fmt.Errorf("save: pack id is required")
	}
	dir := s.Root
	if draft {
		dir = s.DraftsDir()
		p.Metadata.Status = StatusDraft
	} else {
		p.Metadata.Status = StatusPublished
	}
	if p.Kind == "" {
		p.Kind = Kind
	}
	if p.APIVersion == "" {
		p.APIVersion = APIVersion
	}

	data, err := yaml.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("save %s: %w", p.Metadata.ID, err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("save %s: %w", p.Metadata.ID, err)
	}
	path := filepath.Join(dir, p.Metadata.ID+".yaml")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", fmt.Errorf("save %s: %w", p.Metadata.ID, err)
	}
	return path, nil
}

// Load reads a published pack by id.
func (s Store) Load(id string) (Pack, error) {
	return readPack(filepath.Join(s.Root, id+".yaml"))
}

// LoadPath reads a pack from an explicit path; used for drafts under review.
func LoadPath(path string) (Pack, error) {
	return readPack(path)
}

// List returns every published pack, sorted by id.
//
// Deprecated packs are excluded by default. "What skills do we have" almost
// always means "what can an agent use", and a retired skill in that list would
// be an invitation to apply something the team decided against. ListAll is the
// explicit way to see everything.
func (s Store) List() ([]Pack, error) {
	all, err := s.ListAll()
	if err != nil {
		return nil, err
	}
	out := make([]Pack, 0, len(all))
	for _, p := range all {
		if p.Metadata.IsDeprecated() {
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

// ListAll returns every published pack including deprecated ones.
func (s Store) ListAll() ([]Pack, error) {
	entries, err := os.ReadDir(s.Root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("list: %w", err)
	}
	var out []Pack
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		pack, err := readPack(filepath.Join(s.Root, entry.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, pack)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Metadata.ID < out[j].Metadata.ID })
	return out, nil
}

// Deprecate retires a published pack without deleting it.
//
// The file stays, the status changes, and the reason is recorded. Three things
// follow from that, and all three are the point:
//
//   - the decision is reversible: a skill retired by mistake is restored by
//     clearing the status, not by recovering a deleted file;
//   - the reason survives for whoever finds it later, which is what turns
//     "deprecated" from a mystery into a statement about the world;
//   - nothing is destroyed, so the fact that the judgement was once made
//     remains visible.
//
// It refuses a draft: a draft was never in use, so there is nothing to retire —
// discarding it is a different act, and conflating the two would let a
// deprecation quietly mean "I did not finish this".
func (s Store) Deprecate(id, reason string, at time.Time) (Pack, error) {
	if strings.TrimSpace(reason) == "" {
		return Pack{}, fmt.Errorf("deprecate %s: a reason is required", id)
	}
	pack, err := s.Load(id)
	if err != nil {
		// A draft lives in a different directory, so Load cannot see it. Saying
		// "file not found" for something that exists is a misleading error: it
		// sends the reader looking for a missing file instead of at the draft
		// they are trying to retire.
		if draft, derr := readPack(filepath.Join(s.DraftsDir(), id+".yaml")); derr == nil {
			_ = draft
			return Pack{}, fmt.Errorf("deprecate %s: it is a draft, not a published skill "+
				"(a draft was never in use, so there is nothing to retire)", id)
		}
		return Pack{}, err
	}
	if pack.Metadata.Status == StatusDraft {
		return Pack{}, fmt.Errorf("deprecate %s: it is a draft, not a published skill", id)
	}
	if pack.Metadata.IsDeprecated() {
		// Already retired: keep the FIRST retirement time and reason. Refreshing
		// them on every run would erase when and why it actually happened.
		return pack, nil
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	pack.Metadata.Status = StatusDeprecated
	pack.Metadata.DeprecatedAt = at
	pack.Metadata.DeprecationReason = reason

	path, err := s.savePreservingStatus(pack)
	if err != nil {
		return Pack{}, err
	}
	_ = path
	return pack, nil
}

// Restore returns a deprecated pack to published.
//
// It clears the deprecation fields rather than leaving them: a restored skill
// that still carries a retirement reason would read as both published and
// retired, and the next reader would have to guess which field to believe.
func (s Store) Restore(id string, at time.Time) (Pack, error) {
	pack, err := s.Load(id)
	if err != nil {
		return Pack{}, err
	}
	if !pack.Metadata.IsDeprecated() {
		return Pack{}, fmt.Errorf("restore %s: it is not deprecated", id)
	}
	pack.Metadata.Status = StatusPublished
	pack.Metadata.DeprecatedAt = time.Time{}
	pack.Metadata.DeprecationReason = ""
	if _, err := s.savePreservingStatus(pack); err != nil {
		return Pack{}, err
	}
	return pack, nil
}

// MarkReviewed records that a human confirmed this skill still applies.
//
// It stamps the time as well as the status, because staleness is a question
// about WHEN and ReviewStatus alone cannot answer it.
func (s Store) MarkReviewed(id string, at time.Time) (Pack, error) {
	pack, err := s.Load(id)
	if err != nil {
		return Pack{}, err
	}
	if pack.Metadata.IsDeprecated() {
		return Pack{}, fmt.Errorf("review %s: it is deprecated; restore it first", id)
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	pack.Metadata.ReviewStatus = ReviewDone
	pack.Metadata.ReviewedAt = at
	if _, err := s.savePreservingStatus(pack); err != nil {
		return Pack{}, err
	}
	return pack, nil
}

// RecordVerification stores the outcome of running a skill's bound cases.
//
// It records failures as well as successes, and that symmetry is the point: a
// skill whose last run failed is exactly the one a reader needs to find, and
// writing only successes would make "never verified" and "verified and broken"
// look identical.
//
// Recording does not change the skill's status. A regression may mean the skill
// is wrong, or that a case went stale, or that the environment moved — three
// different repairs, none of which this function can choose between. It
// reports; a person decides.
func (s Store) RecordVerification(id string, failedCases []string, at time.Time) (Pack, error) {
	pack, err := s.Load(id)
	if err != nil {
		return Pack{}, err
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	pack.Metadata.LastVerifiedAt = at
	if len(failedCases) == 0 {
		pack.Metadata.LastVerifyStatus = VerifyPassed
		// Clear the previous failures: a stale list of case names next to a
		// "passed" status would describe a failure that no longer exists.
		pack.Metadata.LastVerifyFailedCases = nil
	} else {
		pack.Metadata.LastVerifyStatus = VerifyFailed
		pack.Metadata.LastVerifyFailedCases = append([]string(nil), failedCases...)
	}
	if _, err := s.savePreservingStatus(pack); err != nil {
		return Pack{}, err
	}
	return pack, nil
}

// ListNeedingAttention returns published skills whose last verification failed.
//
// Deprecated skills are excluded: a retired skill is out of use, and reporting
// its regression would ask someone to fix something nobody runs.
func (s Store) ListNeedingAttention() ([]Pack, error) {
	all, err := s.List()
	if err != nil {
		return nil, err
	}
	var out []Pack
	for _, p := range all {
		if p.Metadata.NeedsAttention() {
			out = append(out, p)
		}
	}
	return out, nil
}

// savePreservingStatus writes a pack without letting Save's draft flag decide
// its status.
//
// Save derives the status from WHERE the file goes, which is right for creating
// a pack and wrong for changing one: deprecating and reviewing must not move a
// file between directories or overwrite the status they just set.
func (s Store) savePreservingStatus(p Pack) (string, error) {
	if p.Kind == "" {
		p.Kind = Kind
	}
	if p.APIVersion == "" {
		p.APIVersion = APIVersion
	}
	data, err := yaml.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("save %s: %w", p.Metadata.ID, err)
	}
	if err := os.MkdirAll(s.Root, 0o755); err != nil {
		return "", fmt.Errorf("save %s: %w", p.Metadata.ID, err)
	}
	path := filepath.Join(s.Root, p.Metadata.ID+".yaml")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", fmt.Errorf("save %s: %w", p.Metadata.ID, err)
	}
	return path, nil
}

// StaleSkills reports the published, non-deprecated skills whose review is due.
//
// It is a report, not an action: nothing changes status here. The rules live on
// Metadata.IsStale so the definition of "stale" has one home.
func (s Store) StaleSkills(now time.Time, staleAfter time.Duration) ([]Pack, error) {
	all, err := s.List()
	if err != nil {
		return nil, err
	}
	var out []Pack
	for _, p := range all {
		if p.Metadata.IsStale(now, staleAfter) {
			out = append(out, p)
		}
	}
	return out, nil
}

func readPack(path string) (Pack, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Pack{}, fmt.Errorf("read pack %s: %w", path, err)
	}
	var p Pack
	if err := yaml.Unmarshal(data, &p); err != nil {
		return Pack{}, fmt.Errorf("parse pack %s: %w", path, err)
	}
	return p, nil
}
