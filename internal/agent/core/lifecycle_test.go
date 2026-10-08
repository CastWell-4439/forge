package core

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// obs builds an observation with a usable usage signal.
func obs(runID, entryID string, used bool, at time.Time) MemoryObservation {
	return MemoryObservation{
		RunID: runID, EntryID: entryID, Kind: KindMemory,
		Used: used, UsageKnown: true, At: at,
	}
}

// silent builds an observation with NO usage signal: the run recalled the entry
// and could not say whether it mattered.
func silent(runID, entryID string, at time.Time) MemoryObservation {
	return MemoryObservation{
		RunID: runID, EntryID: entryID, Kind: KindMemory,
		Used: false, UsageKnown: false, At: at,
	}
}

// Usage with a signal and usage without one are counted separately.
//
// This is the distinction the whole lifecycle rests on: a run that cannot say
// whether it used an entry has evidence about the RECALL, not about the entry.
// Collapsing the two would make every silent run look like a rejection.
func TestAggregateSeparatesKnownFromSilent(t *testing.T) {
	now := time.Now()
	stats := AggregateObservations([]MemoryObservation{
		obs("r1", "e1", true, now),
		silent("r2", "e1", now),
		silent("r3", "e1", now),
		obs("r4", "e1", false, now),
	})

	stat := stats["e1"]
	assert.Equal(t, 4, stat.Recalled, "every recall is counted as a recall")
	assert.Equal(t, 2, stat.Known, "only two carried a usage signal")
	assert.Equal(t, 1, stat.Used)
	assert.InDelta(t, 0.5, stat.UseRatio(), 1e-9, "the ratio divides by KNOWN, not by all recalls")
}

// A use ratio computed over all recalls would dilute as silent runs pile up,
// making a genuinely used entry look unused. Dividing by known signals avoids
// that.
func TestUseRatioIgnoresSilentRecalls(t *testing.T) {
	now := time.Now()
	// Ten silent recalls, one known use.
	var list []MemoryObservation
	for i := 0; i < 10; i++ {
		list = append(list, silent("s"+itoa(i), "e1", now))
	}
	list = append(list, obs("known", "e1", true, now))

	stat := AggregateObservations(list)["e1"]
	assert.Equal(t, 11, stat.Recalled)
	assert.Equal(t, 1, stat.Known)
	assert.InDelta(t, 1.0, stat.UseRatio(), 1e-9,
		"the one known recall was a use; the silent ones say nothing")
}

// An entry with no known recalls has no ratio, and the decision must read that
// as "no evidence" rather than as disuse.
func TestNoKnownSignalIsNotEvidence(t *testing.T) {
	now := time.Now()
	stat := AggregateObservations([]MemoryObservation{silent("r1", "e1", now)})["e1"]

	d := ShouldArchive("e1", MemoryUnclassified, stat, DefaultArchivePolicy())
	assert.False(t, d.Archive, "silence is not a rejection")
	assert.Contains(t, d.Reason, "insufficient")
}

// --- aggregation ---

func TestAggregateIgnoresEmptyEntryID(t *testing.T) {
	stats := AggregateObservations([]MemoryObservation{
		obs("r1", "", true, time.Now()),
		obs("r2", "e1", true, time.Now()),
	})
	assert.Len(t, stats, 1)
	assert.Contains(t, stats, "e1")
}

func TestAggregateTracksLastTimes(t *testing.T) {
	early := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	late := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	stat := AggregateObservations([]MemoryObservation{
		obs("r1", "e1", true, early),
		obs("r2", "e1", true, late),
	})["e1"]

	assert.Equal(t, late, stat.LastUsedAt)
	assert.Equal(t, late, stat.LastRecalledAt)
}

// --- the archive decision ---

// Recalled many times with a usage signal and never used: the one case that
// archives. It is an observable fact, not a weighted score.
func TestArchiveUnusedEntry(t *testing.T) {
	now := time.Now()
	var list []MemoryObservation
	for i := 0; i < 6; i++ {
		list = append(list, obs("r"+itoa(i), "e1", false, now))
	}
	stat := AggregateObservations(list)["e1"]

	d := ShouldArchive("e1", MemoryUnclassified, stat, DefaultArchivePolicy())
	assert.True(t, d.Archive)
	assert.Contains(t, d.Reason, "6", "the reason carries the evidence")
	assert.Contains(t, d.Reason, "used 0")
}

