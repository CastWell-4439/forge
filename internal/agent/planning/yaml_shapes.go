package planning

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// The YAML shapes a generated DAG is built from.
//
// They are structs marshalled by the YAML library rather than strings assembled
// with Sprintf. The difference matters at the edges: a requirement description
// containing a colon, a quote or a newline would break a formatted string, and
// these values come from free text. Marshalling escapes them.
type dagYAML struct {
	Name  string              `yaml:"name"`
	Tasks map[string]taskYAML `yaml:"tasks"`
}

type taskYAML struct {
	Handler   string                 `yaml:"handler"`
	Params    map[string]interface{} `yaml:"params,omitempty"`
	DependsOn []string               `yaml:"depends_on,omitempty"`
	Timeout   string                 `yaml:"timeout,omitempty"`
	Retry     *retryYAML             `yaml:"retry,omitempty"`
}

type retryYAML struct {
	MaxAttempts     int    `yaml:"max_attempts"`
	Backoff         string `yaml:"backoff"`
	InitialInterval string `yaml:"initial_interval"`
}

// marshalDAG renders a DAG to YAML.
//
// Task order in the output is the map's iteration order, which Go randomises —
// so two runs of the same input produce different-looking YAML. That is
// tolerable because the YAML is parsed rather than diffed, but it is worth
// knowing when a test compares text.
func marshalDAG(dag dagYAML) (string, error) {
	out, err := yaml.Marshal(dag)
	if err != nil {
		return "", fmt.Errorf("marshal DAG: %w", err)
	}
	return string(out), nil
}
