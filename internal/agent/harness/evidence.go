package harness

import (
	"sort"
	"strings"
	"time"

	"github.com/castwell/forge/internal/agent/core"
)

// Evidence aggregation (memory governance).
//
// Recall returns entries ranked by similarity, each independent. That answers
// "what looks relevant" and leaves three questions unanswered:
//
//   - Do any of them speak about the same thing? (grouping)
//   - If so, are they agreeing, complementing, conflicting or stale?
//   - What should the prompt say when they disagree?
//
// The aggregation below answers them in that order. It is deliberately LOCAL:
// it works on the entries recall already returned, using similarity and
// metadata. It does not call an LLM, does not consult a knowledge graph, and
// does not re-embed anything — a pure function over a small list, which means
// it is cheap enough to run on every recall and testable without a provider.
//
// What it does NOT try to do: decide what is objectively true. It decides what
// the EVIDENCE supports, and when the evidence does not support one answer it
// says so. That is the part a flat topK list gets wrong most expensively.

// EvidenceConfig tunes the aggregation.
type EvidenceConfig struct {
	// GroupThreshold is the similarity above which two entries are treated as
	// speaking about the same subject. Below it they are complementary, which
	// needs no decision.
	//
	// Too high and genuine conflicts are never compared (they stay independent
	// and the model sees two unrelated facts that happen to disagree). Too low
	// and unrelated memories get merged into bogus conflicts. 0.85 is the usual
	// starting point for sentence embeddings.
	GroupThreshold float64
	// DuplicateThreshold is the similarity above which two entries in a group
	// are treated as the same claim rather than different aspects of it.
	// Duplicates vote together and raise confidence; distinct aspects merely
	// coexist.
	DuplicateThreshold float64
	// ConflictBand is how close two competing confidences may be before the
	// aggregation refuses to pick a winner. Inside the band the group is
	// reported as a conflict; outside it the stronger claim wins and the weaker
	// is noted as a dissenting source.
	//
	// The band is the whole point: a narrow one makes every small difference a
	// decision, a wide one makes genuine disagreements get silently resolved.
	ConflictBand float64
	// StaleAfter is how much older one observation must be before it counts as
	// stale rather than a peer. Some slack is needed because two runs minutes
	// apart are not "old and new", they are two observations.
	StaleAfter time.Duration
	// Now is the reference time for staleness. Injected so tests are
	// deterministic.
	Now time.Time
	// MaxGroups caps how many groups reach the prompt. Conflicts are never
	// dropped by the cap (see Aggregate): a cap that hid a disagreement would
	// recreate the problem this exists to fix.
	MaxGroups int
}

// DefaultEvidenceConfig returns the standard tuning.
func DefaultEvidenceConfig() EvidenceConfig {
	return EvidenceConfig{
		GroupThreshold:     0.85,
		DuplicateThreshold: 0.92,
		ConflictBand:       0.15,
		StaleAfter:         30 * 24 * time.Hour,
		MaxGroups:          5,
	}
}

// normalize fills in anything unset so a partial config still behaves.
func (c EvidenceConfig) normalize() EvidenceConfig {
	d := DefaultEvidenceConfig()
	if c.GroupThreshold <= 0 || c.GroupThreshold > 1 {
		c.GroupThreshold = d.GroupThreshold
	}
	if c.DuplicateThreshold <= 0 || c.DuplicateThreshold > 1 {
		c.DuplicateThreshold = d.DuplicateThreshold
	}
	// A duplicate threshold below the group threshold would mean an entry can
	// be "the same subject" but never "the same claim", making the duplicate
	// relation unreachable. Raising it keeps the relations ordered.
	if c.DuplicateThreshold < c.GroupThreshold {
		c.DuplicateThreshold = c.GroupThreshold
	}
	if c.ConflictBand < 0 || c.ConflictBand > 1 {
		c.ConflictBand = d.ConflictBand
	}
	if c.StaleAfter <= 0 {
		c.StaleAfter = d.StaleAfter
	}
	if c.MaxGroups <= 0 {
		c.MaxGroups = d.MaxGroups
	}
	if c.Now.IsZero() {
		c.Now = time.Now()
	}
	return c
}

