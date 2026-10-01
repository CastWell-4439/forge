package main

import (
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/castwell/forge/internal/forgex/model"
	"github.com/castwell/forge/internal/forgex/policy"
	"github.com/castwell/forge/internal/forgex/runtimegate"
	"github.com/castwell/forge/internal/forgex/storage"
	"github.com/castwell/forge/internal/forgex/toolgw"
	"github.com/castwell/forge/internal/worker"
)

// Environment variables for the runtime gate.
//
// The names mirror the coordinator's observer variables (FORGEX_RUNTIME_*) so the
// two halves of ForgeX are configured the same way; FORGEX_GATE_MODE is the only
// addition, because observing and enforcing are different decisions.
const (
	envGateMode           = "FORGEX_GATE_MODE"
	envRuntimeRoot        = "FORGEX_RUNTIME_ROOT"
	envRuntimeAuthority   = "FORGEX_RUNTIME_AUTHORITY"
	envRuntimeContracts   = "FORGEX_RUNTIME_CONTRACTS"
	envRuntimePolicy      = "FORGEX_RUNTIME_POLICY"
	defaultRuntimeRoot    = ".forgex-runtime"
	defaultWorkerContract = "configs/forgex/tool_contracts/worker_handlers.yaml"
)

// runtimeGateOptions holds the resolved gate configuration. Resolving it is kept
// separate from building the gate so both steps stay testable.
type runtimeGateOptions struct {
	Mode          model.GateMode
	Authority     string
	Root          string
	ContractsPath string
	PolicyPath    string
}

// normalizeGateMode maps a configured value onto a gate mode. Anything
// unrecognised returns "", which leaves the gate off: a typo must not silently
// turn enforcement on.
func normalizeGateMode(raw string) model.GateMode {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case string(model.GateModeShadow):
		return model.GateModeShadow
	case string(model.GateModeEnforce):
		return model.GateModeEnforce
	case "off", "disabled":
		return ""
	default:
		return ""
	}
}

// runtimeGateOptionsFromEnv resolves the gate configuration.
//
// The default is shadow: the gate records a decision for every task but never
// blocks one, so an unmodified worker keeps its existing behaviour while still
// producing the audit trail. Enforcement is always an explicit opt-in.
func runtimeGateOptionsFromEnv() (runtimeGateOptions, bool) {
	mode := normalizeGateMode(os.Getenv(envGateMode))
	if os.Getenv(envGateMode) == "" {
		mode = model.GateModeShadow
	}
	if mode == "" {
		return runtimeGateOptions{}, false
	}
	return runtimeGateOptions{
		Mode:          mode,
		Authority:     envOrDefault(envRuntimeAuthority, string(policy.AuthorityL0)),
		Root:          envOrDefault(envRuntimeRoot, defaultRuntimeRoot),
		ContractsPath: os.Getenv(envRuntimeContracts),
		PolicyPath:    os.Getenv(envRuntimePolicy),
	}, true
}

// buildRuntimeGate assembles the gate from resolved options.
//
// A missing contracts file at its default location is tolerated, because that
// only means the caller is not running from the repository root; an explicitly
// configured path that cannot be read is an error, because that is a mistake.
func buildRuntimeGate(opts runtimeGateOptions) (*runtimegate.Gate, error) {
	cfg := runtimegate.Config{
		Mode:      opts.Mode,
		Authority: policy.NormalizeAuthority(policy.AuthorityLevel(opts.Authority)),
		Store:     storage.NewFileStore(opts.Root),
	}

	contractsPath := opts.ContractsPath
	explicitContracts := contractsPath != ""
	if !explicitContracts {
		if _, err := os.Stat(defaultWorkerContract); err == nil {
			contractsPath = defaultWorkerContract
		}
	}
	if contractsPath != "" {
		registry, err := toolgw.LoadContracts(contractsPath)
		if err != nil {
			if explicitContracts {
				return nil, fmt.Errorf("load contracts %s: %w", contractsPath, err)
			}
			log.Printf("WARN: ignoring default contracts %s: %v", contractsPath, err)
		} else {
			contracts := make(map[string]toolgw.ToolContract, len(registry.List()))
			for _, contract := range registry.List() {
				contracts[contract.Name] = contract
			}
			cfg.Contracts = contracts
		}
	}

	if opts.PolicyPath != "" {
		policyCfg, err := policy.LoadConfig(opts.PolicyPath)
		if err != nil {
			return nil, fmt.Errorf("load policy %s: %w", opts.PolicyPath, err)
		}
		cfg.Policy = policy.NewEngine(policyCfg)
	}

	return runtimegate.New(cfg), nil
}

// installRuntimeGate attaches a ForgeX runtime gate to the worker.
//
// Without a gate the worker executes whatever the coordinator sends it. With one,
// every task is evaluated against the tool contracts and the current authority
// level before its handler runs, and the decision is appended to
// gate_decisions.jsonl under the runtime root.
func installRuntimeGate(w *worker.Worker) {
	opts, enabled := runtimeGateOptionsFromEnv()
	if !enabled {
		log.Printf("INFO: ForgeX runtime gate disabled (%s=off)", envGateMode)
		return
	}
	gate, err := buildRuntimeGate(opts)
	if err != nil {
		log.Fatalf("FATAL: build ForgeX runtime gate: %v", err)
	}
	w.WithRuntimeGate(gate)
	log.Printf("INFO: ForgeX runtime gate enabled (mode=%s authority=%s root=%s)",
		opts.Mode, policy.NormalizeAuthority(policy.AuthorityLevel(opts.Authority)), opts.Root)
}
