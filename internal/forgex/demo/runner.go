package demo

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/castwell/forge/internal/forgex/failure"
	"github.com/castwell/forge/internal/forgex/model"
	forgexpolicy "github.com/castwell/forge/internal/forgex/policy"
	forgexstate "github.com/castwell/forge/internal/forgex/state"
	"github.com/castwell/forge/internal/forgex/stop"
	"github.com/castwell/forge/internal/forgex/storage"
	forgextask "github.com/castwell/forge/internal/forgex/task"
	"github.com/castwell/forge/internal/forgex/toolgw"
)

// Default paths used when the caller does not override them.
const (
	DefaultTaxonomyPath      = "configs/forgex/failure_taxonomy.yaml"
	DefaultPolicyPath        = "configs/forgex/stop_policies.yaml"
	DefaultPacketPath        = "examples/forgex/task_packet_generic_contract_violation.yaml"
	DefaultSuccessPacketPath = "examples/forgex/task_packet_generic_contract_success.yaml"
	DefaultContractsPath     = "configs/forgex/tool_contracts/generic_tool_contracts.yaml"
	DefaultToolPolicyPath    = "configs/forgex/policies/safe_default.yaml"
	DefaultAuthorityLevel    = ""
	// DefaultStateAuthorityPath holds the World State permission rules. The file
	// carries its own enable switch, so the demo always loads it. It is resolved
	// beside whichever taxonomy file the caller supplied (see stateAuthorityFileName),
	// so a caller passing an absolute config directory keeps working.
	DefaultStateAuthorityPath = "configs/forgex/state_authority.yaml"
	stateAuthorityFileName    = "state_authority.yaml"
	defaultExpensiveTool      = "demo.expensive_generation"
)

// demoInputs bundles the configuration a demo run needs. Loading everything up
// front means a config error fails the demo before any run artifacts are
// written.
type demoInputs struct {
	packet         model.TaskPacket
	suitability    forgextask.SuitabilityResult
	authorityLevel string
	taxonomy       *failure.Taxonomy
	stopPolicy     *stop.PolicyConfig
	contract       toolgw.ToolContract
	toolPolicy     *forgexpolicy.Config
	stateAuthority *forgexstate.AuthorityTable
}

// loadDemoInputs reads and validates every config file a demo run depends on.
// The packet path must already be resolved by the caller; the remaining paths
// fall back to the ForgeX defaults when empty.
func loadDemoInputs(taxonomyPath, policyPath, packetPath, contractsPath, toolPolicyPath, authorityLevel string) (demoInputs, error) {
	if taxonomyPath == "" {
		taxonomyPath = DefaultTaxonomyPath
	}
	if policyPath == "" {
		policyPath = DefaultPolicyPath
	}
	if contractsPath == "" {
		contractsPath = DefaultContractsPath
	}
	if toolPolicyPath == "" {
		toolPolicyPath = DefaultToolPolicyPath
	}

	packet, err := forgextask.LoadPacket(packetPath)
	if err != nil {
		return demoInputs{}, err
	}
	taxonomy, err := failure.LoadTaxonomy(taxonomyPath)
	if err != nil {
		return demoInputs{}, err
	}
	stopPolicy, err := stop.LoadPolicy(policyPath)
	if err != nil {
		return demoInputs{}, err
	}
	contracts, err := toolgw.LoadContracts(contractsPath)
	if err != nil {
		return demoInputs{}, err
	}
	contract, err := contracts.MustGet(defaultExpensiveTool)
	if err != nil {
		return demoInputs{}, err
	}
	toolPolicy, err := forgexpolicy.LoadConfig(toolPolicyPath)
	if err != nil {
		return demoInputs{}, err
	}
	stateAuthority, err := forgexstate.LoadAuthority(filepath.Join(filepath.Dir(taxonomyPath), stateAuthorityFileName))
	if err != nil {
		return demoInputs{}, err
	}

	return demoInputs{
		packet:         packet,
		suitability:    forgextask.EvaluatePacket(packet),
		authorityLevel: effectiveAuthorityLevel(authorityLevel, packet),
		taxonomy:       taxonomy,
		stopPolicy:     stopPolicy,
		contract:       contract,
		toolPolicy:     toolPolicy,
		stateAuthority: stateAuthority,
	}, nil
}

