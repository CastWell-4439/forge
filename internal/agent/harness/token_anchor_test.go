package harness

import (
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/castwell/forge/internal/agent/core"
)

// --- EstimateTokens counts native tool calls ---

// An assistant message carrying tool calls costs tokens the provider bills for.
// Ignoring them made the native path under-estimate, which is the dangerous
// direction: the estimate says a request fits and the provider disagrees.
func TestEstimateTokensCountsToolCalls(t *testing.T) {
	plain := []core.Message{{Role: "assistant", Content: "let me look"}}
	withCalls := []core.Message{{
		Role:    "assistant",
		Content: "let me look",
		ToolCalls: []core.NativeToolCall{{
			ID:   "call-1",
			Name: "file.read",
			Arguments: map[string]any{
				"path": strings.Repeat("x", 3000),
			},
		}},
	}}

	without := EstimateTokens(plain)
	with := EstimateTokens(withCalls)

	assert.Greater(t, with, without, "tool call arguments are billed tokens")
	assert.Greater(t, with-without, 500, "a 3000-char argument is not a rounding error")
}

// Several calls on one message all count, and a call with no arguments still
// costs its name plus the envelope.
func TestEstimateTokensCountsEveryCall(t *testing.T) {
	one := []core.Message{{Role: "assistant", ToolCalls: []core.NativeToolCall{
		{Name: "a.read", Arguments: map[string]any{"p": "x"}},
	}}}
	three := []core.Message{{Role: "assistant", ToolCalls: []core.NativeToolCall{
		{Name: "a.read", Arguments: map[string]any{"p": "x"}},
		{Name: "b.read", Arguments: map[string]any{"p": "y"}},
		{Name: "c.read", Arguments: map[string]any{"p": "z"}},
	}}}
	assert.Greater(t, EstimateTokens(three), EstimateTokens(one))

	bare := []core.Message{{Role: "assistant", ToolCalls: []core.NativeToolCall{{Name: "ping"}}}}
	assert.Greater(t, EstimateTokens(bare), EstimateTokens([]core.Message{{Role: "assistant"}}))
}

// A message with no tool calls is unchanged from the historical formula, so the
// prompt path is byte-for-byte what it was.
func TestEstimateTokensPromptPathUnchanged(t *testing.T) {
	msgs := []core.Message{
		{Role: "system", Content: strings.Repeat("s", 300)},
		{Role: "user", Content: strings.Repeat("u", 600)},
	}
	// 4 + 300/3 + 4 + 600/3 = 308
	assert.Equal(t, 308, EstimateTokens(msgs))
}

// --- anchor and delta ---

// With an anchor, the estimate is the exact base plus a guess at what was
// added — the error stops being spread over the whole history.
func TestAnchorConfinesErrorToTheDelta(t *testing.T) {
	cm := NewContextManager(100000, nil)

	// Pretend the provider reported 1000 tokens for a 2-message conversation.
	sent := []core.Message{{Role: "user", Content: "hello"}, {Role: "assistant", Content: "hi"}}
	cm.observeAnchor(sent, 1000)
	require.True(t, cm.HasAnchor())

	// Three more messages arrive. The estimate must be 1000 + estimate(new).
	grown := append(append([]core.Message{}, sent...),
		core.Message{Role: "user", Content: strings.Repeat("n", 300)},
		core.Message{Role: "assistant", Content: strings.Repeat("n", 300)},
		core.Message{Role: "user", Content: strings.Repeat("n", 300)},
	)

	base, delta := cm.anchoredEstimate(grown)
	assert.Equal(t, 1000, base, "the base is the provider's exact count")
	assert.Equal(t, EstimateTokens(grown[2:]), delta, "only the new messages are estimated")
	assert.Equal(t, 1000+delta, cm.estimateTokens(grown))
}

// Without an anchor the whole list is estimated, which is the historical
// behaviour and what a fresh conversation uses.
func TestNoAnchorEstimatesTheWholeList(t *testing.T) {
	cm := NewContextManager(100000, nil)
	require.False(t, cm.HasAnchor())

	msgs := []core.Message{{Role: "user", Content: strings.Repeat("x", 900)}}
	assert.Equal(t, EstimateTokens(msgs), cm.estimateTokens(msgs))
}

// The anchor is set from the list that was SENT: the count describes that list,
// and the next estimate adds whatever comes after it.
func TestAnchorBoundaryIsTheSentLength(t *testing.T) {
	cm := NewContextManager(100000, nil)
	sent := []core.Message{{Role: "user", Content: "a"}}
	cm.observeAnchor(sent, 500)

	// Same list again: nothing added, so the estimate is exactly the truth.
	assert.Equal(t, 500, cm.estimateTokens(sent))

	// One more message: base plus that one message.
	grown := append(append([]core.Message{}, sent...), core.Message{Role: "user", Content: "bb"})
	_, delta := cm.anchoredEstimate(grown)
	assert.Equal(t, EstimateTokens(grown[1:]), delta)
}

