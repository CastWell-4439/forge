package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/castwell/forge/internal/discovery"
)

// FORGE_ETCD_ENDPOINTS points the worker at the etcd cluster (or the
// coordinator's embedded etcd) where it registers itself so the
// coordinator's discovery-aware WorkerManager can find it. Unset means
// direct registration only — the behaviour before this wiring existed.
const envEtcdEndpoints = "FORGE_ETCD_ENDPOINTS"

// etcdDiscoveryFromEnv builds the discovery backend from the environment.
// A configured-but-unusable etcd is an error, not a silent fallback: in
// distributed mode the coordinator dispatches through discovered workers
// only, so a worker that skipped registration would be invisible.
func etcdDiscoveryFromEnv(workerID string) (discovery.Discovery, error) {
	raw := strings.TrimSpace(os.Getenv(envEtcdEndpoints))
	if raw == "" {
		return nil, nil
	}
	var endpoints []string
	for _, part := range strings.Split(raw, ",") {
		if p := strings.TrimSpace(part); p != "" {
			endpoints = append(endpoints, p)
		}
	}
	if len(endpoints) == 0 {
		return nil, fmt.Errorf("%s is set but contains no endpoints", envEtcdEndpoints)
	}
	return discovery.NewEtcdClient(endpoints, workerID)
}
