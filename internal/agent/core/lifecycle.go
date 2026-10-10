package core

import (
	"context"
	"sort"
	"time"
)

// Memory lifecycle: observation, classification and the archive decision
// (memory governance, part two).
//
// Part one made recall weigh evidence. This part answers the other half of the
// same problem: memory only grows. Every run may add a lesson, every recall may
// re-surface something, and nothing ever leaves — so the topK that recall can
// afford is competed for by a set that keeps getting larger and worse.
//
// The division of labour is the design here, and it is not the obvious one:
//
//	the agent      OBSERVES. It records what was recalled and what was actually
//	               used. It does not decide, and it never edits memory.
//	the control    DECIDES. It aggregates observations across runs and marks
//	plane          memories archived, offline and auditable.
//	a human        AUTHORISES deletion. Archiving is reversible; deleting is not.
//
// Why the agent must not decide, in one sentence: memory is SHARED, its content
// is MODEL-GENERATED, and "I did not use it this time" is not "it is worthless"
// — a cold memory for one run is often the key for another. Letting a run
// prune the global store would give untrusted, partial information the power to
// destroy other runs' knowledge.

// MemoryObservation is one run's view of one recalled memory.
//
// It is append-only and carries no judgement: "recalled but unused" is a fact
// about a run, and only the aggregate over many runs says anything about the
// memory itself.
type MemoryObservation struct {
	// RunID identifies the run that made the observation.
	RunID string `json:"run_id"`
	// EntryID is the memory (or lesson) that was recalled.
	EntryID string `json:"entry_id"`
	// Kind distinguishes memories from lessons, which share this channel but
	// live in different stores and are pruned by different commands.
	Kind ObservationKind `json:"kind"`
	// Used reports whether the entry influenced the run — see UsedSignal for
	// how that is decided and why a conservative answer is required.
	Used bool `json:"used"`
	// UsageKnown says whether Used means anything.
	//
	// This is the distinction the whole lifecycle rests on, and it mirrors the
	// effect model's rule that an undeclared tool is not a read-only tool. A run
	// that merely recalled an entry has learned nothing about whether it was
	// useful: the model may have ignored it, or used it without saying so. Only
	// when the run had a way to TELL us — the citation contract — does a zero
	// use count mean "offered and not taken".
	//
	// Without this field, every recall would look like disuse and the archive
	// would sweep the store clean. "Not observed" and "observed as unused" must
	// not be the same value.
	UsageKnown bool `json:"usage_known"`
	// At is when the observation was made.
	At time.Time `json:"at"`
}

// ObservationKind says which store an observation is about.
type ObservationKind string

const (
	// KindMemory is a long-term memory entry.
	KindMemory ObservationKind = "memory"
	// KindLesson is a distilled lesson from the control plane.
	KindLesson ObservationKind = "lesson"
)

// MemoryType is whether a memory ages.
//
// This is the distinction the earlier design missed, and it is the reason the
// first model was wrong. "The project is maintained in Python" is a property:
// it does not become false with time, and a recency factor would eventually
// archive the most reliable memory in the store precisely because it has been
// true for so long. "Production runs on v2" is an observation: v3 supersedes
// it, and time is exactly what should retire it.
//
// One formula cannot serve both. Applying a decay to a property is a category
// error, not a tuning problem.
type MemoryType string

const (
	// MemoryPersistent is a preference, convention or property. Time does not
	// weaken it.
	MemoryPersistent MemoryType = "persistent"
	// MemoryTemporal is a state or version that a newer observation can
	// supersede.
	MemoryTemporal MemoryType = "temporal"
	// MemoryUnclassified is the state before anything is known. It is treated
	// conservatively: an unclassified memory is never archived on a score, only
	// on the observable "recalled many times, used never" signal.
	MemoryUnclassified MemoryType = ""
)

// NormalizeMemoryType resolves a configured or stored value.
//
// Unrecognised values become unclassified rather than guessing a type: a typo
// in a marker must not turn a property into something that decays.
func NormalizeMemoryType(raw string) MemoryType {
	switch MemoryType(raw) {
	case MemoryPersistent:
		return MemoryPersistent
	case MemoryTemporal:
		return MemoryTemporal
	default:
		return MemoryUnclassified
	}
}