// The case that motivated the design: a property recalled constantly over a
// long time. Its USE count is not zero, so no amount of age archives it.
func TestPersistentPropertySurvivesAge(t *testing.T) {
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	var list []MemoryObservation
	for i := 0; i < 30; i++ {
		// Every recall is a use: the property is relied on every time.
		list = append(list, obs("r"+itoa(i), "python-project", true, old))
	}
	stat := AggregateObservations(list)["python-project"]

	d := ShouldArchive("python-project", MemoryPersistent, stat, DefaultArchivePolicy())
	assert.False(t, d.Archive, "a property in constant use is never archived, however old")

	// Even unclassified, a used entry survives: the decision never looks at age.
	d = ShouldArchive("python-project", MemoryUnclassified, stat, DefaultArchivePolicy())
	assert.False(t, d.Archive)
	assert.Contains(t, d.Reason, "still in use")
}

// A persistent memory is protected even when unused: a convention that is
// rarely needed is still a convention.
func TestPersistentProtectedWhenUnused(t *testing.T) {
	now := time.Now()
	var list []MemoryObservation
	for i := 0; i < 20; i++ {
		list = append(list, obs("r"+itoa(i), "e1", false, now))
	}
	stat := AggregateObservations(list)["e1"]

	d := ShouldArchive("e1", MemoryPersistent, stat, DefaultArchivePolicy())
	assert.False(t, d.Archive, "properties are not retired for being rarely needed")
	assert.Contains(t, d.Reason, "persistent")
}

// Below the threshold there is no evidence: an entry offered once and not used
// has not been tested, it has been sampled.
func TestBelowThresholdIsNotEvidence(t *testing.T) {
	now := time.Now()
	stat := AggregateObservations([]MemoryObservation{
		obs("r1", "e1", false, now),
		obs("r2", "e1", false, now),
	})["e1"]

	d := ShouldArchive("e1", MemoryUnclassified, stat, DefaultArchivePolicy())
	assert.False(t, d.Archive)
	assert.Contains(t, d.Reason, "insufficient")
}

// One use anywhere keeps an entry in the pool: a memory used once in many
// recalls is rare value, and archiving is reversible but unnecessary here.
func TestOneUseKeepsEntry(t *testing.T) {
	now := time.Now()
	var list []MemoryObservation
	for i := 0; i < 20; i++ {
		list = append(list, obs("r"+itoa(i), "e1", false, now))
	}
	list = append(list, obs("used", "e1", true, now))
	stat := AggregateObservations(list)["e1"]

	d := ShouldArchive("e1", MemoryUnclassified, stat, DefaultArchivePolicy())
	assert.False(t, d.Archive, "rare value is still value")
}

// A policy with a non-zero ratio threshold allows "mostly unused" archiving.
func TestMaxUseRatioIsConfigurable(t *testing.T) {
	now := time.Now()
	var list []MemoryObservation
	for i := 0; i < 10; i++ {
		list = append(list, obs("r"+itoa(i), "e1", i == 0, now))
	}
	stat := AggregateObservations(list)["e1"] // 1/10 used

	strict := DefaultArchivePolicy()
	assert.False(t, ShouldArchive("e1", MemoryUnclassified, stat, strict).Archive,
		"the strict default keeps anything used at all")

	lenient := DefaultArchivePolicy()
	lenient.MaxUseRatio = 0.2
	assert.True(t, ShouldArchive("e1", MemoryUnclassified, stat, lenient).Archive,
		"a configured tolerance archives mostly-unused entries")
}

// The threshold is configurable, so a deployment can demand more evidence.
func TestMinKnownIsConfigurable(t *testing.T) {
	now := time.Now()
	var list []MemoryObservation
	for i := 0; i < 3; i++ {
		list = append(list, obs("r"+itoa(i), "e1", false, now))
	}
	stat := AggregateObservations(list)["e1"]

	assert.False(t, ShouldArchive("e1", MemoryUnclassified, stat, DefaultArchivePolicy()).Archive)

	eager := ArchivePolicy{MinKnown: 2}
	assert.True(t, ShouldArchive("e1", MemoryUnclassified, stat, eager).Archive)
}

