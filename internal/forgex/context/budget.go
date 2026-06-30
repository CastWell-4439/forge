package context

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/castwell/forge/internal/forgex/model"
	"github.com/google/uuid"
)

// BudgetManager builds compact context packs and marks budget pressure.
type BudgetManager struct {
	BudgetTokens    int
	MaxSummaryRunes int
}

// NewBudgetManager returns a lightweight character-based budget manager.
func NewBudgetManager(budgetTokens int) BudgetManager {
	if budgetTokens <= 0 {
		budgetTokens = 2048
	}
	return BudgetManager{BudgetTokens: budgetTokens, MaxSummaryRunes: budgetTokens * 4}
}

// Build creates a ContextPack from raw context text and artifact references.
func (m BudgetManager) Build(runID string, purpose string, raw string, artifactRefs []string) model.ContextPack {
	estimated := EstimateTokens(raw)
	maxRunes := m.MaxSummaryRunes
	if maxRunes <= 0 {
		maxRunes = m.BudgetTokens * 4
	}
	summary, truncated := truncateRunes(strings.TrimSpace(raw), maxRunes)
	pack := model.ContextPack{
		ID:                 "ctx_" + uuid.NewString(),
		RunID:              runID,
		Purpose:            purpose,
		Summary:            summary,
		ArtifactRefs:       append([]string(nil), artifactRefs...),
		IncludedBytes:      len([]byte(raw)),
		EstimatedTokens:    estimated,
		BudgetTokens:       m.BudgetTokens,
		Truncated:          truncated,
		BudgetExceeded:     estimated > m.BudgetTokens,
		CompactionStrategy: "char_budget_v1",
		CreatedAt:          time.Now().UTC(),
	}
	if pack.BudgetExceeded {
		pack.Metadata = map[string]string{"warning": "context_budget_exceeded"}
	}
	return pack
}

// EstimateTokens approximates token usage with a conservative 4 chars/token rule.
func EstimateTokens(text string) int {
	runes := utf8.RuneCountInString(text)
	if runes == 0 {
		return 0
	}
	return (runes + 3) / 4
}

func truncateRunes(text string, maxRunes int) (string, bool) {
	if maxRunes <= 0 || utf8.RuneCountInString(text) <= maxRunes {
		return text, false
	}
	runes := []rune(text)
	if maxRunes > len("\n...[truncated]") {
		return string(runes[:maxRunes-len("\n...[truncated]")]) + "\n...[truncated]", true
	}
	return string(runes[:maxRunes]), true
}
