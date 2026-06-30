package toolgw

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Registry stores validated tool contracts by tool name.
type Registry struct {
	contracts map[string]ToolContract
	order     []string
}

// LoadContracts reads a contract YAML file and returns a validated registry.
func LoadContracts(path string) (*Registry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read tool contracts %s: %w", path, err)
	}

	var cfg ContractConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse tool contracts %s: %w", path, err)
	}
	if cfg.Version <= 0 {
		return nil, fmt.Errorf("tool contracts %s: version is required", path)
	}
	return NewRegistry(cfg.Tools)
}

// NewRegistry validates contracts and indexes them by name.
func NewRegistry(contracts []ToolContract) (*Registry, error) {
	r := &Registry{contracts: make(map[string]ToolContract, len(contracts))}
	for i, c := range contracts {
		if err := validateContract(c); err != nil {
			return nil, fmt.Errorf("contract[%d]: %w", i, err)
		}
		name := strings.TrimSpace(c.Name)
		if _, exists := r.contracts[name]; exists {
			return nil, fmt.Errorf("duplicate tool contract %q", name)
		}
		c.Name = name
		c.Capability = strings.TrimSpace(c.Capability)
		r.contracts[name] = c
		r.order = append(r.order, name)
	}
	sort.Strings(r.order)
	return r, nil
}

// Get returns a contract by name.
func (r *Registry) Get(name string) (ToolContract, bool) {
	if r == nil {
		return ToolContract{}, false
	}
	c, ok := r.contracts[strings.TrimSpace(name)]
	return c, ok
}

// MustGet returns a contract or a descriptive error when missing.
func (r *Registry) MustGet(name string) (ToolContract, error) {
	c, ok := r.Get(name)
	if !ok {
		return ToolContract{}, fmt.Errorf("tool contract %q not found", name)
	}
	return c, nil
}

// List returns contracts in deterministic name order.
func (r *Registry) List() []ToolContract {
	if r == nil {
		return nil
	}
	out := make([]ToolContract, 0, len(r.order))
	for _, name := range r.order {
		out = append(out, r.contracts[name])
	}
	return out
}

func validateContract(c ToolContract) error {
	if strings.TrimSpace(c.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if strings.TrimSpace(c.Capability) == "" {
		return fmt.Errorf("capability is required")
	}
	if c.RiskLevel == "" {
		return fmt.Errorf("risk_level is required")
	}
	if !c.RiskLevel.Valid() {
		return fmt.Errorf("unknown risk_level %q", c.RiskLevel)
	}
	if c.SideEffect == "" {
		return fmt.Errorf("side_effect is required")
	}
	if !c.SideEffect.Valid() {
		return fmt.Errorf("unknown side_effect %q", c.SideEffect)
	}
	return nil
}
