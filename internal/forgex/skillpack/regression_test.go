package skillpack

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// casePack builds a published pack with bound cases.
func casePack(id string, cases ...string) Pack {
	p := publishedPack(id)
	p.Spec.Eval = EvalBinding{Suite: "suite-a", Cases: cases}
	return p
}

// seedStore writes packs into a fresh store and returns it.
func seedStore(t *testing.T, packs ...Pack) Store {
	t.Helper()
	store := NewStore(t.TempDir())
	for _, p := range packs {
		_, err := store.Save(p, false)
		require.NoError(t, err)
	}
	return store
}

// --- recording ---

// A passing check is recorded with its time, so "verified" stops meaning
// "somebody once ran it".
func TestRecordVerificationPassed(t *testing.T) {
	store := seedStore(t, casePack("sk-1", "c1"))
	at := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	pack, err := store.RecordVerification("sk-1", nil, at)
	require.NoError(t, err)
	assert.Equal(t, VerifyPassed, pack.Metadata.LastVerifyStatus)
	assert.True(t, pack.Metadata.LastVerifiedAt.Equal(at))
	assert.Empty(t, pack.Metadata.LastVerifyFailedCases)
	assert.False(t, pack.Metadata.NeedsAttention())

	// And it survives a round trip.
	loaded, err := store.Load("sk-1")
	require.NoError(t, err)
	assert.Equal(t, VerifyPassed, loaded.Metadata.LastVerifyStatus)
	assert.True(t, loaded.Metadata.LastVerifiedAt.Equal(at))
}

// A failure is recorded WITH the case names: "it broke" needs "here is where",
// or the next reader re-runs everything to find out which part moved.
func TestRecordVerificationFailed(t *testing.T) {
	store := seedStore(t, casePack("sk-1", "c1", "c2", "c3"))

	pack, err := store.RecordVerification("sk-1", []string{"c2"}, time.Now().UTC())
	require.NoError(t, err)
	assert.Equal(t, VerifyFailed, pack.Metadata.LastVerifyStatus)
	assert.Equal(t, []string{"c2"}, pack.Metadata.LastVerifyFailedCases)
	assert.True(t, pack.Metadata.NeedsAttention())

	loaded, err := store.Load("sk-1")
	require.NoError(t, err)
	assert.Equal(t, []string{"c2"}, loaded.Metadata.LastVerifyFailedCases, "the names persist")
}

// Recovering clears the previous failure list: case names left next to a
// "passed" status would describe a failure that no longer exists.
func TestRecordVerificationClearsOldFailures(t *testing.T) {
	store := seedStore(t, casePack("sk-1", "c1"))
	_, err := store.RecordVerification("sk-1", []string{"c1"}, time.Now().UTC())
	require.NoError(t, err)

	pack, err := store.RecordVerification("sk-1", nil, time.Now().UTC())
	require.NoError(t, err)
	assert.Equal(t, VerifyPassed, pack.Metadata.LastVerifyStatus)
	assert.Empty(t, pack.Metadata.LastVerifyFailedCases, "the stale failure names are gone")
}

// Recording does not change the skill's status: a regression may mean the skill
// is wrong, a case went stale, or the environment moved — three repairs this
// function cannot choose between.
func TestRecordingDoesNotChangeStatus(t *testing.T) {
	store := seedStore(t, casePack("sk-1", "c1"))
	pack, err := store.RecordVerification("sk-1", []string{"c1"}, time.Now().UTC())
	require.NoError(t, err)
	assert.Equal(t, StatusPublished, pack.Metadata.Status, "a report is not a decision")
	assert.False(t, pack.Metadata.IsDeprecated())
}

// The recorded failure list is copied, not aliased, so a caller reusing its
// slice cannot change what the skill says.
func TestRecordedFailuresAreCopied(t *testing.T) {
	store := seedStore(t, casePack("sk-1", "c1"))
	failed := []string{"c1"}
	pack, err := store.RecordVerification("sk-1", failed, time.Now().UTC())
	require.NoError(t, err)

	failed[0] = "mutated"
	assert.Equal(t, []string{"c1"}, pack.Metadata.LastVerifyFailedCases)
}

// --- selection ---

// Previously failing skills come first: a batch run answers "did anything
// regress", and the most likely answer is the one that already did.
func TestCheckPutsPreviouslyFailingFirst(t *testing.T) {
	// Both need a bound case, or they are skipped as uncheckable and the runner
	// never runs — which is what made the first version of this test observe an
	// empty call list rather than an ordering.
	store := seedStore(t, casePack("aaa", "c1"), casePack("zzz", "c1"))
	_, err := store.RecordVerification("zzz", []string{"c1"}, time.Now().UTC())
	require.NoError(t, err)

	var order []string
	results, err := CheckSkills(store, func(p Pack) ([]string, error) {
		order = append(order, p.Metadata.ID)
		return nil, nil
	}, CheckConfig{})
	require.NoError(t, err)

	assert.Equal(t, []string{"zzz", "aaa"}, order, "the previously failing one is checked first")
	assert.Len(t, results, 2)
}

