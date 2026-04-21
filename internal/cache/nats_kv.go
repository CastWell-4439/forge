// Package cache — NATSKVHeartbeat implements Worker heartbeat storage using NATS KV Store.
// It replaces Redis for heartbeat tracking with zero additional infrastructure
// (NATS JetStream provides KV Store as a built-in feature).
package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// HeartbeatInfo holds the metadata sent with each heartbeat.
type HeartbeatInfo struct {
	WorkerID    string            `json:"worker_id"`
	Addr        string            `json:"addr"`
	Capacity    int               `json:"capacity"`
	ActiveTasks int               `json:"active_tasks"`
	Handlers    []string          `json:"handlers"`
	Labels      map[string]string `json:"labels,omitempty"`
	Timestamp   time.Time         `json:"timestamp"`
}

// NATSKVHeartbeat manages Worker heartbeats via NATS KV Store.
// Keys auto-expire based on the bucket's TTL, so stale workers
// are automatically removed without explicit cleanup.
type NATSKVHeartbeat struct {
	kv  jetstream.KeyValue
	ttl time.Duration
}

// NATSKVConfig configures the heartbeat KV bucket.
type NATSKVConfig struct {
	// BucketName is the KV bucket name (default: "FORGE_HEARTBEATS").
	BucketName string
	// TTL is the key expiration time (default: 30s).
	// Workers must heartbeat before TTL expires to stay active.
	TTL time.Duration
}

// DefaultNATSKVConfig returns sensible defaults.
func DefaultNATSKVConfig() NATSKVConfig {
	return NATSKVConfig{
		BucketName: "FORGE_HEARTBEATS",
		TTL:        30 * time.Second,
	}
}

// NewNATSKVHeartbeat creates a heartbeat store backed by NATS KV.
func NewNATSKVHeartbeat(js jetstream.JetStream, cfg NATSKVConfig) (*NATSKVHeartbeat, error) {
	if cfg.BucketName == "" {
		cfg = DefaultNATSKVConfig()
	}
	if cfg.TTL == 0 {
		cfg.TTL = 30 * time.Second
	}

	kv, err := js.CreateOrUpdateKeyValue(context.Background(), jetstream.KeyValueConfig{
		Bucket: cfg.BucketName,
		TTL:    cfg.TTL,
	})
	if err != nil {
		return nil, fmt.Errorf("nats kv: create bucket %s: %w", cfg.BucketName, err)
	}

	return &NATSKVHeartbeat{kv: kv, ttl: cfg.TTL}, nil
}

// Put updates the heartbeat for a worker (upsert).
func (h *NATSKVHeartbeat) Put(ctx context.Context, info HeartbeatInfo) error {
	info.Timestamp = time.Now()
	data, err := json.Marshal(info)
	if err != nil {
		return fmt.Errorf("nats kv: marshal heartbeat: %w", err)
	}

	_, err = h.kv.Put(ctx, info.WorkerID, data)
	if err != nil {
		return fmt.Errorf("nats kv: put %s: %w", info.WorkerID, err)
	}
	return nil
}

// Get retrieves the heartbeat for a worker.
func (h *NATSKVHeartbeat) Get(ctx context.Context, workerID string) (*HeartbeatInfo, error) {
	entry, err := h.kv.Get(ctx, workerID)
	if err != nil {
		return nil, fmt.Errorf("nats kv: get %s: %w", workerID, err)
	}

	var info HeartbeatInfo
	if err := json.Unmarshal(entry.Value(), &info); err != nil {
		return nil, fmt.Errorf("nats kv: unmarshal %s: %w", workerID, err)
	}
	return &info, nil
}

// Delete removes a worker's heartbeat (explicit deregistration).
func (h *NATSKVHeartbeat) Delete(ctx context.Context, workerID string) error {
	return h.kv.Delete(ctx, workerID)
}

// Keys returns all active worker IDs (keys that haven't expired).
func (h *NATSKVHeartbeat) Keys(ctx context.Context) ([]string, error) {
	keys, err := h.kv.Keys(ctx)
	if err != nil {
		return nil, fmt.Errorf("nats kv: list keys: %w", err)
	}
	return keys, nil
}

// Watch monitors worker heartbeat changes (add/update/delete).
// Returns a channel that receives updates.
func (h *NATSKVHeartbeat) Watch(ctx context.Context) (<-chan HeartbeatEvent, error) {
	watcher, err := h.kv.WatchAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("nats kv: watch: %w", err)
	}

	out := make(chan HeartbeatEvent, 64)

	go func() {
		defer close(out)
		defer watcher.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case entry, ok := <-watcher.Updates():
				if !ok {
					return
				}
				if entry == nil {
					continue // initial values done marker
				}

				evt := HeartbeatEvent{WorkerID: entry.Key()}

				switch entry.Operation() {
				case jetstream.KeyValuePut:
					evt.Type = HeartbeatPut
					var info HeartbeatInfo
					if json.Unmarshal(entry.Value(), &info) == nil {
						evt.Info = &info
					}
				case jetstream.KeyValueDelete, jetstream.KeyValuePurge:
					evt.Type = HeartbeatDelete
				}

				select {
				case out <- evt:
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	return out, nil
}

// HeartbeatEventType indicates the kind of heartbeat change.
type HeartbeatEventType int

const (
	// HeartbeatPut indicates a worker sent a heartbeat (new or update).
	HeartbeatPut HeartbeatEventType = iota
	// HeartbeatDelete indicates a worker's heartbeat was removed (expired or explicit).
	HeartbeatDelete
)

// HeartbeatEvent represents a change in worker heartbeat state.
type HeartbeatEvent struct {
	Type     HeartbeatEventType
	WorkerID string
	Info     *HeartbeatInfo // nil for delete events
}
