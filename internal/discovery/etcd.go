package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/concurrency"
	"go.etcd.io/etcd/server/v3/embed"
)

// EtcdConfig holds configuration for the embedded etcd server.
type EtcdConfig struct {
	// DataDir is the directory for etcd data. If empty, a temporary directory is used.
	DataDir string
	// ClientAddr is the etcd client listen address (e.g., "http://localhost:2379").
	ClientAddr string
	// PeerAddr is the etcd peer listen address (e.g., "http://localhost:2380").
	PeerAddr string
	// Name is the human-readable name for this etcd member.
	Name string
}

// EtcdDiscovery implements the Discovery interface using embedded etcd.
type EtcdDiscovery struct {
	server *embed.Etcd
	client *clientv3.Client
	config EtcdConfig
}

// NewEtcdDiscovery creates a new EtcdDiscovery with the given configuration.
func NewEtcdDiscovery(cfg EtcdConfig) *EtcdDiscovery {
	return &EtcdDiscovery{config: cfg}
}

// Start starts the embedded etcd server and creates a client.
func (d *EtcdDiscovery) Start() error {
	cfg := embed.NewConfig()

	if d.config.Name != "" {
		cfg.Name = d.config.Name
	}

	if d.config.DataDir != "" {
		cfg.Dir = d.config.DataDir
	} else {
		dir, err := os.MkdirTemp("", "forge-etcd-*")
		if err != nil {
			return fmt.Errorf("create temp dir for etcd: %w", err)
		}
		cfg.Dir = dir
	}

	clientAddr := d.config.ClientAddr
	if clientAddr == "" {
		clientAddr = "http://localhost:2379"
	}
	peerAddr := d.config.PeerAddr
	if peerAddr == "" {
		peerAddr = "http://localhost:2380"
	}

	lcURL, err := url.Parse(clientAddr)
	if err != nil {
		return fmt.Errorf("parse client addr %s: %w", clientAddr, err)
	}
	lpURL, err := url.Parse(peerAddr)
	if err != nil {
		return fmt.Errorf("parse peer addr %s: %w", peerAddr, err)
	}

	cfg.ListenClientUrls = []url.URL{*lcURL}
	cfg.AdvertiseClientUrls = []url.URL{*lcURL}
	cfg.ListenPeerUrls = []url.URL{*lpURL}
	cfg.AdvertisePeerUrls = []url.URL{*lpURL}
	cfg.InitialCluster = cfg.Name + "=" + peerAddr
	cfg.ClusterState = embed.ClusterStateFlagNew
	cfg.LogLevel = "error"

	// Disable WAL and snapshot fsync for dev/test performance
	cfg.Dir = filepath.Clean(cfg.Dir)

	server, err := embed.StartEtcd(cfg)
	if err != nil {
		return fmt.Errorf("start embedded etcd: %w", err)
	}

	// Wait for etcd to be ready
	select {
	case <-server.Server.ReadyNotify():
	case <-time.After(30 * time.Second):
		server.Close()
		return fmt.Errorf("embedded etcd took too long to start")
	}

	d.server = server

	client, err := clientv3.New(clientv3.Config{
		Endpoints:   []string{clientAddr},
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		server.Close()
		return fmt.Errorf("create etcd client: %w", err)
	}
	d.client = client

	return nil
}

// Close shuts down the embedded etcd server and client.
func (d *EtcdDiscovery) Close() error {
	if d.client != nil {
		d.client.Close()
	}
	if d.server != nil {
		d.server.Close()
		// Clean up temp data directory if auto-created
		if d.config.DataDir == "" && d.server != nil {
			os.RemoveAll(d.server.Config().Dir)
		}
	}
	return nil
}

// Client returns the underlying etcd client (for use by coordinator leader election).
func (d *EtcdDiscovery) Client() *clientv3.Client {
	return d.client
}

// LeaderElect starts leader election and returns a channel that signals leadership status.
func (d *EtcdDiscovery) LeaderElect(ctx context.Context) (<-chan bool, error) {
	session, err := concurrency.NewSession(d.client, concurrency.WithTTL(10))
	if err != nil {
		return nil, fmt.Errorf("create etcd session: %w", err)
	}

	election := concurrency.NewElection(session, "forge/leader")
	ch := make(chan bool, 1)

	go func() {
		defer close(ch)
		defer session.Close()

		// Campaign blocks until this node becomes the leader or ctx is cancelled.
		if err := election.Campaign(ctx, d.config.Name); err != nil {
			return
		}

		// Signal that we are the leader.
		ch <- true

		// Watch for session expiration (leadership loss).
		select {
		case <-ctx.Done():
			// Context cancelled, resign leadership.
			election.Resign(context.Background())
		case <-session.Done():
			// Session expired, lost leadership.
			ch <- false
		}
	}()

	return ch, nil
}

// Register registers a node in etcd under the forge/nodes/ prefix.
func (d *EtcdDiscovery) Register(ctx context.Context, node NodeInfo) error {
	data, err := json.Marshal(node)
	if err != nil {
		return fmt.Errorf("marshal node info: %w", err)
	}

	// Create a session with a TTL for auto-expiry on disconnect.
	session, err := concurrency.NewSession(d.client, concurrency.WithTTL(15))
	if err != nil {
		return fmt.Errorf("create registration session: %w", err)
	}

	key := node.ID
	_, err = d.client.Put(ctx, key, string(data), clientv3.WithLease(session.Lease()))
	if err != nil {
		session.Close()
		return fmt.Errorf("register node %s: %w", node.ID, err)
	}

	// Keep session alive in background; close when ctx is done.
	go func() {
		select {
		case <-ctx.Done():
			session.Close()
		case <-session.Done():
		}
	}()

	return nil
}

// Watch observes changes under the given key prefix and sends events.
func (d *EtcdDiscovery) Watch(ctx context.Context, prefix string) (<-chan Event, error) {
	ch := make(chan Event, 64)

	// First, list existing keys to send initial EventAdd events.
	resp, err := d.client.Get(ctx, prefix, clientv3.WithPrefix())
	if err != nil {
		return nil, fmt.Errorf("list keys under %s: %w", prefix, err)
	}

	var revision int64
	if resp.Header != nil {
		revision = resp.Header.Revision
	}

	go func() {
		defer close(ch)

		// Send initial state as EventAdd.
		for _, kv := range resp.Kvs {
			var node NodeInfo
			if err := json.Unmarshal(kv.Value, &node); err != nil {
				continue
			}
			select {
			case ch <- Event{Type: EventAdd, Node: node}:
			case <-ctx.Done():
				return
			}
		}

		// Watch for changes starting after the initial listing.
		watchCh := d.client.Watch(ctx, prefix, clientv3.WithPrefix(), clientv3.WithRev(revision+1), clientv3.WithPrevKV())
		for wresp := range watchCh {
			for _, ev := range wresp.Events {
				var node NodeInfo
				switch ev.Type {
				case clientv3.EventTypePut:
					if err := json.Unmarshal(ev.Kv.Value, &node); err != nil {
						continue
					}
					evtType := EventAdd
					if ev.IsCreate() {
						evtType = EventAdd
					} else {
						evtType = EventUpdate
					}
					select {
					case ch <- Event{Type: evtType, Node: node}:
					case <-ctx.Done():
						return
					}
				case clientv3.EventTypeDelete:
					// For deletes, try to parse the previous value if available.
					if ev.PrevKv != nil {
						if err := json.Unmarshal(ev.PrevKv.Value, &node); err != nil {
							continue
						}
					}
					select {
					case ch <- Event{Type: EventDelete, Node: node}:
					case <-ctx.Done():
						return
					}
				}
			}
		}
	}()

	return ch, nil
}

// Lock acquires a distributed lock for the given key.
func (d *EtcdDiscovery) Lock(ctx context.Context, key string) (unlock func(), err error) {
	session, err := concurrency.NewSession(d.client)
	if err != nil {
		return nil, fmt.Errorf("create lock session: %w", err)
	}

	mutex := concurrency.NewMutex(session, "forge/locks/"+key)
	if err := mutex.Lock(ctx); err != nil {
		session.Close()
		return nil, fmt.Errorf("acquire lock %s: %w", key, err)
	}

	unlock = func() {
		mutex.Unlock(context.Background())
		session.Close()
	}
	return unlock, nil
}