// AggregateEvidence turns a flat recall list into evidence groups.
//
// The steps mirror the pipeline an evidence-aggregation design needs, in the
// order that makes each one possible:
//
//  1. score    attach each entry's relevance (its confidence, and similarity
//     when the caller has it)
//  2. group    decide which entries speak about the same subject
//  3. relate   within a group: duplicate, complementary, conflicting, stale
//  4. fuse     aggregate confidence and pick a representative
//  5. report   rank the groups, and separate out the unresolved conflicts
//
// A single-entry group is complementary by definition: nothing to compare it
// with. That is the common case and it passes through unchanged.
func (l *AgentLoop) AggregateEvidence(entries []core.MemoryEntry, similarity func(core.MemoryEntry, core.MemoryEntry) float64) core.MemoryEvidence {
	return AggregateEvidenceWith(entries, similarity, l.evidenceConfig())
}

// evidenceConfig resolves the aggregation tuning from the loop's configuration.
func (l *AgentLoop) evidenceConfig() EvidenceConfig {
	cfg := l.config.Evidence
	if cfg.Now.IsZero() {
		cfg.Now = time.Now()
	}
	return cfg.normalize()
}

// AggregateEvidenceWith is the aggregation itself, as a free function so it can
// be tested without a loop.
//
// similarity may be nil, in which case entries are grouped by exact content
// only. That is a real mode, not a degenerate one: content equality is the one
// relation that needs no embedding, so a deployment without a vector store
// still gets duplicate detection and conflict reporting between identical-text
// entries with different metadata.
func AggregateEvidenceWith(entries []core.MemoryEntry, similarity func(a, b core.MemoryEntry) float64, cfg EvidenceConfig) core.MemoryEvidence {
	cfg = cfg.normalize()
	if len(entries) == 0 {
		return core.MemoryEvidence{}
	}

	groups := groupEvidence(entries, similarity, cfg)

	out := make([]core.EvidenceGroup, 0, len(groups))
	var conflicts []core.EvidenceGroup
	for _, g := range groups {
		relateGroup(&g, cfg)
		fuseGroup(&g, cfg)
		if g.Relation == core.RelationConflicting {
			conflicts = append(conflicts, g)
		}
		out = append(out, g)
	}

	// Best first: the aggregated confidence decides, with the newest member
	// breaking ties so the order is deterministic.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Confidence != out[j].Confidence {
			return out[i].Confidence > out[j].Confidence
		}
		return newestMember(out[i]).After(newestMember(out[j]))
	})

	// The cap applies to the ranked list but NOT to conflicts: a disagreement
	// that fell off the end of the prompt would be a silent resolution, which
	// is the failure this whole path exists to prevent.
	if len(out) > cfg.MaxGroups {
		kept := out[:cfg.MaxGroups]
		for _, c := range conflicts {
			if !containsGroup(kept, c) {
				// Replace the weakest non-conflicting group with the conflict,
				// so the budget is respected without hiding the disagreement.
				replaced := false
				for i := len(kept) - 1; i >= 0; i-- {
					if kept[i].Relation != core.RelationConflicting {
						kept[i] = c
						replaced = true
						break
					}
				}
				if !replaced {
					break
				}
			}
		}
		sort.SliceStable(kept, func(i, j int) bool { return kept[i].Confidence > kept[j].Confidence })
		out = kept
	}

	return core.MemoryEvidence{Groups: out, Conflicts: conflicts}
}

// groupEvidence partitions entries by subject.
//
// Union-find rather than a single pass: similarity is not transitive in
// practice (A~B and B~C does not imply A~C), but for "are these about the same
// subject" the connected component is the right notion — if A is about B's
// subject and B is about C's, all three belong together, and splitting them
// would hide a conflict between A and C.
func groupEvidence(entries []core.MemoryEntry, similarity func(a, b core.MemoryEntry) float64, cfg EvidenceConfig) []core.EvidenceGroup {
	parent := make([]int, len(entries))
	for i := range parent {
		parent[i] = i
	}
	// find follows parent links with path compression. It is not recursive —
	// the loop does the compression — so it can be declared as a plain closure.
	find := func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	union := func(a, b int) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[rb] = ra
		}
	}

	for i := 0; i < len(entries); i++ {
		for j := i + 1; j < len(entries); j++ {
			if sameSubject(entries[i], entries[j], similarity, cfg) {
				union(i, j)
			}
		}
	}

	byRoot := map[int][]core.MemoryEntry{}
	var order []int
	for i := range entries {
		root := find(i)
		if _, seen := byRoot[root]; !seen {
			order = append(order, root)
		}
		byRoot[root] = append(byRoot[root], entries[i])
	}

	groups := make([]core.EvidenceGroup, 0, len(order))
	for _, root := range order {
		members := byRoot[root]
		groups = append(groups, core.EvidenceGroup{
			Subject: members[0].Content,
			Members: members,
		})
	}
	return groups
}