// Decays reports whether time should weaken this memory.
func (t MemoryType) Decays() bool { return t == MemoryTemporal }

// UsageStat is the aggregate of a memory's observations across runs.
type UsageStat struct {
	// Recalled is how many times the entry was returned by a recall.
	Recalled int
	// Known is how many of those recalls came with a usable usage signal.
	//
	// It is separate from Recalled because only these observations may inform a
	// decision. A run that could not tell us whether it used the entry has
	// evidence about the RECALL, not about the entry's value.
	Known int
	// Used is how many of the KNOWN recalls were followed by actual use.
	Used int
	// LastRecalledAt is the most recent recall, zero when never recalled.
	LastRecalledAt time.Time
	// LastUsedAt is the most recent use, zero when never used.
	LastUsedAt time.Time
}

// UseRatio is the fraction of KNOWN recalls that led to use.
//
// The denominator is Known, not Recalled: dividing by all recalls would dilute
// the ratio with observations that say nothing, making a genuinely used entry
// look unused as soon as a few silent runs recalled it.
//
// An entry with no known recalls reports 0, and the caller must treat that as
// "no evidence" rather than "evidence of disuse" (see ShouldArchive).
func (s UsageStat) UseRatio() float64 {
	if s.Known <= 0 {
		return 0
	}
	return float64(s.Used) / float64(s.Known)
}

// AggregateObservations folds per-run observations into per-entry statistics.
//
// It is a pure function over a list, which keeps the decision reproducible: the
// same observations always produce the same statistics, so an archive decision
// can be re-derived and reviewed rather than trusted.
func AggregateObservations(obs []MemoryObservation) map[string]UsageStat {
	out := make(map[string]UsageStat, len(obs))
	for _, o := range obs {
		if o.EntryID == "" {
			continue
		}
		stat := out[o.EntryID]
		stat.Recalled++
		if o.UsageKnown {
			stat.Known++
			if o.Used {
				stat.Used++
				if o.At.After(stat.LastUsedAt) {
					stat.LastUsedAt = o.At
				}
			}
		}
		if o.At.After(stat.LastRecalledAt) {
			stat.LastRecalledAt = o.At
		}
		out[o.EntryID] = stat
	}
	return out
}

// ArchivePolicy decides what leaves the recall pool.
type ArchivePolicy struct {
	// MinKnown is how many recalls with a USABLE usage signal an entry must
	// have before its lack of use counts as STRONG evidence. Below it, a lack
	// of use is still evidence — just weaker; see MinRecalled.
	//
	// This is the field that keeps the store from being swept clean: most runs
	// cannot say whether a recalled memory mattered, and those runs must not
	// count toward "offered and not taken" on their own.
	MinKnown int
	// MinRecalled is how many times an entry must have been OFFERED, with no
	// known use, before it is archived on the weaker evidence.
	//
	// It exists because the strong signal is one this system cannot always
	// produce. Usage is recorded only when a run cites what it relied on, and
	// citing is an invitation rather than a requirement — so an entry nobody
	// ever mentions accumulates no strong signal at all. Requiring MinKnown of
	// an entry that can never produce one made archiving unreachable: every
	// entry that reached the decision had a known signal, every known signal
	// was positive, so the use ratio was always 1.0 and the answer was always
	// "still in use".
	//
	// "Offered many times, never used" is an observable fact rather than a
	// score, which is what the decision is supposed to rest on. It is weaker
	// than "declined many times", so it gets a higher bar of its own, and the
	// decision says which of the two it used.
	MinRecalled int
	// MaxUseRatio is the use ratio at or below which an entry is considered
	// unused. Zero means "never used at all" — the strictest reading, and the
	// default, because a memory used once in fifty recalls is rare but real
	// value, and archiving is not deletion: it can be undone.
	MaxUseRatio float64
	// ProtectPersistent keeps a property from being retired for being rarely
	// needed.
	ProtectPersistent bool
	// Now is the reference time, injected for deterministic tests.
	Now time.Time
}

