package storage

import (
	"context"

	"github.com/castwell/forge/internal/forgex/model"
)

// Store persists ForgeX run metadata and append-only run streams.
type Store interface {
	InitRun(ctx context.Context, run model.Run, packet model.TaskPacket) error
	SaveRun(ctx context.Context, run model.Run) error
	AppendEvent(ctx context.Context, event model.Event) error
	AppendSpan(ctx context.Context, span model.Span) error
	AppendToolCall(ctx context.Context, call model.ToolCall) error
	AppendError(ctx context.Context, envelope model.ErrorEnvelope) error
	AppendStopDecision(ctx context.Context, decision model.StopDecision) error
	AppendStopSignal(ctx context.Context, signal model.StopSignalRecord) error
	SaveProgressLedger(ctx context.Context, ledger model.ProgressLedger) error
	AppendContextPack(ctx context.Context, pack model.ContextPack) error
	AppendPolicyDecision(ctx context.Context, decision model.PolicyDecision) error
	AppendContractValidation(ctx context.Context, validation model.ContractValidation) error
	SaveWorldState(ctx context.Context, state model.WorldState) error
	AppendStateClaim(ctx context.Context, claim model.StateClaim) error
	AppendArtifact(ctx context.Context, artifact model.ArtifactRecord) error
	AppendLesson(ctx context.Context, lesson model.Lesson) error
	WriteReport(ctx context.Context, runID string, markdown string) error
	WriteBadCase(ctx context.Context, runID string, yamlBytes []byte) error
}
