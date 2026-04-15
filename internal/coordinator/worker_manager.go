package coordinator

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/castwell/forge/internal/discovery"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/timestamppb"

	forgev1 "github.com/castwell/forge/api/proto/gen"
)

// WorkerStatus represents the health status of a worker.
type WorkerStatus string

const (
	// WorkerStatusActive means the worker is healthy and accepting tasks.
	WorkerStatusActive WorkerStatus = "ACTIVE"
	// WorkerStatusSuspect means the worker missed heartbeats and may be failing.
	WorkerStatusSuspect WorkerStatus = "SUSPECT"
	// WorkerStatusDead means the worker is unresponsive; its tasks will be rescheduled.
	WorkerStatusDead WorkerStatus = "DEAD"
)

const (
	// heartbeatInterval is how often the coordinator pings workers.
	heartbeatInterval = 10 * time.Second
	// suspectThreshold is 3 missed pings (30s).
	suspectThreshold = 30 * time.Second
	// deadThreshold is 60s with no response.
	deadThreshold = 60 * time.Second
	// failureCheckInterval is how often we scan for failed workers.
	failureCheckInterval = 5 * time.Second
)

// WorkerInfo tracks a worker's registration, status, and heartbeat state.
type WorkerInfo struct {
	Registration discovery.NodeInfo
	Handlers     []string
	Capacity     int
	Status       WorkerStatus
	LastHeartbeat time.Time
	ActiveTasks  int
	Conn         *grpc.ClientConn
	Client       forgev1.WorkerServiceClient
}

// WorkerManager maintains the set of known workers discovered via etcd,
// manages heartbeat streams, and detects worker failures.
type WorkerManager struct {
	disco   discovery.Discovery
	workers map[string]*WorkerInfo
	mu      sync.RWMutex

	// heartbeatCancels holds cancel functions for per-worker heartbeat goroutines.
	heartbeatCancels map[string]context.CancelFunc

	// onWorkerDead is called when a worker is marked DEAD.
	// The coordinator sets this to reschedule tasks.
	onWorkerDead func(workerID string)
}

// NewWorkerManager creates a new WorkerManager.
func NewWorkerManager(disco discovery.Discovery) *WorkerManager {
	return &WorkerManager{
		disco:            disco,
		workers:          make(map[string]*WorkerInfo),
		heartbeatCancels: make(map[string]context.CancelFunc),
	}
}

// OnWorkerDead sets the callback invoked when a worker is marked DEAD.
func (wm *WorkerManager) OnWorkerDead(fn func(workerID string)) {
	wm.onWorkerDead = fn
}

// GetWorker returns a worker by ID, or nil if not found.
func (wm *WorkerManager) GetWorker(id string) *WorkerInfo {
	wm.mu.RLock()
	defer wm.mu.RUnlock()
	return wm.workers[id]
}

// ActiveWorkers returns all workers with ACTIVE status.
func (wm *WorkerManager) ActiveWorkers() []*WorkerInfo {
	wm.mu.RLock()
	defer wm.mu.RUnlock()
	var result []*WorkerInfo
	for _, w := range wm.workers {
		if w.Status == WorkerStatusActive {
			result = append(result, w)
		}
	}
	return result
}

// AllWorkers returns a snapshot of all known workers.
func (wm *WorkerManager) AllWorkers() map[string]*WorkerInfo {
	wm.mu.RLock()
	defer wm.mu.RUnlock()
	result := make(map[string]*WorkerInfo, len(wm.workers))
	for k, v := range wm.workers {
		result[k] = v
	}
	return result
}

// WatchWorkers starts watching the etcd prefix forge/workers/ for worker add/remove events.
// It maintains the in-memory worker list. Blocks until ctx is cancelled.
func (wm *WorkerManager) WatchWorkers(ctx context.Context) error {
	ch, err := wm.disco.Watch(ctx, "forge/workers/")
	if err != nil {
		return fmt.Errorf("watch workers: %w", err)
	}

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case evt, ok := <-ch:
				if !ok {
					return
				}
				wm.handleWorkerEvent(evt)
			}
		}
	}()

	return nil
}

