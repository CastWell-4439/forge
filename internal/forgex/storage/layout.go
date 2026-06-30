package storage

import "path/filepath"

// Layout defines the on-disk structure for ForgeX run artifacts.
type Layout struct {
	Root string
}

// NewLayout creates a Layout with the provided root. Empty root defaults to .forgex.
func NewLayout(root string) Layout {
	if root == "" {
		root = ".forgex"
	}
	return Layout{Root: root}
}

// RunsDir returns the directory that contains all run directories.
func (l Layout) RunsDir() string {
	return filepath.Join(l.Root, "runs")
}

// RunDir returns the directory for one run.
func (l Layout) RunDir(runID string) string {
	return filepath.Join(l.RunsDir(), runID)
}

// RunFile returns the run metadata file path.
func (l Layout) RunFile(runID string) string {
	return filepath.Join(l.RunDir(runID), "run.json")
}

// TaskPacketFile returns the task packet file path.
func (l Layout) TaskPacketFile(runID string) string {
	return filepath.Join(l.RunDir(runID), "task_packet.yaml")
}

// EventsFile returns the events JSONL file path.
func (l Layout) EventsFile(runID string) string {
	return filepath.Join(l.RunDir(runID), "events.jsonl")
}

// SpansFile returns the spans JSONL file path.
func (l Layout) SpansFile(runID string) string {
	return filepath.Join(l.RunDir(runID), "spans.jsonl")
}

// ToolCallsFile returns the tool calls JSONL file path.
func (l Layout) ToolCallsFile(runID string) string {
	return filepath.Join(l.RunDir(runID), "tool_calls.jsonl")
}

// ErrorsFile returns the errors JSONL file path.
func (l Layout) ErrorsFile(runID string) string {
	return filepath.Join(l.RunDir(runID), "errors.jsonl")
}

// StopDecisionsFile returns the stop decisions JSONL file path.
func (l Layout) StopDecisionsFile(runID string) string {
	return filepath.Join(l.RunDir(runID), "stop_decisions.jsonl")
}

// ProgressLedgerFile returns the progress ledger YAML file path.
func (l Layout) ProgressLedgerFile(runID string) string {
	return filepath.Join(l.RunDir(runID), "progress_ledger.yaml")
}

// ContextPacksFile returns the context packs JSONL file path.
func (l Layout) ContextPacksFile(runID string) string {
	return filepath.Join(l.RunDir(runID), "context_packs.jsonl")
}

// PolicyDecisionsFile returns the policy decisions JSONL file path.
func (l Layout) PolicyDecisionsFile(runID string) string {
	return filepath.Join(l.RunDir(runID), "policy_decisions.jsonl")
}

// ContractValidationsFile returns the contract validations JSONL file path.
func (l Layout) ContractValidationsFile(runID string) string {
	return filepath.Join(l.RunDir(runID), "contract_validations.jsonl")
}

// WorldStateFile returns the world state YAML file path.
func (l Layout) WorldStateFile(runID string) string {
	return filepath.Join(l.RunDir(runID), "world_state.yaml")
}

// StateClaimsFile returns the state claims JSONL file path.
func (l Layout) StateClaimsFile(runID string) string {
	return filepath.Join(l.RunDir(runID), "state_claims.jsonl")
}

// ArtifactsFile returns the artifact index JSONL file path.
func (l Layout) ArtifactsFile(runID string) string {
	return filepath.Join(l.RunDir(runID), "artifacts.jsonl")
}

// ReportFile returns the Markdown report file path.
func (l Layout) ReportFile(runID string) string {
	return filepath.Join(l.RunDir(runID), "report.md")
}

// BadCaseFile returns the bad case YAML file path.
func (l Layout) BadCaseFile(runID string) string {
	return filepath.Join(l.RunDir(runID), "badcase.yaml")
}
