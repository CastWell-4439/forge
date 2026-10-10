package judge

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/castwell/forge/internal/agent/core"
)

// countingLLM records how many times it was called, which is how the ordering
// property is checked: a static failure must cost no model call.
type countingLLM struct {
	calls int
	reply string
	err   error
}

func (c *countingLLM) Chat(_ context.Context, _ []core.Message) (string, error) {
	c.calls++
	if c.err != nil {
		return "", c.err
	}
	return c.reply, nil
}

func (c *countingLLM) ChatWithUsage(ctx context.Context, m []core.Message) (core.ChatResult, error) {
	r, err := c.Chat(ctx, m)
	return core.ChatResult{Content: r}, err
}

const goodScoreReply = `{"score": 0.9, "reason": "覆盖了全部三项指标", "suggestion": ""}`

// writeArtifacts creates files in a temp workspace and returns it.
func writeArtifacts(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, n := range names {
		require.NoError(t, os.WriteFile(filepath.Join(dir, n), []byte("content"), 0o600))
	}
	return dir
}

func decodeScore(t *testing.T, out string) Score {
	t.Helper()
	var s Score
	require.NoError(t, json.Unmarshal([]byte(out), &s), "output: %s", out)
	return s
}

// The architectural property this package exists to hold: the output is a
// measurement, and carries no verdict. If a `passed` field ever appears, the
// scorer has started deciding its own outcome.
func TestScoreCarriesNoVerdict(t *testing.T) {
	dir := writeArtifacts(t, "report.md")
	llm := &countingLLM{reply: goodScoreReply}
	w := NewWorker(Config{Workspace: dir}, llm)

	out, err := w.Execute(context.Background(), "run", map[string]any{
		"acceptance": map[string]any{
			"criteria":  "报告覆盖全部指标",
			"artifacts": []any{"report.md"},
		},
		"subject": "报告内容",
	})
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &raw))

	for _, forbidden := range []string{"passed", "pass", "verdict", "acceptable", "ok"} {
		_, present := raw[forbidden]
		assert.False(t, present,
			"the score must not carry %q: deciding is the workflow's job, not the scorer's", forbidden)
	}
	assert.Contains(t, raw, "score", "it reports a measurement instead")
}

// The scorer must not be told the bar, so it cannot decide its own outcome. The
// acceptance declares a threshold, and none of it may reach the model.
func TestScorerIsNotToldTheThreshold(t *testing.T) {
	dir := writeArtifacts(t, "out.txt")

	var sent string
	spy := &spyLLM{inner: &countingLLM{reply: goodScoreReply}, record: &sent}
	w := NewWorker(Config{Workspace: dir}, spy)

	_, err := w.Execute(context.Background(), "run", map[string]any{
		"acceptance": map[string]any{
			"criteria":  "输出可用",
			"artifacts": []any{"out.txt"},
			"threshold": 0.85,
		},
	})
	require.NoError(t, err)

	assert.NotContains(t, sent, "0.85", "the threshold must not reach the model")
	assert.NotContains(t, sent, "threshold")
}

// Static before dynamic: a missing artifact scores zero and costs no model call.
func TestMissingArtifactScoresZeroWithoutCallingTheModel(t *testing.T) {
	dir := writeArtifacts(t, "present.txt")
	llm := &countingLLM{reply: goodScoreReply}
	w := NewWorker(Config{Workspace: dir}, llm)

	out, err := w.Execute(context.Background(), "run", map[string]any{
		"acceptance": map[string]any{
			"criteria":  "产出三份材料",
			"artifacts": []any{"present.txt", "absent.txt", "also-absent.txt"},
		},
	})
	require.NoError(t, err)

	assert.Zero(t, llm.calls,
		"a missing artifact must not cost a model call: there is nothing to assess")

	score := decodeScore(t, out)
	assert.Zero(t, score.Score)
	assert.False(t, score.Scored, "the quality question was never asked")
	assert.Contains(t, score.Reason, "absent.txt")
	assert.Contains(t, score.Reason, "also-absent.txt")
	assert.NotContains(t, score.Reason, "present.txt", "only the missing ones are named")
	assert.Contains(t, score.Suggestion, "absent.txt", "a retry needs to know what to produce")
}

