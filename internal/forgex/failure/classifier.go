package failure

import (
	"fmt"
	"os"
	"strings"

	"github.com/castwell/forge/internal/forgex/model"
	"gopkg.in/yaml.v3"
)

// Defaults applied when no rule matches an ErrorEnvelope.
const (
	categoryUnknown = "unknown"
	severityMedium  = "medium"
)

// LoadTaxonomy reads and parses a failure taxonomy YAML file.
func LoadTaxonomy(path string) (*Taxonomy, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read taxonomy %s: %w", path, err)
	}

	var taxonomy Taxonomy
	if err := yaml.Unmarshal(data, &taxonomy); err != nil {
		return nil, fmt.Errorf("parse taxonomy %s: %w", path, err)
	}
	return &taxonomy, nil
}

// Classify applies the taxonomy to an ErrorEnvelope and returns a copy with the
// classification (Category/Severity/Retryable), recommendation metadata and a
// stable Fingerprint filled in. The first matching rule wins; when no rule
// matches the envelope is classified as unknown/medium/non-retryable.
func Classify(taxonomy *Taxonomy, envelope model.ErrorEnvelope) model.ErrorEnvelope {
	result := envelope

	if taxonomy != nil {
		for _, rule := range taxonomy.Rules {
			if matchRule(rule.Match, envelope) {
				result.Category = rule.Category
				result.Severity = rule.Severity
				result.Retryable = rule.Retryable
				result.Metadata = setMeta(result.Metadata, map[string]string{
					"rule_id":        rule.ID,
					"source":         rule.Source,
					"recommendation": rule.Recommendation,
				})
				result.Fingerprint = Fingerprint(result)
				return result
			}
		}
	}

	result.Category = categoryUnknown
	result.Severity = severityMedium
	result.Retryable = false
	result.Fingerprint = Fingerprint(result)
	return result
}

// matchRule reports whether the envelope satisfies all of a rule's conditions.
// All matching is case-insensitive.
func matchRule(match RuleMatch, envelope model.ErrorEnvelope) bool {
	message := strings.ToLower(envelope.Message)
	for _, want := range match.MessageContains {
		if want == "" {
			continue
		}
		if !strings.Contains(message, strings.ToLower(want)) {
			return false
		}
	}

	if match.OperationContains != "" {
		if !strings.Contains(strings.ToLower(envelope.Operation), strings.ToLower(match.OperationContains)) {
			return false
		}
	}

	if match.SourceContains != "" {
		if !strings.Contains(strings.ToLower(envelope.Source), strings.ToLower(match.SourceContains)) {
			return false
		}
	}

	return true
}

// setMeta merges entries into a metadata map, allocating it if needed and
// skipping empty values to keep serialized output clean.
func setMeta(meta map[string]string, entries map[string]string) map[string]string {
	for k, v := range entries {
		if v == "" {
			continue
		}
		if meta == nil {
			meta = make(map[string]string)
		}
		meta[k] = v
	}
	return meta
}
