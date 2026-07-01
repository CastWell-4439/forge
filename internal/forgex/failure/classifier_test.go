package failure

import (
	"path/filepath"
	"testing"

	"github.com/castwell/forge/internal/forgex/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// taxonomyPath points at the repository's real taxonomy config relative to this
// package directory (internal/forgex/failure -> repo root).
var taxonomyPath = filepath.Join("..", "..", "..", "configs", "forgex", "failure_taxonomy.yaml")

func loadTestTaxonomy(t *testing.T) *Taxonomy {
	t.Helper()
	tax, err := LoadTaxonomy(taxonomyPath)
	require.NoError(t, err)
	require.NotNil(t, tax)
	require.NotEmpty(t, tax.Rules)
	return tax
}

func TestClassifyEmptyRequiredAssets(t *testing.T) {
	tax := loadTestTaxonomy(t)

	env := model.ErrorEnvelope{
		ID:        "err-1",
		RunID:     "run-1",
		Source:    "tool",
		Operation: "demo.expensive_generation",
		Message:   "required_assets is empty",
	}

	got := Classify(tax, env)

	assert.Equal(t, "GENERIC_REQUIRED_ASSETS_EMPTY", got.Metadata["rule_id"])
	assert.Equal(t, "tool_contract_violation", got.Category)
	assert.Equal(t, "high", got.Severity)
	assert.False(t, got.Retryable)
	assert.Equal(t, "demo", got.Metadata["source"])
	assert.NotEmpty(t, got.Metadata["recommendation"])
	assert.NotEmpty(t, got.Fingerprint)
}

func TestClassifyEmptyRequiredAssetsCaseInsensitive(t *testing.T) {
	tax := loadTestTaxonomy(t)

	env := model.ErrorEnvelope{
		Operation: "DEMO.Expensive_Generation",
		Message:   "Required_Assets is EMPTY",
	}

	got := Classify(tax, env)
	assert.Equal(t, "GENERIC_REQUIRED_ASSETS_EMPTY", got.Metadata["rule_id"])
	assert.Equal(t, "tool_contract_violation", got.Category)
}

func TestClassifyTimeout(t *testing.T) {
	tax := loadTestTaxonomy(t)

	env := model.ErrorEnvelope{
		Operation: "tool.fetch",
		Message:   "request timeout after 30s",
	}

	got := Classify(tax, env)
	assert.Equal(t, "TOOL_TIMEOUT", got.Metadata["rule_id"])
	assert.Equal(t, "transient_timeout", got.Category)
	assert.Equal(t, "medium", got.Severity)
	assert.True(t, got.Retryable)
	assert.NotEmpty(t, got.Fingerprint)
}

func TestClassifyUnknown(t *testing.T) {
	tax := loadTestTaxonomy(t)

	env := model.ErrorEnvelope{
		Operation: "tool.fetch",
		Message:   "something completely unexpected happened",
	}

	got := Classify(tax, env)
	assert.Equal(t, "unknown", got.Category)
	assert.Equal(t, "medium", got.Severity)
	assert.False(t, got.Retryable)
	assert.Empty(t, got.Metadata["rule_id"])
	assert.NotEmpty(t, got.Fingerprint)
}

// required_assets without a matching operation must not match the
// operation-scoped rule.
func TestClassifyOperationGate(t *testing.T) {
	tax := loadTestTaxonomy(t)

	env := model.ErrorEnvelope{
		Operation: "some.other.tool",
		Message:   "required_assets is empty",
	}

	got := Classify(tax, env)
	assert.Equal(t, "unknown", got.Category)
}

// Same logical failure with different ids/numbers/uuids yields one fingerprint.
func TestFingerprintStableAcrossVolatileTokens(t *testing.T) {
	tax := loadTestTaxonomy(t)

	a := model.ErrorEnvelope{
		Source:    "tool",
		Operation: "demo.expensive_generation",
		Message:   "required_assets is empty for request 121503 batch 493e80ecf0ec4503853429161b285500",
	}
	b := model.ErrorEnvelope{
		Source:    "tool",
		Operation: "demo.expensive_generation",
		Message:   "required_assets is empty for request 999888 batch a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6",
	}

	ga := Classify(tax, a)
	gb := Classify(tax, b)

	require.NotEmpty(t, ga.Fingerprint)
	assert.Equal(t, ga.Fingerprint, gb.Fingerprint)
}

func TestLoadTaxonomyMissingFile(t *testing.T) {
	_, err := LoadTaxonomy(filepath.Join(t.TempDir(), "does_not_exist.yaml"))
	assert.Error(t, err)
}
