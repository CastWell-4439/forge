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
	Run           model.Run
	TaskPacket    model.TaskPacket
	Events        []model.Event
	ToolCalls     []model.ToolCall
	Errors        []model.ErrorEnvelope
	StopDecisions []model.StopDecision
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
	writeTimeline(&b, snapshot)
	writeToolCalls(&b, snapshot)
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
