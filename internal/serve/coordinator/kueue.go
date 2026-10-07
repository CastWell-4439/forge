package coordinator

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/castwell/forge/internal/coordinator"
	"github.com/castwell/forge/internal/kubesubmit"
)

// Environment variables that configure the Kueue integration.
//
//	FORGE_KUEUE_ENABLED       1/true to enable (default: off, zero change)
//	FORGE_KUBECONFIG          kubeconfig path (default: in-cluster config)
//	FORGE_KUEUE_NAMESPACE     Job namespace (default forge)
//	FORGE_KUEUE_QUEUE         LocalQueue name (default forge-local-queue)
//	FORGE_KUEUE_POLL_INTERVAL how often job status is polled (default 5s)
const (
	envKueueEnabled      = "FORGE_KUEUE_ENABLED"
	envKubeconfig        = "FORGE_KUBECONFIG"
	envKueueNamespace    = "FORGE_KUEUE_NAMESPACE"
	envKueueQueue        = "FORGE_KUEUE_QUEUE"
	envKueuePollInterval = "FORGE_KUEUE_POLL_INTERVAL"
)

// setupKueue builds the real Kueue submitter and installs the manager,
// returning it so the caller can start the result reconciler.
//
// Disabled (the default) is a no-op returning nil. Enabled but misconfigured
// fails startup — same hard-failure contract as a configured etcd that cannot
// be reached. The client is probed once at construction, so a broken cluster
// surfaces here instead of at the first GPU task.
func setupKueue(coord *coordinator.Coordinator) (*coordinator.KueueManager, error) {
	if !envBool(envKueueEnabled) {
		return nil, nil
	}

	submitter, err := kubesubmit.NewSubmitterFromKubeconfig(os.Getenv(envKubeconfig))
	if err != nil {
		return nil, fmt.Errorf("kueue: %w", err)
	}

	cfg := coordinator.DefaultKueueConfig()
	cfg.Enabled = true
	if v := os.Getenv(envKueueNamespace); v != "" {
		cfg.Namespace = v
	}
	if v := os.Getenv(envKueueQueue); v != "" {
		cfg.QueueName = v
	}

	manager := coordinator.NewKueueManager(cfg, submitter)
	coord.SetKueue(manager)
	log.Printf("INFO: kueue: enabled (namespace=%s queue=%s, kubeconfig=%q)",
		cfg.Namespace, cfg.QueueName, os.Getenv(envKubeconfig))
	return manager, nil
}

// startKueueReconciler starts the async result write-back for GPU tasks.
//
// Submission alone would leave a task RUNNING forever, because the job runs in
// Kubernetes and nothing on a worker ever answers for it — the half-wire #8b
// warned about. The reconciler is leader-only for the same reason workflow
// processing is: every replica runs the loop, and only one should act.
func startKueueReconciler(appCtx context.Context, coord *coordinator.Coordinator, manager *coordinator.KueueManager) {
	if manager == nil {
		return
	}
	interval := defaultKueuePollInterval()
	reconciler := coordinator.NewKueueReconciler(
		coord.Store(),
		manager,
		func(ctx context.Context, taskID string, output []byte) error {
			return coord.OnTaskCompleted(ctx, taskID, output)
		},
		func(ctx context.Context, taskID string, msg string) error {
			return coord.OnTaskFailed(ctx, taskID, msg)
		},
		coord.IsLeader,
		interval,
	)
	go reconciler.Run(appCtx)
	log.Printf("INFO: kueue: result reconciler started (interval=%v, leader-only)", interval)
}

// defaultKueuePollInterval resolves FORGE_KUEUE_POLL_INTERVAL, falling back to
// the package default. An unparseable value warns and uses the default rather
// than disabling the loop: a typo must not silently stop result write-back.
func defaultKueuePollInterval() time.Duration {
	raw := strings.TrimSpace(os.Getenv(envKueuePollInterval))
	if raw == "" {
		return coordinator.DefaultKueuePollInterval()
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		log.Printf("WARN: unknown %s %q (want a duration like 5s); using the default", envKueuePollInterval, raw)
		return coordinator.DefaultKueuePollInterval()
	}
	return d
}