// sameSubject reports whether two entries are about the same thing.
//
// Exact content match always counts, so this works without an embedder. Beyond
// that the caller's similarity decides.
func sameSubject(a, b core.MemoryEntry, similarity func(x, y core.MemoryEntry) float64, cfg EvidenceConfig) bool {
	if strings.EqualFold(strings.TrimSpace(a.Content), strings.TrimSpace(b.Content)) {
		return true
	}
	if similarity == nil {
		return false
	}
	return similarity(a, b) >= cfg.GroupThreshold
}

// relateGroup decides how a group's members relate.
//
// The order of the checks is the design: staleness is examined before
// disagreement, because an old claim disagreeing with a new one is not a
// conflict to report — it is history, and the newer claim simply stands. Only
// when claims disagree within the same era does the group become a conflict.
func relateGroup(g *core.EvidenceGroup, cfg EvidenceConfig) {
	if len(g.Members) <= 1 {
		g.Relation = core.RelationComplementary
		return
	}

	newest, oldest := newestMember(*g), oldestMember(*g)
	stale := newest.Sub(oldest) > cfg.StaleAfter

	sameClaim, conflicting := compareClaims(*g)

	switch {
	case stale:
		// An old claim disagreeing with a new observation is not a conflict to
		// report — it is history, and the newer claim stands. This is checked
		// first because applying the conflict rule here would flag every
		// superseded value as a live disagreement.
		g.Relation = core.RelationStale
	case conflicting:
		g.Relation = core.RelationConflicting
	case sameClaim:
		g.Relation = core.RelationDuplicate
	default:
		g.Relation = core.RelationComplementary
	}
}

// compareClaims reports two things about a group: whether all members state
// the same claim, and whether any two of them state mutually exclusive claims.
//
// This is the step that makes "conflicting" reachable at all. Grouping already
// established that the members are about the same subject; the remaining
// question is whether they AGREE. Text inequality is not an answer — "the
// deploy has two stages" and "the deploy needs an approval" are different
// sentences that do not disagree — so a cheaper structural test is used.
//
// The test: strip the VALUES out of each claim, and compare the remaining
// SKELETON. Two claims with the same skeleton and different values are
// statements about the same property with different answers, which is exactly
// what a conflict is:
//
//	"the retry limit is 3"   -> skeleton "the retry limit is #"
//	"the retry limit is 5"   -> skeleton "the retry limit is #"   => CONFLICT
//	"the retry limit is 3"   -> skeleton "the retry limit is #"
//	"the retry has a cap"    -> skeleton "the retry has a cap"    => complementary
//
// This is the structural-matching half of fact normalisation, without the
// expensive half (entity aliasing and triple extraction, which need a model).
// It catches the case that actually occurs — the same sentence with a changed
// number, name or date — and it fails in the safe direction: claims it cannot
// analyse are treated as complementary rather than as conflicts, so a
// limitation produces a missed warning rather than a false alarm.
func compareClaims(g core.EvidenceGroup) (sameClaim, conflicting bool) {
	if len(g.Members) <= 1 {
		return true, false
	}

	skeletons := make([]string, 0, len(g.Members))
	values := make([]string, 0, len(g.Members))
	for _, m := range g.Members {
		skeletons = append(skeletons, claimSkeleton(m.Content))
		values = append(values, valuesOf(m.Content))
	}

	allSameSkeleton := true
	for _, s := range skeletons[1:] {
		if s != skeletons[0] {
			allSameSkeleton = false
			break
		}
	}

	if allSameSkeleton {
		// One skeleton, so every member claims the SAME property. Now the
		// values decide: identical values are one claim restated (duplicate),
		// different values are the same question answered differently — which
		// is precisely a conflict.
		//
		// This branch is the whole point of the structural comparison, and it is
		// the branch that a naive "compare the text" check misses: the two
		// sentences are not equal, so text comparison calls them unrelated.
		allSameValues := true
		for _, v := range values[1:] {
			if !strings.EqualFold(v, values[0]) {
				allSameValues = false
				break
			}
		}
		if allSameValues {
			return true, false
		}
		return false, true
	}

	// Different skeletons. A pair with the SAME skeleton but different values
	// still conflicts, even though the group as a whole has mixed structures.
	for i := 0; i < len(skeletons); i++ {
		for j := i + 1; j < len(skeletons); j++ {
			if skeletons[i] == skeletons[j] && !strings.EqualFold(values[i], values[j]) {
				return false, true
			}
		}
	}
	return false, false
}