// handleWorkerEvent processes a discovery event for a worker.
func (wm *WorkerManager) handleWorkerEvent(evt discovery.Event) {
	switch evt.Type {
	case discovery.EventAdd:
		wm.addWorker(evt.Node)
	case discovery.EventUpdate:
		wm.updateWorker(evt.Node)
	case discovery.EventDelete:
		wm.removeWorker(evt.Node.ID)
	}
}

// addWorker registers a new worker from a discovery event.
func (wm *WorkerManager) addWorker(node discovery.NodeInfo) {
	wm.mu.Lock()
	defer wm.mu.Unlock()

	if _, exists := wm.workers[node.ID]; exists {
		return
	}

	conn, err := grpc.NewClient(node.Addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Printf("ERROR: connect to worker %s at %s: %v", node.ID, node.Addr, err)
		return
	}

	handlers := parseLabels(node.Metadata, "handlers")
	capacity := parseIntLabel(node.Metadata, "capacity", 10)
	client := forgev1.NewWorkerServiceClient(conn)

	wm.workers[node.ID] = &WorkerInfo{
		Registration:  node,
		Handlers:      handlers,
		Capacity:      capacity,
		Status:        WorkerStatusActive,
		LastHeartbeat: time.Now(),
		Conn:          conn,
		Client:        client,
	}

	// Start heartbeat sender for this worker.
	wm.startHeartbeat(node.ID, client)

	log.Printf("INFO: worker %s registered at %s (capacity=%d, handlers=%v)", node.ID, node.Addr, capacity, handlers)
}

// updateWorker updates an existing worker's info from a discovery event.
func (wm *WorkerManager) updateWorker(node discovery.NodeInfo) {
	wm.mu.Lock()
	defer wm.mu.Unlock()

	w, exists := wm.workers[node.ID]
	if !exists {
		return
	}
	w.Registration = node
	w.Handlers = parseLabels(node.Metadata, "handlers")
	w.Capacity = parseIntLabel(node.Metadata, "capacity", w.Capacity)
}

// removeWorker deregisters a worker and closes its gRPC connection.
func (wm *WorkerManager) removeWorker(id string) {
	// Cancel heartbeat goroutine.
	if cancel, ok := wm.heartbeatCancels[id]; ok {
		cancel()
		delete(wm.heartbeatCancels, id)
	}

	wm.mu.Lock()
	w, exists := wm.workers[id]
	if !exists {
		wm.mu.Unlock()
		return
	}
	delete(wm.workers, id)
	wm.mu.Unlock()

	if w.Conn != nil {
		w.Conn.Close()
	}
	log.Printf("INFO: worker %s removed", id)
}

// UpdateHeartbeat records a successful heartbeat from a worker.
func (wm *WorkerManager) UpdateHeartbeat(workerID string, activeTasks int, capacity int) {
	wm.mu.Lock()
	defer wm.mu.Unlock()

	w, exists := wm.workers[workerID]
	if !exists {
		return
	}

	oldStatus := w.Status
	w.LastHeartbeat = time.Now()
	w.ActiveTasks = activeTasks
	w.Capacity = capacity
	w.Status = WorkerStatusActive

	if oldStatus != WorkerStatusActive {
		log.Printf("INFO: worker %s recovered from %s to ACTIVE", workerID, oldStatus)
	}
}

// RunFailureDetector periodically checks worker heartbeats and transitions
// workers to SUSPECT or DEAD based on missed heartbeats.
func (wm *WorkerManager) RunFailureDetector(ctx context.Context) {
	ticker := time.NewTicker(failureCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			wm.checkWorkerHealth()
		}
	}
}

