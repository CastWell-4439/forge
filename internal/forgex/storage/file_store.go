package storage

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/castwell/forge/internal/forgex/model"
	"gopkg.in/yaml.v3"
)

// FileStore persists ForgeX runs to a local filesystem layout.
type FileStore struct {
	layout Layout
}

// NewFileStore creates a filesystem-backed Store.
func NewFileStore(root string) *FileStore {
	return &FileStore{layout: NewLayout(root)}
}

// Layout returns the store layout.
func (s *FileStore) Layout() Layout {
	return s.layout
}

// InitRun creates the run directory and writes initial run/task packet files.
func (s *FileStore) InitRun(ctx context.Context, run model.Run, packet model.TaskPacket) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(s.layout.RunDir(run.ID), 0o755); err != nil {
		return err
	}
	if err := s.SaveRun(ctx, run); err != nil {
		return err
	}
	return writeYAMLFile(ctx, s.layout.TaskPacketFile(run.ID), packet)
}

// SaveRun writes run metadata as JSON.
func (s *FileStore) SaveRun(ctx context.Context, run model.Run) error {
	return writeJSONFile(ctx, s.layout.RunFile(run.ID), run)
}

// AppendEvent appends one event to events.jsonl.
func (s *FileStore) AppendEvent(ctx context.Context, event model.Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return AppendJSONL(s.layout.EventsFile(event.RunID), event)
}

// AppendSpan appends one span to spans.jsonl.
func (s *FileStore) AppendSpan(ctx context.Context, span model.Span) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return AppendJSONL(s.layout.SpansFile(span.RunID), span)
}

// AppendToolCall appends one tool call to tool_calls.jsonl.
func (s *FileStore) AppendToolCall(ctx context.Context, call model.ToolCall) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return AppendJSONL(s.layout.ToolCallsFile(call.RunID), call)
}

// AppendError appends one error envelope to errors.jsonl.
func (s *FileStore) AppendError(ctx context.Context, envelope model.ErrorEnvelope) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return AppendJSONL(s.layout.ErrorsFile(envelope.RunID), envelope)
}

// AppendStopDecision appends one stop decision to stop_decisions.jsonl.
func (s *FileStore) AppendStopDecision(ctx context.Context, decision model.StopDecision) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return AppendJSONL(s.layout.StopDecisionsFile(decision.RunID), decision)
}

// AppendStopSignal appends one termination signal to stop_signals.jsonl.
func (s *FileStore) AppendStopSignal(ctx context.Context, signal model.StopSignalRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return AppendJSONL(s.layout.StopSignalsFile(signal.RunID), signal)
}

// SaveProgressLedger writes the latest progress ledger as YAML.
func (s *FileStore) SaveProgressLedger(ctx context.Context, ledger model.ProgressLedger) error {
	return writeYAMLFile(ctx, s.layout.ProgressLedgerFile(ledger.RunID), ledger)
}

// AppendContextPack appends one context pack to context_packs.jsonl.
func (s *FileStore) AppendContextPack(ctx context.Context, pack model.ContextPack) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return AppendJSONL(s.layout.ContextPacksFile(pack.RunID), pack)
}

// AppendPolicyDecision appends one policy decision to policy_decisions.jsonl.
func (s *FileStore) AppendPolicyDecision(ctx context.Context, decision model.PolicyDecision) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return AppendJSONL(s.layout.PolicyDecisionsFile(decision.RunID), decision)
}

// AppendContractValidation appends one validation result to contract_validations.jsonl.
func (s *FileStore) AppendContractValidation(ctx context.Context, validation model.ContractValidation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return AppendJSONL(s.layout.ContractValidationsFile(validation.RunID), validation)
}

// SaveWorldState writes the latest world state as YAML.
func (s *FileStore) SaveWorldState(ctx context.Context, state model.WorldState) error {
	return writeYAMLFile(ctx, s.layout.WorldStateFile(state.RunID), state)
}

// AppendStateClaim appends one state claim to state_claims.jsonl.
func (s *FileStore) AppendStateClaim(ctx context.Context, claim model.StateClaim) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return AppendJSONL(s.layout.StateClaimsFile(claim.RunID), claim)
}

// AppendArtifact appends one artifact record to artifacts.jsonl.
func (s *FileStore) AppendArtifact(ctx context.Context, artifact model.ArtifactRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return AppendJSONL(s.layout.ArtifactsFile(artifact.RunID), artifact)
}

// AppendLesson appends one lesson to lessons.jsonl. The lesson is routed to its
// source run's directory via lesson.SourceRunID.
func (s *FileStore) AppendLesson(ctx context.Context, lesson model.Lesson) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return AppendJSONL(s.layout.LessonsFile(lesson.SourceRunID), lesson)
}

// WriteReport writes the Markdown report for a run.
func (s *FileStore) WriteReport(ctx context.Context, runID string, markdown string) error {
	return writeBytesFile(ctx, s.layout.ReportFile(runID), []byte(markdown))
}

// WriteBadCase writes the bad case YAML for a run.
func (s *FileStore) WriteBadCase(ctx context.Context, runID string, yamlBytes []byte) error {
	return writeBytesFile(ctx, s.layout.BadCaseFile(runID), yamlBytes)
}

func writeJSONFile(ctx context.Context, path string, value any) error {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	return writeBytesFile(ctx, path, encoded)
}

func writeYAMLFile(ctx context.Context, path string, value any) error {
	encoded, err := yaml.Marshal(value)
	if err != nil {
		return err
	}
	return writeBytesFile(ctx, path, encoded)
}

func writeBytesFile(ctx context.Context, path string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

var _ Store = (*FileStore)(nil)
