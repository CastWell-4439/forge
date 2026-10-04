package coordinator

import (
	"fmt"
	"log"
	"os"

	"github.com/castwell/forge/internal/coordinator"
	"github.com/castwell/forge/internal/kubesubmit"
)

// Environment variables that configure the Kueue integration.
//
//	FORGE_KUEUE_ENABLED    1/true to enable (default: off, zero change)
//	FORGE_KUBECONFIG       kubeconfig path (default: in-cluster config)
//	FORGE_KUEUE_NAMESPACE  Job namespace (default forge)
//	FORGE_KUEUE_QUEUE      LocalQueue name (default forge-local-queue)
const (
	envKueueEnabled   = "FORGE_KUEUE_ENABLED"
	envKubeconfig     = "FORGE_KUBECONFIG"
	envKueueNamespace = "FORGE_KUEUE_NAMESPACE"
	envKueueQueue     = "FORGE_KUEUE_QUEUE"
)

// setupKueue builds the real Kueue submitter and installs the manager.
// Disabled (the default) is a no-op; enabled but misconfigured fails
// startup — same hard-failure contract as a configured etcd that cannot be
// reached. The client is probed once at construction, so a broken cluster
// surfaces here instead of at the first GPU task.
//
// Dispatch routing and result write-back are NOT wired here (tracked as
// #8b): what this gate buys today is fail-fast config validation and an
// assembled, tested component behind a single env switch.
func setupKueue(coord *coordinator.Coordinator) error {
	if !envBool(envKueueEnabled) {
		return nil
	}

	submitter, err := kubesubmit.NewSubmitterFromKubeconfig(os.Getenv(envKubeconfig))
	if err != nil {
		return fmt.Errorf("kueue: %w", err)
	}

	cfg := coordinator.DefaultKueueConfig()
	cfg.Enabled = true
	if v := os.Getenv(envKueueNamespace); v != "" {
		cfg.Namespace = v
	}
	if v := os.Getenv(envKueueQueue); v != "" {
		cfg.QueueName = v
	}

	coord.SetKueue(coordinator.NewKueueManager(cfg, submitter))
	log.Printf("INFO: kueue: enabled (namespace=%s queue=%s, kubeconfig=%q)",
		cfg.Namespace, cfg.QueueName, os.Getenv(envKubeconfig))
	return nil
}