// checkWorkerHealth scans all workers and updates their status based on
// time since last heartbeat.
func (wm *WorkerManager) checkWorkerHealth() {
	wm.mu.Lock()
	now := time.Now()
	var deadWorkerIDs []string

	for id, w := range wm.workers {
		if w.Status == WorkerStatusDead {
			continue
		}

		elapsed := now.Sub(w.LastHeartbeat)

		if elapsed >= deadThreshold {
			oldStatus := w.Status
			w.Status = WorkerStatusDead
			deadWorkerIDs = append(deadWorkerIDs, id)
			log.Printf("INFO: worker %s transitioned %s -> DEAD (no heartbeat for %v)", id, oldStatus, elapsed.Round(time.Second))
		} else if elapsed >= suspectThreshold && w.Status == WorkerStatusActive {
			w.Status = WorkerStatusSuspect
			log.Printf("INFO: worker %s transitioned ACTIVE -> SUSPECT (no heartbeat for %v)", id, elapsed.Round(time.Second))
		}
	}
	wm.mu.Unlock()

	// Call onWorkerDead outside the lock to avoid deadlock.
	if wm.onWorkerDead != nil {
		for _, id := range deadWorkerIDs {
			wm.onWorkerDead(id)
		}
	}
}

// AddWorkerDirect adds a worker directly (for use without etcd discovery, e.g., testing).
func (wm *WorkerManager) AddWorkerDirect(id, addr string, handlers []string, capacity int) error {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("connect to worker %s at %s: %w", id, addr, err)
	}

	wm.mu.Lock()
	defer wm.mu.Unlock()
	wm.workers[id] = &WorkerInfo{
		Registration: discovery.NodeInfo{ID: id, Addr: addr},
		Handlers:     handlers,
		Capacity:     capacity,
		Status:       WorkerStatusActive,
		LastHeartbeat: time.Now(),
		Conn:         conn,
		Client:       forgev1.NewWorkerServiceClient(conn),
	}
	return nil
}

// startHeartbeat launches a goroutine that sends heartbeat Pings to a worker
// and processes Pong responses. Must be called with wm.mu held.
func (wm *WorkerManager) startHeartbeat(workerID string, client forgev1.WorkerServiceClient) {
	ctx, cancel := context.WithCancel(context.Background())
	wm.heartbeatCancels[workerID] = cancel

	go func() {
		for {
			err := wm.runHeartbeatStream(ctx, workerID, client)
			if ctx.Err() != nil {
				return // context cancelled, stop retrying
			}
			log.Printf("ERROR: heartbeat stream to worker %s failed: %v, reconnecting...", workerID, err)
			time.Sleep(1 * time.Second)
		}
	}()
}

// runHeartbeatStream opens a single heartbeat stream to the worker and runs
// the ping/pong loop until it fails or the context is cancelled.
func (wm *WorkerManager) runHeartbeatStream(ctx context.Context, workerID string, client forgev1.WorkerServiceClient) error {
	stream, err := client.Heartbeat(ctx)
	if err != nil {
		return fmt.Errorf("open heartbeat stream: %w", err)
	}

	// Receive pongs in a goroutine.
	errCh := make(chan error, 1)
	go func() {
		for {
			pong, err := stream.Recv()
			if err != nil {
				errCh <- err
				return
			}
			wm.UpdateHeartbeat(workerID, int(pong.GetActiveTasks()), int(pong.GetCapacity()))
		}
	}()

	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()

	// Send the first ping immediately.
	if err := stream.Send(&forgev1.HeartbeatPing{Timestamp: timestamppb.Now()}); err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			stream.CloseSend()
			return ctx.Err()
		case err := <-errCh:
			return err
		case <-ticker.C:
			if err := stream.Send(&forgev1.HeartbeatPing{Timestamp: timestamppb.Now()}); err != nil {
				return err
			}
		}
	}
}

// parseLabels extracts a comma-separated list from a metadata key.
func parseLabels(metadata map[string]string, key string) []string {
	if metadata == nil {
		return nil
	}
	val, ok := metadata[key]
	if !ok || val == "" {
		return nil
	}
	var result []string
	for _, s := range strings.Split(val, ",") {
		s = strings.TrimSpace(s)
		if s != "" {
			result = append(result, s)
		}
	}
	return result
}

// parseIntLabel extracts an integer from a metadata key, returning defaultVal on failure.
func parseIntLabel(metadata map[string]string, key string, defaultVal int) int {
	if metadata == nil {
		return defaultVal
	}
	val, ok := metadata[key]
	if !ok || val == "" {
		return defaultVal
	}
	var n int
	if _, err := fmt.Sscanf(val, "%d", &n); err != nil {
		return defaultVal
	}
	return n
}
