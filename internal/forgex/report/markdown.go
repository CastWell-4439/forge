package report

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/castwell/forge/internal/forgex/model"
)

// RunSnapshot is the in-memory view of a single ForgeX run used to render the
// Markdown report and the bad-case export. It bundles the run metadata together
// with every append-only stream captured during the run.
type RunSnapshot struct {
	Run                 model.Run
	TaskPacket          model.TaskPacket
	Events              []model.Event
	ToolCalls           []model.ToolCall
	PolicyDecisions     []model.PolicyDecision
	ContractValidations []model.ContractValidation
	WorldState          *model.WorldState
	StateClaims         []model.StateClaim
	Artifacts           []model.ArtifactRecord
	Errors              []model.ErrorEnvelope
	StopDecisions       []model.StopDecision
	ProgressLedger      *model.ProgressLedger
	ContextPacks        []model.ContextPack
}

// GenerateMarkdown renders a human-readable Markdown report for a run snapshot.
//
// The report always contains the following sections in order: Summary,
// Timeline, Tool Calls, Errors, Stop Decisions and Suggested Fix. Empty streams
// render an explicit placeholder so the structure stays stable across runs.
func GenerateMarkdown(snapshot RunSnapshot) string {
	var b strings.Builder

	b.WriteString("# ForgeX Run Report\n\n")

	writeSummary(&b, snapshot)
	writeTaskPacket(&b, snapshot)
	writeProgress(&b, snapshot)
	writeContextPacks(&b, snapshot)
	writeTimeline(&b, snapshot)
	writeToolCalls(&b, snapshot)
	writePolicyDecisions(&b, snapshot)
	writeContractValidations(&b, snapshot)
	writeWorldState(&b, snapshot)
	writeArtifacts(&b, snapshot)
	writeErrors(&b, snapshot)
	writeStopDecisions(&b, snapshot)
	writeSuggestedFix(&b, snapshot)

	return b.String()
}

func writeSummary(b *strings.Builder, snapshot RunSnapshot) {
	b.WriteString("## Summary\n\n")

	task := strings.TrimSpace(snapshot.TaskPacket.Goal)
	if task == "" {
		task = strings.TrimSpace(snapshot.TaskPacket.Name)
	}
	if task == "" {
		task = "(unspecified)"
	}

	b.WriteString(fmt.Sprintf("- **Run ID**: %s\n", orPlaceholder(snapshot.Run.ID)))
	b.WriteString(fmt.Sprintf("- **Task**: %s\n", task))
	b.WriteString(fmt.Sprintf("- **Status**: %s\n", orPlaceholder(string(snapshot.Run.Status))))
	b.WriteString(fmt.Sprintf("- **Final Decision**: %s\n", finalDecision(snapshot)))
	b.WriteString("\n")
}

func writeTaskPacket(b *strings.Builder, snapshot RunSnapshot) {
	b.WriteString("## Task Packet\n\n")
	packet := snapshot.TaskPacket
	if packet.ID == "" && packet.Name == "" && packet.Goal == "" {
		b.WriteString("_No task packet recorded._\n\n")
		return
	}
	b.WriteString(fmt.Sprintf("- **ID**: %s\n", orPlaceholder(packet.ID)))
	b.WriteString(fmt.Sprintf("- **Name**: %s\n", orPlaceholder(packet.Name)))
	b.WriteString(fmt.Sprintf("- **Goal**: %s\n", orPlaceholder(packet.Goal)))
	for _, c := range packet.Constraints {
		b.WriteString(fmt.Sprintf("- Constraint: %s\n", c))
	}
	for _, s := range packet.Success {
		b.WriteString(fmt.Sprintf("- Success: %s\n", s))
	}
	b.WriteString("\n")
}

func writeProgress(b *strings.Builder, snapshot RunSnapshot) {
	b.WriteString("## Progress Ledger\n\n")
	if snapshot.ProgressLedger == nil {
		b.WriteString("_No progress ledger recorded._\n\n")
		return
	}
	ledger := snapshot.ProgressLedger
	b.WriteString(fmt.Sprintf("- **Current Phase**: %s\n", orPlaceholder(ledger.CurrentPhase)))
	b.WriteString(fmt.Sprintf("- **Completion**: %.0f%%\n", ledger.CompletionRatio()*100))
	for _, item := range ledger.Checklist {
		b.WriteString(fmt.Sprintf("- [%s] %s", item.Status, item.Title))
		if item.Evidence != "" {
			b.WriteString(fmt.Sprintf(" — %s", item.Evidence))
		}
		b.WriteString("\n")
	}
	for _, blocker := range ledger.Blockers {
		b.WriteString(fmt.Sprintf("- Blocker: %s\n", blocker))
	}
	for _, action := range ledger.NextActions {
		b.WriteString(fmt.Sprintf("- Next: %s\n", action))
	}
	b.WriteString("\n")
}

