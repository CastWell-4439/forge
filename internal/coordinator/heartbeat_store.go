package coordinator

import (
	"context"
	"log"
	"time"
)

// HeartbeatSnapshot is the durable copy of one worker's heartbeat: the same
// facts the gRPC heartbeat stream carries, in a form another process can
// read.
type HeartbeatSnapshot struct {
	WorkerID    string            `json:"worker_id"`
	Addr        string            `json:"addr"`
	Capacity    int               `json:"capacity"`
	ActiveTasks int               `json:"active_tasks"`
	Handlers    []string          `json:"handlers"`
	Labels      map[string]string `json:"labels,omitempty"`
	Timestamp   time.Time         `json:"timestamp"`
}

// HeartbeatStore persists heartbeat snapshots somewhere durable (NATS KV in
// production — README's "NATS (消息+心跳)").
//
// The division of labour is deliberate: the gRPC heartbeat stream stays the
// liveness mechanism (it drives SUSPECT/DEAD and task rescheduling — the
// execution model's red line), while this store keeps a shared,
// restart-survivable copy of the same facts for other processes to read.
// Writes are best-effort: a store outage degrades persistence, never
// liveness.
type HeartbeatStore interface {
	PutHeartbeat(ctx context.Context, snap HeartbeatSnapshot) error
	DeleteHeartbeat(ctx context.Context, workerID string) error
}

// SetHeartbeatStore installs the durable heartbeat sink. Nil (the default)
// keeps heartbeats memory-only, which is the behaviour before this wiring.
// Call it before workers are added so registrations are persisted too.
func (wm *WorkerManager) SetHeartbeatStore(s HeartbeatStore) {
	wm.hbStore = s
}

// persistHeartbeat writes one snapshot. Callers must NOT hold wm.mu: the
// store performs I/O, and a slow store must not stall worker bookkeeping.
func (wm *WorkerManager) persistHeartbeat(snap HeartbeatSnapshot) {
	if wm.hbStore == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := wm.hbStore.PutHeartbeat(ctx, snap); err != nil {
		log.Printf("WARN: persist heartbeat %s: %v", snap.WorkerID, err)
	}
}

// unpersistHeartbeat removes a worker's snapshot (explicit deregistration or
// DEAD). KV TTL is the backstop for a delete that never lands.
func (wm *WorkerManager) unpersistHeartbeat(workerID string) {
	if wm.hbStore == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := wm.hbStore.DeleteHeartbeat(ctx, workerID); err != nil {
		log.Printf("WARN: unpersist heartbeat %s: %v", workerID, err)
	}
}

// snapshotFor builds a snapshot from a registered worker. Call with wm.mu
// held; the snapshot itself is written after the lock is released.
func snapshotFor(w *WorkerInfo, activeTasks int, labels map[string]string) HeartbeatSnapshot {
	return HeartbeatSnapshot{
		WorkerID:    w.Registration.ID,
		Addr:        w.Registration.Addr,
		Capacity:    w.Capacity,
		ActiveTasks: activeTasks,
		Handlers:    w.Handlers,
		Labels:      labels,
		Timestamp:   time.Now(),
	}
}
