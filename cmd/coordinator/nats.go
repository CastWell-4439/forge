package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/castwell/forge/internal/bus"
	"github.com/castwell/forge/internal/cache"
	"github.com/castwell/forge/internal/coordinator"
	"github.com/castwell/forge/internal/storage"
)

// FORGE_NATS_URL selects the event notification transport and enables the
// durable heartbeat store:
//
//	set      → NATS JetStream bus (events) + NATS KV (heartbeat snapshots)
//	unset    → if storage is PostgreSQL, fall back to the zero-dependency
//	           LISTEN/NOTIFY bus; otherwise events stay storage-only
const envNATSURL = "FORGE_NATS_URL"

// kvHeartbeatStore adapts the NATS KV store to the coordinator's
// HeartbeatStore interface (deliberately decoupled: the coordinator does not
// import the NATS types).
type kvHeartbeatStore struct {
	kv *cache.NATSKVHeartbeat
}

func (s kvHeartbeatStore) PutHeartbeat(ctx context.Context, snap coordinator.HeartbeatSnapshot) error {
	return s.kv.Put(ctx, cache.HeartbeatInfo{
		WorkerID:    snap.WorkerID,
		Addr:        snap.Addr,
		Capacity:    snap.Capacity,
		ActiveTasks: snap.ActiveTasks,
		Handlers:    snap.Handlers,
		Timestamp:   snap.Timestamp,
	})
}

func (s kvHeartbeatStore) DeleteHeartbeat(ctx context.Context, workerID string) error {
	return s.kv.Delete(ctx, workerID)
}

// setupNATS wires the notification side of the pipeline: it returns an event
// publisher (coordinator.EventPublisher), a durable heartbeat store, and a
// cleanup. Either may be nil — nil means that half stays storage-only /
// memory-only, exactly as before this wiring. A transport that is configured
// but unusable fails startup: a bus that silently does not publish is the
// half-wired state this exists to avoid.
func setupNATS(store storage.Storage) (coordinator.EventPublisher, coordinator.HeartbeatStore, func(), error) {
	noop := func() {}

	if url := strings.TrimSpace(os.Getenv(envNATSURL)); url != "" {
		nc, err := nats.Connect(url)
		if err != nil {
			return nil, nil, noop, fmt.Errorf("connect nats %s: %w", url, err)
		}
		cfg := bus.DefaultNATSConfig()
		cfg.URL = url
		nb, err := bus.NewNATSBus(nc, cfg)
		if err != nil {
			nc.Close()
			return nil, nil, noop, fmt.Errorf("create nats bus: %w", err)
		}
		js, err := jetstream.New(nc)
		if err != nil {
			nb.Close()
			return nil, nil, noop, fmt.Errorf("create jetstream context: %w", err)
		}
		kv, err := cache.NewNATSKVHeartbeat(js, cache.DefaultNATSKVConfig())
		if err != nil {
			nb.Close()
			return nil, nil, noop, fmt.Errorf("create heartbeat kv: %w", err)
		}
		log.Printf("INFO: event bus enabled (nats=%s stream=%s) + heartbeat kv store", url, cfg.StreamName)
		return nb, kvHeartbeatStore{kv: kv}, func() { _ = nb.Close() }, nil
	}

	if pg, ok := store.(*storage.PGStorage); ok {
		pgBus := bus.NewPGNotifyBus(pg.Pool())
		log.Printf("INFO: event bus enabled (postgres LISTEN/NOTIFY)")
		return pgBus, nil, func() { _ = pgBus.Close() }, nil
	}

	return nil, nil, noop, nil
}