// DefaultArchivePolicy returns the standard thresholds.
//
// MinRecalled is higher than MinKnown on purpose: being never mentioned is
// weaker evidence than being declined, so it takes more of it.
func DefaultArchivePolicy() ArchivePolicy {
	return ArchivePolicy{
		MinKnown:          5,
		MinRecalled:       10,
		MaxUseRatio:       0,
		ProtectPersistent: true,
	}
}

func (p ArchivePolicy) normalize() ArchivePolicy {
	if p.MinKnown <= 0 {
		p.MinKnown = DefaultArchivePolicy().MinKnown
	}
	if p.MinRecalled <= 0 {
		p.MinRecalled = DefaultArchivePolicy().MinRecalled
	}
	if p.MaxUseRatio < 0 {
		p.MaxUseRatio = 0
	}
	if p.Now.IsZero() {
		p.Now = time.Now()
	}
	return p
}

// ArchiveDecision is the verdict for one entry, with its reasons.
//
// The reasons are part of the contract, not logging: an archive decision removes
// an entry from every future recall, and whoever reviews it must be able to see
// WHY without re-deriving the numbers. "Archived because unused in 12 recalls"
// is reviewable; a bare score is not.
type ArchiveDecision struct {
	EntryID string
	Archive bool
	// Reason is a short human-readable explanation, always populated.
	Reason string
	// Stat is the evidence the decision was made from.
	Stat UsageStat
}

// ShouldArchive decides whether an entry leaves the recall pool.
//
// The decision rests on an OBSERVABLE fact — recalled many times, used never —
// rather than on a weighted score. That matters for three reasons:
//
//   - a score depends on weights being right, and a misconfigured weight would
//     silently archive good memories;
//   - "it was offered twelve times and taken zero" is evidence a reviewer can
//     check, while a temperature is an opinion;
//   - the case that started this design — a property recalled constantly — is
//     safe by construction, because its use count is not zero.
//
// Time is deliberately absent. An old entry that is still used is not stale,
// and an entry that has never been offered is not neglected — it is untested.
func ShouldArchive(entryID string, memType MemoryType, stat UsageStat, policy ArchivePolicy) ArchiveDecision {
	policy = policy.normalize()
	d := ArchiveDecision{EntryID: entryID, Stat: stat}

	// A property is not retired for being rarely needed.
	if policy.ProtectPersistent && memType == MemoryPersistent {
		d.Reason = "persistent memory: not archived on usage"
		return d
	}

	// Strong evidence: enough recalls carried a usage signal for the ratio to
	// mean something. The runs could tell us, and the configured tolerance
	// decides how much rare use is acceptable.
	if stat.Known >= policy.MinKnown {
		if stat.UseRatio() > policy.MaxUseRatio {
			d.Reason = "still in use (used in " + itoa(stat.Used) + " of " + itoa(stat.Known) +
				" recalls with a usage signal, " + itoa(stat.Recalled) + " offered)"
			return d
		}
		d.Archive = true
		d.Reason = "offered " + itoa(stat.Recalled) + " times, used " + itoa(stat.Used) + " in " +
			itoa(stat.Known) + " recalls with a usage signal (type " + string(orUnclassified(memType)) + ")"
		return d
	}

	// Below that, a single known use protects the entry outright.
	//
	// The ratio is not applied here: with one or two known signals it would call
	// an entry used-once "still in use" at any tolerance, which is right, and an
	// entry used-zero "unused" — but the latter also matches any entry nobody
	// ever cited, so the two cannot be told apart by the ratio alone. Protecting
	// on Used > 0 keeps the one distinction that is certain.
	if stat.Used > 0 {
		d.Reason = "still in use (used in " + itoa(stat.Used) + " of " + itoa(stat.Known) +
			" recalls with a usage signal, " + itoa(stat.Recalled) + " offered)"
		return d
	}

	// Weak evidence: offered many times and never cited, with too few usable
	// signals to say more.
	//
	// The bar is higher because the inference is weaker — a run that used the
	// entry without citing it is indistinguishable from one that ignored it.
	// The decision is still worth making, because it is the only one the data
	// supports and archiving is reversible; that is why the reason names which
	// evidence it used rather than reporting a bare count.
	if stat.Recalled >= policy.MinRecalled {
		d.Archive = true
		d.Reason = "offered " + itoa(stat.Recalled) + " times, never cited, and only " +
			itoa(stat.Known) + " of those runs declared usage (weak signal; " +
			"recoverable with `memory recover`)"
		return d
	}

	// Not enough of either. The reason reports both counts, because "not enough"
	// is only actionable if the reader can see how close it is.
	d.Reason = "insufficient evidence (" + itoa(stat.Recalled) + " of " + itoa(policy.MinRecalled) +
		" offers, " + itoa(stat.Known) + " of " + itoa(policy.MinKnown) + " with a usage signal)"
	return d
}