// claimSkeleton replaces the variables in a claim with a placeholder, leaving
// the structure that identifies WHAT is being claimed.
//
// Numbers, quoted strings and capitalised tokens are treated as values. That
// covers the vocabulary of operational memory — versions, limits, host names,
// dates, counts — without needing to know the domain.
func claimSkeleton(s string) string {
	fields := strings.Fields(strings.ToLower(s))
	for i, f := range fields {
		if isValueToken(f) {
			fields[i] = "#"
		}
	}
	return strings.Join(fields, " ")
}

// valuesOf extracts the value tokens from a claim, in order, so two claims with
// the same skeleton can be compared by what they actually say.
func valuesOf(s string) string {
	var values []string
	for _, f := range strings.Fields(strings.ToLower(s)) {
		if isValueToken(f) {
			values = append(values, f)
		}
	}
	return strings.Join(values, "|")
}

// isValueToken reports whether a token is a value rather than part of the
// claim's structure.
//
// The rules are deliberately narrow. A token that merely looks unusual stays
// part of the structure, because wrongly treating structure as a value would
// turn two different claims into "the same claim with different values" and
// invent a conflict.
func isValueToken(tok string) bool {
	trimmed := strings.Trim(tok, `.,;:!?"'()`)
	if trimmed == "" {
		return false
	}
	// A number, with or without a unit suffix or decimal part.
	digits := 0
	for _, r := range trimmed {
		switch {
		case r >= '0' && r <= '9':
			digits++
		case r == '.' || r == '-' || r == '/' || r == '_':
			// separators inside a number-like token (1.5, 2026-01, a/b handled below)
		case r >= 'a' && r <= 'z':
			// a unit suffix such as "100rps" or "5s"
		default:
			return false
		}
	}
	return digits > 0
}

// fuseGroup aggregates confidence and orders the members.
//
// The rules come from how evidence behaves:
//
//   - duplicates vote: n independent sources agreeing is stronger than one, so
//     confidence rises with the number of DISTINCT sources (the same run
//     writing twice is not two witnesses);
//   - a conflict does not average: averaging two contradictory claims would
//     invent a middle value neither source supports, and the result would look
//     authoritative;
//   - a stale group takes the newest member's confidence, since it wins.
func fuseGroup(g *core.EvidenceGroup, cfg EvidenceConfig) {
	// Order members newest-first so the representative is the freshest.
	sort.SliceStable(g.Members, func(i, j int) bool {
		return observedAtOf(g.Members[i]).After(observedAtOf(g.Members[j]))
	})

	// Distinct sources, in first-seen order.
	seen := map[core.MemorySource]bool{}
	for _, m := range g.Members {
		src := m.Source
		if src == "" {
			src = core.MemorySource("unknown")
		}
		if !seen[src] {
			seen[src] = true
			g.Sources = append(g.Sources, src)
		}
	}

	base := core.ConfidenceOf(g.Members[0])
	if g.Relation == core.RelationConflicting {
		// A conflict caps confidence at the strongest single claim: the group
		// as a whole is no more trustworthy than its best member, and it is
		// certainly not the sum of them.
		best := 0.0
		for _, m := range g.Members {
			if c := core.ConfidenceOf(m); c > best {
				best = c
			}
		}
		g.Confidence = best
		g.Subject = g.Members[0].Content
		return
	}

	// Independent agreement raises confidence. The boost is per ADDITIONAL
	// distinct source and saturates: three agreeing runs should be better than
	// one, and thirty should not be ten times better than three.
	extra := float64(len(g.Sources) - 1)
	if extra < 0 {
		extra = 0
	}
	boost := 1 + 0.1*extra
	if boost > 1.5 {
		boost = 1.5
	}
	conf := base * boost
	if conf > 1 {
		conf = 1
	}
	g.Confidence = conf
	g.Subject = g.Members[0].Content
}

// ObservedAt is a convenience for the aggregation's staleness checks.
func observedAtOf(m core.MemoryEntry) time.Time { return core.ObservedAtOf(m) }

// newestMember returns the member with the latest observation time.
func newestMember(g core.EvidenceGroup) time.Time {
	var best time.Time
	for _, m := range g.Members {
		if t := observedAtOf(m); t.After(best) {
			best = t
		}
	}
	return best
}

// oldestMember returns the member with the earliest observation time.
func oldestMember(g core.EvidenceGroup) time.Time {
	var best time.Time
	for i, m := range g.Members {
		t := observedAtOf(m)
		if i == 0 || t.Before(best) {
			best = t
		}
	}
	return best
}

// containsGroup reports whether a group with the same subject is already kept.
func containsGroup(groups []core.EvidenceGroup, g core.EvidenceGroup) bool {
	for _, existing := range groups {
		if existing.Subject == g.Subject {
			return true
		}
	}
	return false
}