// An unset policy normalizes to the defaults rather than to "archive nothing"
// or "archive everything".
func TestPolicyNormalization(t *testing.T) {
	now := time.Now()
	var list []MemoryObservation
	for i := 0; i < 6; i++ {
		list = append(list, obs("r"+itoa(i), "e1", false, now))
	}
	stat := AggregateObservations(list)["e1"]

	// Zero value: MinKnown normalizes to the default (5), so 6 known unused
	// recalls archive.
	d := ShouldArchive("e1", MemoryUnclassified, stat, ArchivePolicy{})
	assert.True(t, d.Archive)

	d2 := ShouldArchive("e1", MemoryUnclassified, stat, ArchivePolicy{MinKnown: -1})
	assert.True(t, d2.Archive, "a negative threshold falls back to the default")

	d3 := ShouldArchive("e1", MemoryUnclassified, stat, ArchivePolicy{MaxUseRatio: -1})
	assert.True(t, d3.Archive, "a negative ratio is clamped to zero")
}

// --- memory types ---

func TestMemoryTypeNormalization(t *testing.T) {
	assert.Equal(t, MemoryPersistent, NormalizeMemoryType("persistent"))
	assert.Equal(t, MemoryTemporal, NormalizeMemoryType("temporal"))
	assert.Equal(t, MemoryUnclassified, NormalizeMemoryType(""))
	// A misspelling must not turn a property into something that decays.
	assert.Equal(t, MemoryUnclassified, NormalizeMemoryType("persist-ant"))

	assert.False(t, MemoryPersistent.Decays())
	assert.True(t, MemoryTemporal.Decays())
	assert.False(t, MemoryUnclassified.Decays())
}

// --- reporting ---

// Archives come first in the report: they are what a reviewer acts on.
func TestSortedDecisionsArchiveFirst(t *testing.T) {
	decisions := []ArchiveDecision{
		{EntryID: "b", Archive: false},
		{EntryID: "a", Archive: true},
		{EntryID: "c", Archive: true},
	}
	sorted := SortedDecisions(decisions)
	require.Len(t, sorted, 3)
	assert.True(t, sorted[0].Archive)
	assert.True(t, sorted[1].Archive)
	assert.False(t, sorted[2].Archive)

	// Ties break by id so repeated runs report the same order.
	assert.Equal(t, "a", sorted[0].EntryID)
	assert.Equal(t, "c", sorted[1].EntryID)
}

// SortedDecisions copies: the caller's slice must not be reordered underneath
// it, or a report would mutate the input it was given.
func TestSortedDecisionsDoesNotMutateInput(t *testing.T) {
	input := []ArchiveDecision{
		{EntryID: "z", Archive: false},
		{EntryID: "a", Archive: true},
	}
	_ = SortedDecisions(input)
	assert.Equal(t, "z", input[0].EntryID, "the input order is preserved")
}

// --- run attribution ---

// Recall made outside a run records nothing: there is no run to attribute the
// evidence to, and a fabricated id would corrupt the aggregate.
func TestObservedRunContext(t *testing.T) {
	assert.Empty(t, ObservedRunFrom(context.Background()))

	ctx := WithObservedRun(context.Background(), "run-1")
	assert.Equal(t, "run-1", ObservedRunFrom(ctx))

	// An empty id is not stamped, so an unlabelled recall stays unlabelled.
	assert.Empty(t, ObservedRunFrom(WithObservedRun(context.Background(), "")))

	// A context with no value reports empty rather than panicking.
	assert.Empty(t, ObservedRunFrom(context.TODO()))
}

// --- itoa helper ---

// The reason strings are built with a tiny helper; it must handle the values
// that appear in practice, including zero.
func TestItoa(t *testing.T) {
	assert.Equal(t, "0", itoa(0))
	assert.Equal(t, "1", itoa(1))
	assert.Equal(t, "9", itoa(9))
	assert.Equal(t, "10", itoa(10))
	assert.Equal(t, "123", itoa(123))
	assert.Equal(t, "-5", itoa(-5))
	assert.Equal(t, "1000000", itoa(1000000))
}
