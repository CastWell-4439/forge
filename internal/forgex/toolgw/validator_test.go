package toolgw

import "testing"

func TestValidateInputsRequiredMissing(t *testing.T) {
	contract := ToolContract{
		Name:           "vidu.reference2video",
		RequiredInputs: []string{"prompt", "images_refs"},
		Validators:     []string{ValidatorRequiredInputsPresent},
	}
	results := ValidateInputs("run-1", contract, map[string]any{"prompt": "make video"})
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[1].Status != ValidationFailed {
		t.Fatalf("expected missing images_refs to fail, got %+v", results[1])
	}
}

func TestValidateInputsImagesRefsEmpty(t *testing.T) {
	contract := ToolContract{Name: "vidu.reference2video", Validators: []string{ValidatorImagesRefsNotEmpty}}
	results := ValidateInputs("run-1", contract, map[string]any{"images_refs": []any{}})
	if len(results) != 1 || results[0].Status != ValidationFailed {
		t.Fatalf("expected failed validation, got %+v", results)
	}
}

func TestValidateInputsPromptEmpty(t *testing.T) {
	contract := ToolContract{Name: "tool", Validators: []string{ValidatorPromptNotEmpty}}
	results := ValidateInputs("run-1", contract, map[string]any{"prompt": "   "})
	if len(results) != 1 || results[0].Status != ValidationFailed {
		t.Fatalf("expected failed prompt validation, got %+v", results)
	}
}

func TestValidateInputsMaterialIDsNotEmpty(t *testing.T) {
	contract := ToolContract{Name: "tool", Validators: []string{ValidatorMaterialIDsNotEmpty}}
	results := ValidateInputs("run-1", contract, map[string]any{"material_ids": []string{"m1"}})
	if len(results) != 1 || results[0].Status != ValidationPassed {
		t.Fatalf("expected passed material_ids validation, got %+v", results)
	}
}

func TestValidateOutputsRequiredMissing(t *testing.T) {
	contract := ToolContract{
		Name:            "tool",
		RequiredOutputs: []string{"video_url"},
		Validators:      []string{ValidatorRequiredOutputsPresent},
	}
	results := ValidateOutputs("run-1", contract, map[string]any{})
	if len(results) != 1 || results[0].Status != ValidationFailed {
		t.Fatalf("expected failed output validation, got %+v", results)
	}
}

func TestValidateInputsValidArgsPassed(t *testing.T) {
	contract := ToolContract{
		Name:           "vidu.reference2video",
		RequiredInputs: []string{"prompt", "images_refs"},
		Validators: []string{
			ValidatorRequiredInputsPresent,
			ValidatorPromptNotEmpty,
			ValidatorImagesRefsNotEmpty,
		},
	}
	results := ValidateInputs("run-1", contract, map[string]any{"prompt": "make video", "images_refs": []any{"img-1"}})
	if len(results) != 4 {
		t.Fatalf("expected 4 results, got %d", len(results))
	}
	for _, result := range results {
		if result.Status != ValidationPassed {
			t.Fatalf("expected all validations to pass, got %+v", results)
		}
	}
}
