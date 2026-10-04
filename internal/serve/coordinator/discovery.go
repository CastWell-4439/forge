package coordinator

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/castwell/forge/internal/coordinator"
	"github.com/castwell/forge/internal/discovery"
)

// Environment variables that configure etcd-based coordination.
//
//	FORGE_ETCD_EMBED=1       start an embedded etcd inside this coordinator
//	                         (the README's "单机可用嵌入模式")
//	FORGE_ETCD_ENDPOINTS     comma-separated etcd endpoints; connects to an
//	                         existing cluster instead of embedding one
//	FORGE_ETCD_CLIENT_ADDR   embedded etcd client listen URL (default http://127.0.0.1:2379)
//	FORGE_ETCD_PEER_ADDR     embedded etcd peer listen URL     (default http://127.0.0.1:2380)
//	FORGE_ETCD_DATA_DIR      embedded etcd data dir            (default .forge-etcd)
//	FORGE_COORD_ID           this coordinator's identity       (default coordinator-1)
//
// Neither variable set means standalone mode: direct worker registration,
// always-leader — exactly the behaviour before this wiring existed.
const (
	envEtcdEmbed       = "FORGE_ETCD_EMBED"
	envEtcdEndpoints   = "FORGE_ETCD_ENDPOINTS"
	envEtcdClientAddr  = "FORGE_ETCD_CLIENT_ADDR"
	envEtcdPeerAddr    = "FORGE_ETCD_PEER_ADDR"
	envEtcdDataDir     = "FORGE_ETCD_DATA_DIR"
	envCoordID         = "FORGE_COORD_ID"
	defaultCoordID     = "coordinator-1"
	defaultEtcdClient  = "http://127.0.0.1:2379"
	defaultEtcdPeer    = "http://127.0.0.1:2380"
	defaultEtcdDataDir = ".forge-etcd"
)

// setupDiscovery wires etcd coordination when configured and returns a
// cleanup that the caller must run at shutdown. The distributed assembly is
// the same in both modes:
//
//	SetDiscovery + StartLeaderElection  → leader gate (standalone = always leader)
//	NewWorkerManager + SetWorkerManager → discovery-aware dispatch + dead-worker rescheduling
//	WatchWorkers + RunFailureDetector   → discovered workers appear, dead ones get rescheduled
//	SetHeartbeatStore (hb != nil)       → durable heartbeat snapshots (NATS KV)
//
// With neither variable set it does nothing and returns a no-op cleanup, so
// an unconfigured coordinator keeps its previous behaviour exactly.
//
// The heartbeat store attaches here because WorkerManager is the only source
// of periodic heartbeat events; standalone mode has no heartbeat stream to
// persist (the two modes' liveness difference is documented in D-17).
func setupDiscovery(ctx context.Context, coord *coordinator.Coordinator, hb coordinator.HeartbeatStore) (func(), error) {
	nodeID := envOrDefault(envCoordID, defaultCoordID)

	var (
		d      discovery.Discovery
		origin string
	)
	switch {
	case envBool(envEtcdEmbed):
		clientAddr := envOrDefault(envEtcdClientAddr, defaultEtcdClient)
		peerAddr := envOrDefault(envEtcdPeerAddr, defaultEtcdPeer)
		embedded := discovery.NewEtcdDiscovery(discovery.EtcdConfig{
			Name:       nodeID,
			DataDir:    envOrDefault(envEtcdDataDir, defaultEtcdDataDir),
			ClientAddr: clientAddr,
			PeerAddr:   peerAddr,
		})
		if err := embedded.Start(); err != nil {
			return nil, fmt.Errorf("start embedded etcd: %w", err)
		}
		d, origin = embedded, "embedded "+clientAddr
	case strings.TrimSpace(os.Getenv(envEtcdEndpoints)) != "":
		endpoints := splitAndTrim(os.Getenv(envEtcdEndpoints))
		client, err := discovery.NewEtcdClient(endpoints, nodeID)
		if err != nil {
			return nil, err
		}
		d, origin = client, strings.Join(endpoints, ",")
	default:
		return func() {}, nil // standalone: nothing to wire, nothing to close
	}

	coord.SetDiscovery(d, nodeID)
	if err := coord.StartLeaderElection(ctx); err != nil {
		_ = closeDiscovery(d)
		return nil, fmt.Errorf("start leader election: %w", err)
	}

	wm := coordinator.NewWorkerManager(d)
	coord.SetWorkerManager(wm)
	if hb != nil {
		wm.SetHeartbeatStore(hb)
	}
	go func() {
		if err := wm.WatchWorkers(ctx); err != nil && ctx.Err() == nil {
			log.Printf("ERROR: worker watch stopped: %v", err)
		}
	}()
	go wm.RunFailureDetector(ctx)

	log.Printf("INFO: etcd discovery enabled (etcd=%s node=%s mode=distributed)", origin, nodeID)
	return func() { _ = closeDiscovery(d) }, nil
}

// closeDiscovery narrows the cleanup to whatever concrete type was built;
// both EtcdDiscovery and any future implementation only need Close.
func closeDiscovery(d discovery.Discovery) error {
	type closer interface{ Close() error }
	if c, ok := d.(closer); ok {
		return c.Close()
	}
	return nil
}

// splitAndTrim splits a comma-separated env value into non-empty parts.
func splitAndTrim(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}