// A zero or negative reported count is not an anchor: a provider that omits
// usage must not be read as "zero tokens".
func TestAnchorIgnoresZeroUsage(t *testing.T) {
	cm := NewContextManager(100000, nil)
	cm.observeAnchor([]core.Message{{Role: "user", Content: "x"}}, 0)
	assert.False(t, cm.HasAnchor())

	cm.observeAnchor([]core.Message{{Role: "user", Content: "x"}}, -5)
	assert.False(t, cm.HasAnchor())
}

// A list SHORTER than the anchor's boundary means the conversation was replaced
// rather than grown, so the anchor cannot apply. The safe reading is "count it
// all" — not "the delta is negative".
func TestShorterListFallsBackToFullEstimate(t *testing.T) {
	cm := NewContextManager(100000, nil)
	cm.observeAnchor([]core.Message{
		{Role: "user", Content: "a"}, {Role: "user", Content: "b"}, {Role: "user", Content: "c"},
	}, 900)

	short := []core.Message{{Role: "user", Content: "a"}}
	assert.Equal(t, EstimateTokens(short), cm.estimateTokens(short),
		"a shorter list is counted from scratch")
}

// resetAnchor drops the base so the next estimate counts everything.
func TestResetAnchor(t *testing.T) {
	cm := NewContextManager(100000, nil)
	cm.observeAnchor([]core.Message{{Role: "user", Content: "x"}}, 700)
	require.True(t, cm.HasAnchor())

	cm.resetAnchor()
	assert.False(t, cm.HasAnchor())

	msgs := []core.Message{{Role: "user", Content: strings.Repeat("y", 300)}}
	assert.Equal(t, EstimateTokens(msgs), cm.estimateTokens(msgs))
}

// Compaction that REMOVES messages must drop the anchor. Keeping it would leave
// the base exact while the boundary it counts from describes content that is
// gone — a drift that is invisible until a budget decision looks wrong.
func TestCompactionResetsTheAnchor(t *testing.T) {
	// A small window with no reply reservation, and enough history that the
	// keep-tail floor cannot swallow it: compaction then really drops messages.
	cm := NewContextManager(3000, nil)
	cm.SetOutputReserve(0)

	var msgs []core.Message
	msgs = append(msgs, core.Message{Role: "user", Content: "the task"})
	for i := 0; i < 40; i++ {
		msgs = append(msgs, core.Message{
			Role:    "user",
			Content: "[Tool \"file.read\" result]: " + strings.Repeat("z", 900),
		})
	}
	cm.observeAnchor(msgs, 9000)
	require.True(t, cm.HasAnchor())

	out, err := cm.CompactIfNeeded(t.Context(), msgs)
	require.NoError(t, err)

	// If this fixture ever stops compacting, the assertion below would pass for
	// the wrong reason, so the precondition is asserted explicitly.
	require.Less(t, len(out), len(msgs), "the fixture must actually compact")
	assert.False(t, cm.HasAnchor(), "a shorter list invalidates the anchor")
}

// S2 slimming edits content but keeps the list length, and that case may stay
// anchored: the base then OVER-estimates the now-shorter content, which errs
// toward compacting earlier rather than overflowing later.
func TestSlimmingKeepsTheAnchor(t *testing.T) {
	cm := NewContextManager(100000, nil)
	msgs := []core.Message{
		{Role: "user", Content: "task"},
		{Role: "user", Content: "[Tool \"file.read\" result]: " + strings.Repeat("z", 9000)},
	}
	cm.observeAnchor(msgs, 500)
	require.True(t, cm.HasAnchor())

	out, err := cm.CompactIfNeeded(t.Context(), msgs)
	require.NoError(t, err)
	require.Len(t, out, len(msgs), "length preserved")
	require.Less(t, len(out[1].Content), 9000, "content was slimmed")

	assert.True(t, cm.HasAnchor(), "a same-length edit keeps the anchor (conservative direction)")
}

// --- cross-run calibration store ---

// The ratio is learned once and reused, so a run too short to reach the sample
// count still benefits.
func TestCalibrationStoreIsReusedAcrossRuns(t *testing.T) {
	store := NewCalibrationStore()
	for i := 0; i < 6; i++ {
		store.Observe("model-a", 1000, 2000) // provider counts 2x
	}

	ratio, samples := store.Ratio("model-a")
	require.GreaterOrEqual(t, samples, calibrationSamples)
	assert.InDelta(t, 2.0, ratio, 0.05)

	// A fresh manager seeded from the store is calibrated from its first call.
	cm := NewContextManager(100000, nil)
	cm.SetCalibrationStore(store, "model-a")
	cm.seedFromStore()

	msgs := []core.Message{{Role: "user", Content: strings.Repeat("x", 3000)}}
	assert.Greater(t, cm.estimateTokens(msgs), EstimateTokens(msgs),
		"the first estimate already reflects what earlier runs learned")
}