func writeContextPacks(b *strings.Builder, snapshot RunSnapshot) {
	b.WriteString("## Context Packs\n\n")
	if len(snapshot.ContextPacks) == 0 {
		b.WriteString("_No context packs recorded._\n\n")
		return
	}
	for _, pack := range snapshot.ContextPacks {
		b.WriteString(fmt.Sprintf("- **%s** purpose=%s tokens=%d/%d truncated=%t exceeded=%t\n", orPlaceholder(pack.ID), orPlaceholder(pack.Purpose), pack.EstimatedTokens, pack.BudgetTokens, pack.Truncated, pack.BudgetExceeded))
		if pack.Summary != "" {
			b.WriteString(fmt.Sprintf("  - summary: %s\n", strings.ReplaceAll(pack.Summary, "\n", " ")))
		}
		for _, ref := range pack.ArtifactRefs {
			b.WriteString(fmt.Sprintf("  - artifact: %s\n", ref))
		}
	}
	b.WriteString("\n")
}

func writeTimeline(b *strings.Builder, snapshot RunSnapshot) {
	b.WriteString("## Timeline\n\n")
	if len(snapshot.Events) == 0 {
		b.WriteString("_No events recorded._\n\n")
		return
	}
	for _, e := range snapshot.Events {
		b.WriteString(fmt.Sprintf("- `%s` **%s** - %s\n", formatTime(e.Timestamp), e.Type, e.Message))
	}
	b.WriteString("\n")
}

func writeToolCalls(b *strings.Builder, snapshot RunSnapshot) {
	b.WriteString("## Tool Calls\n\n")
	if len(snapshot.ToolCalls) == 0 {
		b.WriteString("_No tool calls recorded._\n\n")
		return
	}
	for _, c := range snapshot.ToolCalls {
		status := "ok"
		if c.Error != "" {
			status = "failed"
		}
		b.WriteString(fmt.Sprintf("- **%s** (%s)\n", orPlaceholder(c.ToolName), status))
		if len(c.Args) > 0 {
			b.WriteString(fmt.Sprintf("  - args: `%s`\n", compactJSON(c.Args)))
		}
		if len(c.Result) > 0 {
			b.WriteString(fmt.Sprintf("  - result: `%s`\n", compactJSON(c.Result)))
		}
		if c.Error != "" {
			b.WriteString(fmt.Sprintf("  - error: %s\n", c.Error))
		}
	}
	b.WriteString("\n")
}

func writePolicyDecisions(b *strings.Builder, snapshot RunSnapshot) {
	b.WriteString("## Policy Decisions\n\n")
	if len(snapshot.PolicyDecisions) == 0 {
		b.WriteString("_No policy decisions recorded._\n\n")
		return
	}
	for _, d := range snapshot.PolicyDecisions {
		b.WriteString(fmt.Sprintf("- **%s** action=%s authority=%s risk=%s side_effect=%s\n", orPlaceholder(d.ToolName), orPlaceholder(d.Action), orPlaceholder(d.Authority), orPlaceholder(d.RiskLevel), orPlaceholder(d.SideEffect)))
		if d.Reason != "" {
			b.WriteString(fmt.Sprintf("  - reason: %s\n", d.Reason))
		}
		if d.RequiresHITL {
			b.WriteString("  - requires_hitl: true\n")
		}
	}
	b.WriteString("\n")
}

func writeContractValidations(b *strings.Builder, snapshot RunSnapshot) {
	b.WriteString("## Contract Validations\n\n")
	if len(snapshot.ContractValidations) == 0 {
		b.WriteString("_No contract validations recorded._\n\n")
		return
	}
	for _, v := range snapshot.ContractValidations {
		b.WriteString(fmt.Sprintf("- **%s** validator=%s status=%s\n", orPlaceholder(v.ToolName), orPlaceholder(v.Validator), orPlaceholder(v.Status)))
		if v.Message != "" {
			b.WriteString(fmt.Sprintf("  - message: %s\n", v.Message))
		}
	}
	b.WriteString("\n")
}

func writeWorldState(b *strings.Builder, snapshot RunSnapshot) {
	b.WriteString("## World State\n\n")
	if snapshot.WorldState == nil {
		b.WriteString("_No world state recorded._\n\n")
		return
	}
	ws := snapshot.WorldState
	b.WriteString(fmt.Sprintf("- **Version**: %d\n", ws.Version))
	if ws.UpdatedAt.IsZero() {
		b.WriteString("- **Updated At**: --\n")
	} else {
		b.WriteString(fmt.Sprintf("- **Updated At**: %s\n", formatTime(ws.UpdatedAt)))
	}
	for _, entry := range ws.Entries {
		b.WriteString(fmt.Sprintf("- **%s** status=%s version=%d producer=%s\n", orPlaceholder(entry.Key), entry.Status, entry.Version, orPlaceholder(entry.Producer)))
		if len(entry.Value) > 0 {
			b.WriteString(fmt.Sprintf("  - value: `%s`\n", compactJSON(entry.Value)))
		}
		for _, evidence := range entry.Evidence {
			b.WriteString(fmt.Sprintf("  - evidence: %s\n", evidence))
		}
	}
	b.WriteString("\n")
}

