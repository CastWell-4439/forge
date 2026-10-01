package demo

import "context"

// RunGenericContractSuccessDemo runs the happy-path counterpart to the contract
// violation demo. It returns the generated run ID.
//
// Empty taxonomyPath/policyPath/packetPath fall back to the ForgeX defaults.
func RunGenericContractSuccessDemo(ctx context.Context, root, taxonomyPath, policyPath, packetPath string) (string, error) {
	return RunGenericContractSuccessDemoWithControl(ctx, root, taxonomyPath, policyPath, packetPath, DefaultContractsPath, DefaultToolPolicyPath, DefaultAuthorityLevel)
}

// RunGenericContractSuccessDemoWithControl runs the happy-path case.
//
// The scenario itself lives in RunScenario; this only supplies the packet that
// describes it, so the built-in demos and any case promoted from a bad case share
// one code path.
func RunGenericContractSuccessDemoWithControl(ctx context.Context, root, taxonomyPath, policyPath, packetPath, contractsPath, toolPolicyPath, authorityLevel string) (string, error) {
	if packetPath == "" {
		packetPath = DefaultSuccessPacketPath
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