// Ratios are kept per model: how many characters a token covers is a property
// of the tokenizer, and two models behind one endpoint do not share it.
func TestCalibrationStoreSeparatesModels(t *testing.T) {
	store := NewCalibrationStore()
	for i := 0; i < 6; i++ {
		store.Observe("model-a", 1000, 2000)
		store.Observe("model-b", 1000, 500)
	}

	ratioA, _ := store.Ratio("model-a")
	ratioB, _ := store.Ratio("model-b")
	assert.InDelta(t, 2.0, ratioA, 0.05)
	assert.InDelta(t, 0.5, ratioB, 0.05)

	models := store.Models()
	assert.Len(t, models, 2)

	// An unknown model has no ratio, so the caller uses the raw heuristic.
	ratioC, samplesC := store.Ratio("model-c")
	assert.Zero(t, ratioC)
	assert.Zero(t, samplesC)
}

// Below the sample count the store reports no ratio: a partially-learned value
// would make the first calls of a run behave differently for no visible reason.
func TestCalibrationStoreNeedsSamples(t *testing.T) {
	store := NewCalibrationStore()
	store.Observe("m", 1000, 3000)
	ratio, samples := store.Ratio("m")
	assert.Zero(t, ratio, "one sample is not a calibration")
	assert.Equal(t, 1, samples, "but the caller can see how far along it is")
}

// An unnamed model shares one bucket rather than being dropped: a measurement
// with an unknown label is still better than none.
func TestCalibrationStoreUnnamedBucket(t *testing.T) {
	store := NewCalibrationStore()
	for i := 0; i < 6; i++ {
		store.Observe("", 1000, 1500)
	}
	ratio, _ := store.Ratio("")
	assert.InDelta(t, 1.5, ratio, 0.05)
}

// A nil store is accepted everywhere and means "per-run calibration only".
func TestNilCalibrationStoreIsSafe(t *testing.T) {
	var store *CalibrationStore
	store.Observe("m", 1000, 2000)
	ratio, samples := store.Ratio("m")
	assert.Zero(t, ratio)
	assert.Zero(t, samples)
	assert.Nil(t, store.Models())

	cm := NewContextManager(100000, nil)
	cm.SetCalibrationStore(nil, "")
	cm.seedFromStore()
	cm.publishToStore(1000, 2000)
	assert.Zero(t, cm.calibrationRatio, "no store, no cross-run learning")
}

// The store is shared by concurrent runs (delegation, overlapping sessions), so
// it must be safe to observe from several goroutines.
func TestCalibrationStoreIsConcurrencySafe(t *testing.T) {
	store := NewCalibrationStore()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				store.Observe("shared", 1000, 2000)
				store.Ratio("shared")
				store.Models()
			}
		}()
	}
	wg.Wait()

	ratio, samples := store.Ratio("shared")
	assert.InDelta(t, 2.0, ratio, 0.05, "concurrent observations converge on the same ratio")
	assert.Equal(t, 1000, samples)
}

// This run keeps using its OWN ratio while publishing to the store: a run's
// behaviour must not change mid-flight because another run observed something.
func TestRunKeepsItsOwnRatioWhilePublishing(t *testing.T) {
	store := NewCalibrationStore()
	cm := NewContextManager(100000, nil)
	cm.SetCalibrationStore(store, "m")

	// This run learns 2x.
	for i := 0; i < 6; i++ {
		cm.ObserveUsage(1000, 2000)
	}
	mine := cm.calibrationRatio
	require.InDelta(t, 2.0, mine, 0.05)

	// Another run records something wildly different.
	for i := 0; i < 50; i++ {
		store.Observe("m", 1000, 1000)
	}

	assert.Equal(t, mine, cm.calibrationRatio, "this run's own value is unchanged")
	assert.Greater(t, len(store.Models()), 0, "and its observations reached the store")
}

// --- model naming ---

// The store keys by model, so the loop must be able to ask its client.
func TestModelOfUsesTheOptionalInterface(t *testing.T) {
	assert.Equal(t, "named-model", modelOf(&namedLLM{name: "named-model"}))

	// A client that cannot name its model is not an error: it shares the
	// unnamed bucket.
	assert.Empty(t, modelOf(&mockLLM{}))
}

// namedLLM is a client that identifies its model.
type namedLLM struct {
	mockLLM
	name string
}

func (n *namedLLM) ModelName() string { return n.name }