func writeArtifacts(b *strings.Builder, snapshot RunSnapshot) {
	b.WriteString("## Artifacts\n\n")
	if len(snapshot.Artifacts) == 0 {
		b.WriteString("_No artifacts recorded._\n\n")
		return
	}
	for _, artifact := range snapshot.Artifacts {
		b.WriteString(fmt.Sprintf("- **%s** type=%s status=%s producer=%s\n", orPlaceholder(artifact.ID), orPlaceholder(artifact.Type), artifact.Status, orPlaceholder(artifact.Producer)))
		if artifact.URI != "" {
			b.WriteString(fmt.Sprintf("  - uri: %s\n", artifact.URI))
		}
		if len(artifact.Metadata) > 0 {
			b.WriteString(fmt.Sprintf("  - metadata: `%s`\n", compactStringJSON(artifact.Metadata)))
		}
	}
	b.WriteString("\n")
}

func writeErrors(b *strings.Builder, snapshot RunSnapshot) {
	b.WriteString("## Errors\n\n")
	if len(snapshot.Errors) == 0 {
		b.WriteString("_No errors recorded._\n\n")
		return
	}
	for _, e := range snapshot.Errors {
		title := strings.TrimSpace(e.Operation)
		if title == "" {
			title = strings.TrimSpace(e.Source)
		}
		b.WriteString(fmt.Sprintf("- **%s**: %s\n", orPlaceholder(title), orPlaceholder(e.Message)))
		b.WriteString(fmt.Sprintf("  - Category: %s\n", orPlaceholder(e.Category)))
		b.WriteString(fmt.Sprintf("  - Severity: %s\n", orPlaceholder(e.Severity)))
		b.WriteString(fmt.Sprintf("  - Retryable: %t\n", e.Retryable))
		if e.Fingerprint != "" {
			b.WriteString(fmt.Sprintf("  - Fingerprint: `%s`\n", e.Fingerprint))
		}
		if rec := recommendation(e); rec != "" {
			b.WriteString(fmt.Sprintf("  - Recommendation: %s\n", rec))
		}
	}
	b.WriteString("\n")
}

func writeStopDecisions(b *strings.Builder, snapshot RunSnapshot) {
	b.WriteString("## Stop Decisions\n\n")
	if len(snapshot.StopDecisions) == 0 {
		b.WriteString("_No stop decisions recorded._\n\n")
		return
	}
	for _, d := range snapshot.StopDecisions {
		b.WriteString(fmt.Sprintf("- **%s** - %s\n", orPlaceholder(string(d.Action)), orPlaceholder(d.Reason)))
		if d.RetryAfter != "" {
			b.WriteString(fmt.Sprintf("  - retry_after: %s\n", d.RetryAfter))
		}
	}
	b.WriteString("\n")
}

func writeSuggestedFix(b *strings.Builder, snapshot RunSnapshot) {
	b.WriteString("## Suggested Fix\n\n")

	// Prefer the recommendation carried on a classified error; fall back to the
	// final stop decision reason so the section is never empty.
	for _, e := range snapshot.Errors {
		if rec := recommendation(e); rec != "" {
			b.WriteString(rec)
			b.WriteString("\n")
			return
		}
	}
	if len(snapshot.StopDecisions) > 0 {
		last := snapshot.StopDecisions[len(snapshot.StopDecisions)-1]
		if strings.TrimSpace(last.Reason) != "" {
			b.WriteString(last.Reason)
			b.WriteString("\n")
			return
		}
	}
	b.WriteString("_No specific fix suggested._\n")
}

// finalDecision returns the action of the last stop decision, or a placeholder
// when no decision was recorded.
func finalDecision(snapshot RunSnapshot) string {
	if len(snapshot.StopDecisions) == 0 {
		return "(none)"
	}
	return string(snapshot.StopDecisions[len(snapshot.StopDecisions)-1].Action)
}

// recommendation extracts the recommendation text attached to a classified
// error by the failure taxonomy.
func recommendation(e model.ErrorEnvelope) string {
	if e.Metadata == nil {
		return ""
	}
	return strings.TrimSpace(e.Metadata["recommendation"])
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return "--"
	}
	return t.UTC().Format(time.RFC3339)
}

func orPlaceholder(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(unset)"
	}
	return s
}

// compactJSON renders a map as deterministic single-line JSON for inline display.
// Keys are sorted so repeated runs produce identical output.
func compactStringJSON(m map[string]string) string {
	converted := make(map[string]any, len(m))
	for k, v := range m {
		converted[k] = v
	}
	return compactJSON(converted)
}

func compactJSON(m map[string]any) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		v, err := json.Marshal(m[k])
		if err != nil {
			v = []byte(`"<unencodable>"`)
		}
		kb, _ := json.Marshal(k)
		parts = append(parts, fmt.Sprintf("%s:%s", kb, v))
	}
	return "{" + strings.Join(parts, ",") + "}"
}
