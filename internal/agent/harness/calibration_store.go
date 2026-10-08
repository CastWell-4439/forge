package harness

import (
	"sync"

	"github.com/castwell/forge/internal/agent/core"
)

// Cross-run calibration (N3 follow-up).
//
// The estimate heuristic is off by a factor that depends on the PROVIDER and
// the MODEL, not on the conversation: how many characters a token covers is a
// property of the tokenizer, and it does not change between runs. Yet the ratio
// used to live on the ContextManager, which is built fresh for every run — so a
// run with fewer than calibrationSamples model calls ended without ever
// learning anything, and every short run started from the same guess.
//
// This store moves the ratio up one level: it is keyed by model and shared by
// every run of one agent, which is the scope the ratio actually describes.
//
// It is NOT a global: nothing is shared between agents, because two agents may
// point at different endpoints. The word "global" would be wrong here — the
// right scope is "one agent, one model", and a package-level map would silently
// mix deployments together.

// CalibrationStore remembers the observed/estimated ratio per model.
//
// Safe for concurrent use: a delegation (N5) runs child loops that may share
// their parent's store, and two runs of one agent can overlap.
type CalibrationStore struct {
	mu sync.Mutex
	// byModel is keyed by the model identifier. An empty key is allowed and
	// means "the deployment did not tell us which model this is" — those
	// observations share one bucket rather than being dropped, because a
	// measurement with an unknown label is still better than no measurement.
	byModel map[string]*calibrationEntry
}

type calibrationEntry struct {
	ratio   float64
	samples int
}

// NewCalibrationStore creates an empty store.
func NewCalibrationStore() *CalibrationStore {
	return &CalibrationStore{byModel: map[string]*calibrationEntry{}}
}

// Observe folds one observation into the model's ratio.
//
// It applies the same smoothing, clamping and sample-count rules as the
// per-run calibration, so moving the state up does not change what the ratio
// means — only how long it survives.
func (s *CalibrationStore) Observe(model string, estimated, observed int) {
	if s == nil || estimated <= 0 || observed <= 0 {
		return
	}
	ratio := float64(observed) / float64(estimated)
	if ratio < minCalibrationRatio {
		ratio = minCalibrationRatio
	}
	if ratio > maxCalibrationRatio {
		ratio = maxCalibrationRatio
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.byModel[model]
	if entry == nil {
		entry = &calibrationEntry{}
		s.byModel[model] = entry
	}
	entry.samples++
	if entry.ratio == 0 {
		entry.ratio = ratio
		return
	}
	entry.ratio = entry.ratio*(1-calibrationSmoothing) + ratio*calibrationSmoothing
}

// Ratio returns the ratio for a model and how many samples back it.
//
// A model with fewer than calibrationSamples observations reports (0, n): the
// caller must then use the raw heuristic. Returning a partially-learned ratio
// would make the first two calls of every run behave differently from the
// third, for no reason the operator could see.
func (s *CalibrationStore) Ratio(model string) (ratio float64, samples int) {
	if s == nil {
		return 0, 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.byModel[model]
	if entry == nil {
		return 0, 0
	}
	if entry.samples < calibrationSamples {
		return 0, entry.samples
	}
	return entry.ratio, entry.samples
}

// Models lists the model keys the store has observations for, so an operator
// can see what has been learned. Order is unspecified; callers that need one
// sort it.
func (s *CalibrationStore) Models() []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.byModel))
	for model := range s.byModel {
		out = append(out, model)
	}
	return out
}

// ModelNamer is implemented by LLM clients that know which model they talk to.
//
// It is a separate optional interface rather than a method on core.LLMClient so
// existing implementations keep compiling: a client that cannot name its model
// still works, it just shares the unnamed bucket.
type ModelNamer interface {
	ModelName() string
}

// modelOf reports the model a client identifies as, or "" when it does not say.
func modelOf(llm core.LLMClient) string {
	if namer, ok := llm.(ModelNamer); ok {
		return namer.ModelName()
	}
	return ""
}

// SetCalibrationStore attaches a cross-run store.
//
// A manager without one keeps its per-run calibration, which is the previous
// behaviour: the store is an improvement, not a requirement.
func (cm *ContextManager) SetCalibrationStore(store *CalibrationStore, model string) {
	if cm == nil {
		return
	}
	cm.calibrationStore = store
	cm.calibrationModel = model
}

// seedFromStore pulls the learned ratio in, so the first estimate of a run
// already benefits from previous runs.
//
// It is a read, not a subscription: a run that observes a better ratio keeps
// using its own value for that run, and the store is updated as it goes. The
// alternative (reading the store on every estimate) would make the estimate
// depend on other runs' timing, which is exactly the instability the estimator
// must not have.
func (cm *ContextManager) seedFromStore() {
	if cm == nil || cm.calibrationStore == nil {
		return
	}
	ratio, samples := cm.calibrationStore.Ratio(cm.calibrationModel)
	if samples < calibrationSamples || ratio <= 0 {
		return
	}
	cm.calibrationRatio = ratio
	cm.calibrationSamples = samples
}

// publishToStore folds this run's observations into the shared store, so the
// next run starts from them.
func (cm *ContextManager) publishToStore(estimated, observed int) {
	if cm == nil || cm.calibrationStore == nil {
		return
	}
	cm.calibrationStore.Observe(cm.calibrationModel, estimated, observed)
}
