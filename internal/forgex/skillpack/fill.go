package skillpack

import (
	"context"
)

// Filler drafts the human-facing prose of a distilled pack - the readme and the
// trigger keywords - from the skeleton distillation produced.
//
// It is an injected function rather than a call this package makes: ForgeX's
// control plane keeps its zero-LLM property, and whoever assembles the CLI
// decides whether a model fills the prose or a person writes it during review.
type Filler func(ctx context.Context, draft Pack) (Pack, error)

// OverlayFacts keeps the machine-owned half from the skeleton and takes only the
// prose half from the filled pack.
//
// The split is the whole safety story of "model fills the schema": steps,
// constraints, permissions, eval binding and provenance come from run
// artifacts and stay under version control, while the readme and trigger
// wording may be model-drafted and are then reviewed by a person. A filler that
// returns altered facts simply has them restored here.
func OverlayFacts(skeleton, filled Pack) Pack {
	filled.APIVersion = skeleton.APIVersion
	filled.Kind = skeleton.Kind
	filled.Metadata = skeleton.Metadata
	filled.Spec.Steps = skeleton.Spec.Steps
	filled.Spec.Constraints = skeleton.Spec.Constraints
	filled.Spec.ToolPermissions = skeleton.Spec.ToolPermissions
	filled.Spec.Eval = skeleton.Spec.Eval
	if filled.Spec.Trigger.Description == "" {
		filled.Spec.Trigger.Description = skeleton.Spec.Trigger.Description
	}
	return filled
}
