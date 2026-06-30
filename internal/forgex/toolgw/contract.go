package toolgw

// RiskLevel describes the risk class of a tool invocation.
type RiskLevel string

const (
	RiskLow      RiskLevel = "low"
	RiskMedium   RiskLevel = "medium"
	RiskHigh     RiskLevel = "high"
	RiskCritical RiskLevel = "critical"
)

// SideEffect describes what kind of side effect a tool may produce.
type SideEffect string

const (
	SideEffectNone            SideEffect = "none"
	SideEffectReadOnly        SideEffect = "read_only"
	SideEffectLocalWrite      SideEffect = "local_write"
	SideEffectExternalAPICall SideEffect = "external_api_call"
	SideEffectExternalWrite   SideEffect = "external_write"
	SideEffectPaidCall        SideEffect = "paid_call"
)

// ToolContract declares the control-plane contract for a tool.
type ToolContract struct {
	Name                   string            `json:"name" yaml:"name"`
	Capability             string            `json:"capability" yaml:"capability"`
	Description            string            `json:"description,omitempty" yaml:"description,omitempty"`
	RequiredInputs         []string          `json:"required_inputs,omitempty" yaml:"required_inputs,omitempty"`
	RequiredOutputs        []string          `json:"required_outputs,omitempty" yaml:"required_outputs,omitempty"`
	Validators             []string          `json:"validators,omitempty" yaml:"validators,omitempty"`
	RiskLevel              RiskLevel         `json:"risk_level" yaml:"risk_level"`
	SideEffect             SideEffect        `json:"side_effect" yaml:"side_effect"`
	Idempotent             bool              `json:"idempotent" yaml:"idempotent"`
	TimeoutSeconds         int               `json:"timeout_seconds,omitempty" yaml:"timeout_seconds,omitempty"`
	RetryPolicy            string            `json:"retry_policy,omitempty" yaml:"retry_policy,omitempty"`
	RequiredAuthorityLevel string            `json:"required_authority_level,omitempty" yaml:"required_authority_level,omitempty"`
	ApprovalRequired       bool              `json:"approval_required" yaml:"approval_required"`
	Metadata               map[string]string `json:"metadata,omitempty" yaml:"metadata,omitempty"`
}

// ContractConfig is the top-level YAML file for tool contracts.
type ContractConfig struct {
	Version int            `json:"version" yaml:"version"`
	Tools   []ToolContract `json:"tools" yaml:"tools"`
}

// Valid reports whether the risk level is known.
func (r RiskLevel) Valid() bool {
	switch r {
	case RiskLow, RiskMedium, RiskHigh, RiskCritical:
		return true
	default:
		return false
	}
}

// Valid reports whether the side effect is known.
func (s SideEffect) Valid() bool {
	switch s {
	case SideEffectNone, SideEffectReadOnly, SideEffectLocalWrite, SideEffectExternalAPICall, SideEffectExternalWrite, SideEffectPaidCall:
		return true
	default:
		return false
	}
}