// A draft has never been in use, so there is no regression to detect.
func TestCheckSkipsDrafts(t *testing.T) {
	store := NewStore(t.TempDir())
	_, err := store.Save(casePack("draft-1", "c1"), true)
	require.NoError(t, err)
	_, err = store.Save(casePack("pub-1", "c1"), false)
	require.NoError(t, err)

	var ran []string
	_, err = CheckSkills(store, func(p Pack) ([]string, error) {
		ran = append(ran, p.Metadata.ID)
		return nil, nil
	}, CheckConfig{})
	require.NoError(t, err)
	assert.Equal(t, []string{"pub-1"}, ran)
}

// A deprecated skill is out of use by default: checking it would ask someone to
// fix something nobody runs.
func TestCheckSkipsDeprecatedUnlessAsked(t *testing.T) {
	store := seedStore(t, casePack("live", "c1"), casePack("retired", "c1"))
	_, err := store.Deprecate("retired", "no longer applies", time.Now().UTC())
	require.NoError(t, err)

	var ran []string
	runner := func(p Pack) ([]string, error) { ran = append(ran, p.Metadata.ID); return nil, nil }

	_, err = CheckSkills(store, runner, CheckConfig{})
	require.NoError(t, err)
	assert.Equal(t, []string{"live"}, ran)

	ran = nil
	_, err = CheckSkills(store, runner, CheckConfig{IncludeDeprecated: true})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"live", "retired"}, ran)
}

// A skill with no bound cases is skipped and says why: an empty check is not
// evidence that the skill still works.
func TestCheckSkipsWithoutCases(t *testing.T) {
	store := seedStore(t, Pack{
		APIVersion: APIVersion, Kind: Kind,
		Metadata: Metadata{ID: "no-cases", Status: StatusPublished, GeneratedAt: time.Now()},
		Spec:     Spec{Trigger: Trigger{Description: "d"}},
		Readme:   "written",
	})

	results, err := CheckSkills(store, func(Pack) ([]string, error) {
		t.Fatal("the runner must not be called for a skill with no cases")
		return nil, nil
	}, CheckConfig{})
	require.NoError(t, err)

	require.Len(t, results, 1)
	assert.Contains(t, results[0].Skipped, "no bound cases")
	assert.False(t, results[0].Passed())
}

// The cap bounds the work, because each case is a full scenario execution.
func TestCheckRespectsMax(t *testing.T) {
	store := seedStore(t, casePack("a", "c1"), casePack("b", "c1"), casePack("c", "c1"))

	var ran []string
	_, err := CheckSkills(store, func(p Pack) ([]string, error) {
		ran = append(ran, p.Metadata.ID)
		return nil, nil
	}, CheckConfig{Max: 2})
	require.NoError(t, err)
	assert.Len(t, ran, 2)
}

// --- outcomes ---

// A runner error is reported as an error, NOT as a regression: "I could not
// tell you" is a different answer from "it broke", and conflating them would
// let a broken harness look like a broken skill.
func TestCheckDistinguishesErrorFromFailure(t *testing.T) {
	store := seedStore(t, casePack("broken-harness", "c1"), casePack("regressed", "c1"))

	results, err := CheckSkills(store, func(p Pack) ([]string, error) {
		if p.Metadata.ID == "broken-harness" {
			return nil, errors.New("case registry unreadable")
		}
		return []string{"c1"}, nil
	}, CheckConfig{})
	require.NoError(t, err)

	byID := map[string]CheckResult{}
	for _, r := range results {
		byID[r.Pack.Metadata.ID] = r
	}

	assert.Error(t, byID["broken-harness"].Err)
	assert.Empty(t, byID["broken-harness"].FailedCases, "an error is not a failing case")
	assert.False(t, byID["broken-harness"].Recorded, "and it is not recorded as a result")

	assert.NoError(t, byID["regressed"].Err)
	assert.Equal(t, []string{"c1"}, byID["regressed"].FailedCases)
}

// An errored check leaves the skill's last recorded state alone: overwriting it
// with "no failures" would make a broken harness look like a passing check.
func TestErrorDoesNotOverwriteTheRecord(t *testing.T) {
	store := seedStore(t, casePack("sk-1", "c1"))
	_, err := store.RecordVerification("sk-1", []string{"c1"}, time.Now().UTC())
	require.NoError(t, err)

	_, err = CheckSkills(store, func(Pack) ([]string, error) {
		return nil, errors.New("harness exploded")
	}, CheckConfig{Record: true})
	require.NoError(t, err)

	pack, err := store.Load("sk-1")
	require.NoError(t, err)
	assert.Equal(t, VerifyFailed, pack.Metadata.LastVerifyStatus,
		"a check that could not run says nothing about the skill")
	assert.Equal(t, []string{"c1"}, pack.Metadata.LastVerifyFailedCases)
}

