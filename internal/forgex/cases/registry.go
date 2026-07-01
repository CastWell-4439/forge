package cases

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Registry is the top-level case registry configuration.
type Registry struct {
	Version int        `yaml:"version" json:"version"`
	Cases   []CaseSpec `yaml:"cases" json:"cases"`
}

// CaseSpec describes one registered case: the task packet to run, the eval
// suite that scores it, and the outcome it is expected to produce.
type CaseSpec struct {
	ID          string          `yaml:"id" json:"id"`
	Description string          `yaml:"description,omitempty" json:"description,omitempty"`
	TaskPacket  string          `yaml:"task_packet" json:"task_packet"`
	Suite       string          `yaml:"suite" json:"suite"`
	Expected    ExpectedOutcome `yaml:"expected" json:"expected"`
}

// ExpectedOutcome is the golden outcome for a case. Exact-count fields and
// their *_min counterparts are optional; a nil pointer means "unspecified".
type ExpectedOutcome struct {
	Status              string `yaml:"status,omitempty" json:"status,omitempty"`
	FinalDecision       string `yaml:"final_decision,omitempty" json:"final_decision,omitempty"`
	Errors              *int   `yaml:"errors,omitempty" json:"errors,omitempty"`
	Lessons             *int   `yaml:"lessons,omitempty" json:"lessons,omitempty"`
	LessonsMin          *int   `yaml:"lessons_min,omitempty" json:"lessons_min,omitempty"`
	ValidationFailed    *int   `yaml:"validation_failed,omitempty" json:"validation_failed,omitempty"`
	ValidationFailedMin *int   `yaml:"validation_failed_min,omitempty" json:"validation_failed_min,omitempty"`
	ArtifactsMissing    *int   `yaml:"artifacts_missing,omitempty" json:"artifacts_missing,omitempty"`
	ArtifactsMissingMin *int   `yaml:"artifacts_missing_min,omitempty" json:"artifacts_missing_min,omitempty"`
}

// Load reads and validates a case registry from the YAML file at path.
func Load(path string) (*Registry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read case registry %s: %w", path, err)
	}
	var reg Registry
	if err := yaml.Unmarshal(data, &reg); err != nil {
		return nil, fmt.Errorf("parse case registry %s: %w", path, err)
	}
	if err := reg.Validate(); err != nil {
		return nil, fmt.Errorf("validate case registry %s: %w", path, err)
	}
	return &reg, nil
}

// Validate checks the registry for structural errors: version presence,
// non-empty required fields, and unique case IDs.
func (r *Registry) Validate() error {
	if r == nil {
		return fmt.Errorf("registry is nil")
	}
	if r.Version == 0 {
		return fmt.Errorf("case registry version is required")
	}
	if len(r.Cases) == 0 {
		return fmt.Errorf("case registry has no cases")
	}
	seen := make(map[string]struct{}, len(r.Cases))
	for i := range r.Cases {
		c := &r.Cases[i]
		if c.ID == "" {
			return fmt.Errorf("case[%d]: id is required", i)
		}
		if _, dup := seen[c.ID]; dup {
			return fmt.Errorf("duplicate case id: %s", c.ID)
		}
		seen[c.ID] = struct{}{}
		if c.TaskPacket == "" {
			return fmt.Errorf("case %s: task_packet is required", c.ID)
		}
		if c.Suite == "" {
			return fmt.Errorf("case %s: suite is required", c.ID)
		}
	}
	return nil
}

// IDs returns the case IDs in registry order.
func (r *Registry) IDs() []string {
	if r == nil {
		return nil
	}
	ids := make([]string, 0, len(r.Cases))
	for _, c := range r.Cases {
		ids = append(ids, c.ID)
	}
	return ids
}

// Find returns the case with the requested id.
func (r *Registry) Find(id string) (CaseSpec, error) {
	if r == nil {
		return CaseSpec{}, fmt.Errorf("registry is nil")
	}
	for _, c := range r.Cases {
		if c.ID == id {
			return c, nil
		}
	}
	return CaseSpec{}, fmt.Errorf("case not found: %s", id)
}