// All present: the quality question is asked.
func TestAllArtifactsPresentScoresQuality(t *testing.T) {
	dir := writeArtifacts(t, "report.md")
	llm := &countingLLM{reply: goodScoreReply}
	w := NewWorker(Config{Workspace: dir, Model: "test-model"}, llm)

	out, err := w.Execute(context.Background(), "run", map[string]any{
		"acceptance": map[string]any{
			"criteria":  "报告覆盖全部指标",
			"artifacts": []any{"report.md"},
		},
		"subject": "报告正文",
	})
	require.NoError(t, err)

	assert.Equal(t, 1, llm.calls, "the model is consulted exactly once")

	score := decodeScore(t, out)
	assert.InDelta(t, 0.9, score.Score, 0.001)
	assert.True(t, score.Scored)
	assert.Equal(t, "test-model", score.Model)
	assert.Contains(t, score.Reason, "指标")

	require.Len(t, score.Artifacts, 1)
	assert.True(t, score.Artifacts[0].Present)
}

// No declared artifacts means the static check is skipped and quality is scored:
// a requirement that named no artifacts has not asked for a completeness check.
func TestNoArtifactsDeclaredGoesStraightToQuality(t *testing.T) {
	llm := &countingLLM{reply: goodScoreReply}
	w := NewWorker(Config{}, llm)

	out, err := w.Execute(context.Background(), "run", map[string]any{
		"acceptance": map[string]any{"criteria": "输出可用"},
		"subject":    "内容",
	})
	require.NoError(t, err)

	assert.Equal(t, 1, llm.calls)
	assert.Empty(t, decodeScore(t, out).Artifacts)
}

// An empty file is not an artifact. It exists, which is exactly why presence
// alone would have called it present.
func TestEmptyFileCountsAsMissing(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "empty.txt"), nil, 0o600))

	llm := &countingLLM{reply: goodScoreReply}
	w := NewWorker(Config{Workspace: dir}, llm)

	out, err := w.Execute(context.Background(), "run", map[string]any{
		"acceptance": map[string]any{
			"criteria":  "有产出",
			"artifacts": []any{"empty.txt"},
		},
	})
	require.NoError(t, err)

	score := decodeScore(t, out)
	assert.Zero(t, score.Score)
	assert.False(t, score.Scored)
	assert.Zero(t, llm.calls)
	assert.Contains(t, score.Artifacts[0].Detail, "empty")
}