// Record=false checks and reports without writing, so an operator can look
// before changing a version-controlled file.
func TestRecordCanBeDisabled(t *testing.T) {
	store := seedStore(t, casePack("sk-1", "c1"))

	results, err := CheckSkills(store, func(Pack) ([]string, error) {
		return []string{"c1"}, nil
	}, CheckConfig{Record: false})
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.False(t, results[0].Recorded)

	pack, err := store.Load("sk-1")
	require.NoError(t, err)
	assert.Empty(t, pack.Metadata.LastVerifyStatus, "nothing was written")
}

// With recording on, the outcome is persisted.
func TestRecordWritesThrough(t *testing.T) {
	store := seedStore(t, casePack("sk-1", "c1"))
	at := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

	results, err := CheckSkills(store, func(Pack) ([]string, error) {
		return nil, nil
	}, CheckConfig{Record: true, Now: at})
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.True(t, results[0].Recorded)
	assert.Equal(t, VerifyPassed, results[0].Pack.Metadata.LastVerifyStatus)

	pack, err := store.Load("sk-1")
	require.NoError(t, err)
	assert.Equal(t, VerifyPassed, pack.Metadata.LastVerifyStatus)
	assert.True(t, pack.Metadata.LastVerifiedAt.Equal(at))
}

// --- summaries ---

func TestSummarize(t *testing.T) {
	results := []CheckResult{
		{Pack: casePack("pass", "c1")},
		{Pack: casePack("fail", "c1"), FailedCases: []string{"c1"}},
		{Pack: casePack("err", "c1"), Err: errors.New("boom")},
		{Pack: casePack("skip"), Skipped: "no bound cases"},
		{Pack: casePack("pass2", "c1"), Recorded: true},
	}
	s := Summarize(results)
	assert.Equal(t, 5, s.Checked)
	assert.Equal(t, 2, s.Passed)
	assert.Equal(t, 1, s.Failed)
	assert.Equal(t, 1, s.Errored)
	assert.Equal(t, 1, s.Skipped)
	assert.Equal(t, 1, s.Recorded)

	assert.Equal(t, []string{"fail"}, FailedIDs(results), "only regressions, not errors")
}

// --- attention listing ---

// Only skills whose LAST check failed need attention; never-verified is not the
// same as verified-and-broken.
func TestListNeedingAttention(t *testing.T) {
	store := seedStore(t, casePack("never"), casePack("ok"), casePack("bad"))
	_, err := store.RecordVerification("ok", nil, time.Now().UTC())
	require.NoError(t, err)
	_, err = store.RecordVerification("bad", []string{"c1"}, time.Now().UTC())
	require.NoError(t, err)

	need, err := store.ListNeedingAttention()
	require.NoError(t, err)
	require.Len(t, need, 1)
	assert.Equal(t, "bad", need[0].Metadata.ID)
}

// A deprecated skill is not reported: fixing something nobody runs is not work
// anyone wants.
func TestListNeedingAttentionSkipsDeprecated(t *testing.T) {
	store := seedStore(t, casePack("bad"))
	_, err := store.RecordVerification("bad", []string{"c1"}, time.Now().UTC())
	require.NoError(t, err)
	_, err = store.Deprecate("bad", "retired anyway", time.Now().UTC())
	require.NoError(t, err)

	need, err := store.ListNeedingAttention()
	require.NoError(t, err)
	assert.Empty(t, need)
}

// --- backward compatibility ---

// A pack written before these fields existed reads as never verified, which is
// NOT the same as verified-and-broken.
func TestOldPackReadsAsUnverified(t *testing.T) {
	dir := t.TempDir()
	old := `apiVersion: forgex/v1
kind: SkillPack
metadata:
  id: legacy
  version: 0.1.0
  status: published
  review_status: reviewed
  generated_at: 2026-01-01T00:00:00Z
  reviewed_at: 2026-01-01T00:00:00Z
spec:
  trigger:
    description: an older skill
  steps: []
  tool_permissions: []
  eval:
    suite: suite-a
    cases:
      - c1
readme: a written readme
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "legacy.yaml"), []byte(old), 0o644))

	pack, err := NewStore(dir).Load("legacy")
	require.NoError(t, err)
	assert.Empty(t, pack.Metadata.LastVerifyStatus)
	assert.True(t, pack.Metadata.LastVerifiedAt.IsZero())
	assert.False(t, pack.Metadata.NeedsAttention(), "never checked is not a regression")
}

// FormatFailedCases renders names for a report line.
func TestFormatFailedCases(t *testing.T) {
	assert.Equal(t, "(none)", FormatFailedCases(nil))
	assert.Equal(t, "c1, c2", FormatFailedCases([]string{"c1", "c2"}))
}