// RunGenericContractViolationDemo runs the "empty required_assets" contract
// violation bad case end to end without calling any external API. It reads the
// task packet, creates a run, records a simulated demo.expensive_generation tool
// call, builds an ErrorEnvelope for the empty required_assets failure, classifies
// it with the failure taxonomy, decides on a stop action with the
// StopConditionEngine, and persists the run streams plus a Markdown report and
// bad-case YAML under root/runs/<run_id>/. It returns the generated run ID.
//
// Empty taxonomyPath/policyPath/packetPath fall back to the ForgeX defaults.
func RunGenericContractViolationDemo(ctx context.Context, root, taxonomyPath, policyPath, packetPath string) (string, error) {
	return RunGenericContractViolationDemoWithControl(ctx, root, taxonomyPath, policyPath, packetPath, DefaultContractsPath, DefaultToolPolicyPath, DefaultAuthorityLevel)
}

// RunGenericContractViolationDemoWithControl runs the contract violation case.
//
// The scenario itself lives in RunScenario; this only supplies the packet that
// describes it, so the built-in demos and any case promoted from a bad case share
// one code path.
func RunGenericContractViolationDemoWithControl(ctx context.Context, root, taxonomyPath, policyPath, packetPath, contractsPath, toolPolicyPath, authorityLevel string) (string, error) {
	if packetPath == "" {
		packetPath = DefaultPacketPath
	}
	return RunScenario(ctx, ScenarioConfig{
		Root:           root,
		TaxonomyPath:   taxonomyPath,
		PolicyPath:     policyPath,
		PacketPath:     packetPath,
		ContractsPath:  contractsPath,
		ToolPolicyPath: toolPolicyPath,
		AuthorityLevel: authorityLevel,
	})
}

// persistClaimOutcome records what the claim pipeline decided, so a rejected claim
// leaves the same evidence trail as an accepted one.
func persistClaimOutcome(ctx context.Context, store storage.Store, runID string, outcome forgexstate.SubmitOutcome) error {
	for _, validation := range outcome.Validations {
		if err := store.AppendStateValidation(ctx, validation); err != nil {
			return fmt.Errorf("append state validation: %w", err)
		}
	}
	if outcome.Rejected {
		envelope := outcome.Envelope
		envelope.RunID = runID
		if err := store.AppendError(ctx, envelope); err != nil {
			return fmt.Errorf("append state rejection: %w", err)
		}
	}
	return nil
}

func effectiveAuthorityLevel(override string, packet model.TaskPacket) string {
	override = strings.TrimSpace(override)
	if override != "" {
		return override
	}
	if strings.TrimSpace(packet.Authority) != "" {
		return packet.Authority
	}
	return string(forgexpolicy.AuthorityL0)
}

func toModelPolicyDecision(decision forgexpolicy.Decision) model.PolicyDecision {
	return model.PolicyDecision{
		ID:           decision.ID,
		RunID:        decision.RunID,
		ToolName:     decision.ToolName,
		Action:       string(decision.Action),
		Reason:       decision.Reason,
		RiskLevel:    decision.RiskLevel,
		SideEffect:   decision.SideEffect,
		Authority:    string(decision.Authority),
		RequiresHITL: decision.RequiresHITL,
		CreatedAt:    decision.CreatedAt,
	}
}

func toModelStopSignal(signal stop.StopSignal) model.StopSignalRecord {
	return model.StopSignalRecord{
		ID:        signal.ID,
		RunID:     signal.RunID,
		Source:    string(signal.Source),
		Severity:  string(signal.Severity),
		Suggested: signal.Suggested,
		Reason:    signal.Reason,
		Evidence:  append([]string(nil), signal.Evidence...),
		CreatedAt: signal.CreatedAt,
	}
}

func toModelContractValidation(result toolgw.ValidationResult) model.ContractValidation {
	return model.ContractValidation{
		ID:        result.ID,
		RunID:     result.RunID,
		ToolName:  result.ToolName,
		Status:    string(result.Status),
		Validator: result.Validator,
		Message:   result.Message,
		CreatedAt: result.CreatedAt,
	}
}

func firstValidationFailureMessage(validations []model.ContractValidation, fallback string) string {
	for _, validation := range validations {
		if validation.Status == string(toolgw.ValidationFailed) && validation.Message != "" {
			return validation.Message
		}
	}
	return fallback
}