// A directory satisfies a declared artifact: some outputs are a directory of
// files rather than a single file.
func TestDirectorySatisfiesAnArtifact(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "out"), 0o755))

	llm := &countingLLM{reply: goodScoreReply}
	w := NewWorker(Config{Workspace: dir}, llm)

	_, err := w.Execute(context.Background(), "run", map[string]any{
		"acceptance": map[string]any{
			"criteria":  "有产出",
			"artifacts": []any{"out"},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, 1, llm.calls, "a directory counts as produced")
}

// A glob that matches files is satisfied; one that matches nothing is a miss.
// A requirement often says "one report per region" without naming the regions,
// and treating the pattern itself as a missing file would report a false absence.
func TestGlobArtifacts(t *testing.T) {
	t.Run("matches", func(t *testing.T) {
		dir := writeArtifacts(t, "r1.csv", "r2.csv")
		llm := &countingLLM{reply: goodScoreReply}
		w := NewWorker(Config{Workspace: dir}, llm)

		out, err := w.Execute(context.Background(), "run", map[string]any{
			"acceptance": map[string]any{"criteria": "x", "artifacts": []any{"*.csv"}},
		})
		require.NoError(t, err)
		assert.Equal(t, 1, llm.calls)
		assert.Contains(t, decodeScore(t, out).Artifacts[0].Detail, "2 file(s)")
	})

	t.Run("matches nothing", func(t *testing.T) {
		dir := writeArtifacts(t, "a.txt")
		llm := &countingLLM{reply: goodScoreReply}
		w := NewWorker(Config{Workspace: dir}, llm)

		out, err := w.Execute(context.Background(), "run", map[string]any{
			"acceptance": map[string]any{"criteria": "x", "artifacts": []any{"*.csv"}},
		})
		require.NoError(t, err)

		score := decodeScore(t, out)
		assert.Zero(t, score.Score)
		assert.Zero(t, llm.calls)
		assert.Contains(t, score.Artifacts[0].Detail, "no files match")
	})
}

// A malformed glob is reported against that artifact rather than failing the
// whole run: the other declarations still say something useful.
func TestMalformedGlobIsReportedAsAMiss(t *testing.T) {
	llm := &countingLLM{reply: goodScoreReply}
	w := NewWorker(Config{Workspace: t.TempDir()}, llm)

	out, err := w.Execute(context.Background(), "run", map[string]any{
		"acceptance": map[string]any{"criteria": "x", "artifacts": []any{"[bad"}},
	})
	require.NoError(t, err)

	score := decodeScore(t, out)
	assert.False(t, score.Artifacts[0].Present)
	assert.Contains(t, score.Artifacts[0].Detail, "malformed")
}

// A model that cannot be reached is an error, not a zero: "could not measure"
// and "measured zero" are different facts and must not be collapsed.
func TestUnreachableModelIsAnErrorNotAZero(t *testing.T) {
	dir := writeArtifacts(t, "a.txt")
	llm := &countingLLM{err: errors.New("provider is down")}
	w := NewWorker(Config{Workspace: dir}, llm)

	_, err := w.Execute(context.Background(), "run", map[string]any{
		"acceptance": map[string]any{"criteria": "x", "artifacts": []any{"a.txt"}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "provider is down")
}

// No acceptance at all is a workflow defect, not a score: a bar that says
// nothing cannot be met or missed.
func TestNoAcceptanceIsAnError(t *testing.T) {
	w := NewWorker(Config{}, &countingLLM{reply: goodScoreReply})

	_, err := w.Execute(context.Background(), "run", map[string]any{"task": "做点什么"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no acceptance")
}

// No model configured, with the static check passed, is reported as such: a
// deployment without an LLM can still run the completeness half, and the message
// says which half is unavailable.
func TestNoModelConfiguredIsReportedClearly(t *testing.T) {
	dir := writeArtifacts(t, "a.txt")
	w := NewWorker(Config{Workspace: dir}, nil)

	_, err := w.Execute(context.Background(), "run", map[string]any{
		"acceptance": map[string]any{"criteria": "x", "artifacts": []any{"a.txt"}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no model configured")
}

// An acceptance given as a single sentence is understood, so an author does not
// have to wrap it to be scored.
func TestSentenceAcceptanceIsUnderstood(t *testing.T) {
	llm := &countingLLM{reply: goodScoreReply}
	w := NewWorker(Config{}, llm)

	_, err := w.Execute(context.Background(), "run", map[string]any{
		"acceptance": "输出可用",
		"subject":    "内容",
	})
	require.NoError(t, err)
	assert.Equal(t, 1, llm.calls)
}

// An out-of-range score is clamped: a model returning 7 is reporting on a scale
// it invented, and passing that through would make every threshold meaningless.
func TestOutOfRangeScoresAreClamped(t *testing.T) {
	for _, tc := range []struct {
		reply string
		want  float64
	}{
		{`{"score": 7, "reason": "great"}`, 1},
		{`{"score": -3, "reason": "bad"}`, 0},
		{`{"score": 0.42, "reason": "ok"}`, 0.42},
	} {
		t.Run(tc.reply, func(t *testing.T) {
			w := NewWorker(Config{}, &countingLLM{reply: tc.reply})
			out, err := w.Execute(context.Background(), "run", map[string]any{
				"acceptance": map[string]any{"criteria": "x"},
			})
			require.NoError(t, err)
			assert.InDelta(t, tc.want, decodeScore(t, out).Score, 0.001)
		})
	}
}

// JSON is located inside a reply that has preamble or fences: discarding a
// usable score over formatting would waste the call.
func TestScoringParsesJSONFromNoisyReplies(t *testing.T) {
	for _, reply := range []string{
		"```json\n" + goodScoreReply + "\n```",
		"好的，这是我的评估：\n" + goodScoreReply,
		goodScoreReply + "\n希望有帮助。",
	} {
		t.Run(reply[:12], func(t *testing.T) {
			w := NewWorker(Config{}, &countingLLM{reply: reply})
			out, err := w.Execute(context.Background(), "run", map[string]any{
				"acceptance": map[string]any{"criteria": "x"},
			})
			require.NoError(t, err)
			assert.InDelta(t, 0.9, decodeScore(t, out).Score, 0.001)
		})
	}
}

// A reply with no JSON is an error: there is no score to report, and inventing
// one would be worse than saying the measurement failed.
func TestUnparseableReplyIsAnError(t *testing.T) {
	w := NewWorker(Config{}, &countingLLM{reply: "我觉得挺好的"})

	_, err := w.Execute(context.Background(), "run", map[string]any{
		"acceptance": map[string]any{"criteria": "x"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no JSON object")
}

// The recorded reason is capped: beyond a few hundred characters it is a
// transcript, and the transcript is in the run's own log.
func TestLongReasonIsCapped(t *testing.T) {
	long := strings.Repeat("很长的理由。", 500)
	reply, err := json.Marshal(map[string]any{"score": 0.5, "reason": long})
	require.NoError(t, err)

	w := NewWorker(Config{MaxReason: 100}, &countingLLM{reply: string(reply)})
	out, err := w.Execute(context.Background(), "run", map[string]any{
		"acceptance": map[string]any{"criteria": "x"},
	})
	require.NoError(t, err)

	score := decodeScore(t, out)
	assert.LessOrEqual(t, len(score.Reason), 110, "the cap holds (plus the ellipsis)")
	assert.True(t, strings.HasSuffix(score.Reason, "…"), "and a cut is marked as one")
}

// An unknown action names what is supported.
func TestUnknownAction(t *testing.T) {
	w := NewWorker(Config{}, &countingLLM{})

	_, err := w.Execute(context.Background(), "score", map[string]any{"acceptance": "x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown action")
	assert.Contains(t, err.Error(), "run")
}

// Artifact checks are reported in a stable order, so two runs over the same
// declaration produce the same report and can be diffed.
func TestArtifactChecksAreOrdered(t *testing.T) {
	dir := writeArtifacts(t, "a.txt", "b.txt", "c.txt")
	w := NewWorker(Config{Workspace: dir}, &countingLLM{reply: goodScoreReply})

	out, err := w.Execute(context.Background(), "run", map[string]any{
		"acceptance": map[string]any{
			"criteria":  "x",
			"artifacts": []any{"c.txt", "a.txt", "b.txt"},
		},
	})
	require.NoError(t, err)

	score := decodeScore(t, out)
	require.Len(t, score.Artifacts, 3)
	assert.Equal(t, []string{"a.txt", "b.txt", "c.txt"},
		[]string{score.Artifacts[0].Path, score.Artifacts[1].Path, score.Artifacts[2].Path})
}

// --- helper ---

// spyLLM records what the scorer sends to the model.
type spyLLM struct {
	inner  core.LLMClient
	record *string
}

func (s *spyLLM) Chat(ctx context.Context, m []core.Message) (string, error) {
	var b strings.Builder
	for _, msg := range m {
		b.WriteString(msg.Content)
		b.WriteString("\n")
	}
	*s.record = b.String()
	return s.inner.Chat(ctx, m)
}

func (s *spyLLM) ChatWithUsage(ctx context.Context, m []core.Message) (core.ChatResult, error) {
	r, err := s.Chat(ctx, m)
	return core.ChatResult{Content: r}, err
}