// orUnclassified names the empty type for a message rather than printing "".
func orUnclassified(t MemoryType) MemoryType {
	if t == MemoryUnclassified {
		return "unclassified"
	}
	return t
}

// itoa is a tiny helper so the reason strings do not pull in strconv at every
// call site; the values are small and non-negative.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// Observation context: how the run id reaches the source that records recall.
//
// The agent plane's lesson source implements Recall(ctx, query, topK) — a
// signature that predates observations and has no run id in it. Rather than
// widen an interface that three implementations share, the run id travels in
// the context, the same way the delegation depth does (N5).
//
// It lives in core because both planes need it and neither may import the
// other: the harness stamps it, the serve layer reads it.
type observedRunKey struct{}

// WithObservedRun marks the context as belonging to a run, so recall can record
// which run saw which entry.
func WithObservedRun(ctx context.Context, runID string) context.Context {
	if runID == "" {
		return ctx
	}
	return context.WithValue(ctx, observedRunKey{}, runID)
}

// ObservedRunFrom reports the run id a context carries, or "" when it carries
// none.
//
// An empty result means "do not record": a recall made outside a run (a test, a
// CLI query, a probe) has no run to attribute evidence to, and filing it under
// a fabricated id would corrupt the aggregate that archiving depends on.
func ObservedRunFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if id, ok := ctx.Value(observedRunKey{}).(string); ok {
		return id
	}
	return ""
}

// ObservationSink accepts usage observations from a run.
//
// The harness cannot write them itself: the store is the control plane's index,
// and reaching into it would point the dependency the wrong way (the same
// reason the lesson channel is an interface rather than a package import). The
// serve layer implements this and injects it.
//
// The two methods record the two things a run can honestly report, and they are
// not symmetrical:
//
//   - RecordUsage takes VERIFIED citations. It is a statement: "I relied on
//     these". Only ids the run was actually shown are accepted, so a
//     hallucinated citation cannot steer the archive.
//   - RecordOffers takes the ids the run was SHOWN, and says nothing about use.
//     It is the observation the lifecycle needs in order to see a never-cited
//     entry at all: without it, an entry nobody ever mentions has no rows, and
//     the archive can only ever look at entries that were cited — that is, at
//     entries that were used. The decision then sees only the survivors and
//     concludes nothing.
//
// What the interface still cannot express is the middle case: "these were
// offered AND I used none of them". That is a stronger statement than an offer
// and a different one from a citation, and no contract currently produces it —
// so it is absent here rather than approximated.
type ObservationSink interface {
	// RecordUsage records that a run relied on the given memory ids.
	// Implementations must be safe for concurrent use: N5 delegation can run
	// child loops that share their parent's sink.
	RecordUsage(ctx context.Context, runID string, entryIDs []string) error

	// RecordOffers records that a run was shown the given memory ids, without
	// claiming anything about whether they were used.
	//
	// Same concurrency requirement as RecordUsage.
	RecordOffers(ctx context.Context, runID string, entryIDs []string) error
}

// SortedDecisions orders decisions for stable reporting: archives first (they
// are the ones a reviewer acts on), then by entry id so repeated runs agree.
func SortedDecisions(decisions []ArchiveDecision) []ArchiveDecision {
	out := append([]ArchiveDecision(nil), decisions...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Archive != out[j].Archive {
			return out[i].Archive
		}
		return out[i].EntryID < out[j].EntryID
	})
	return out
}
