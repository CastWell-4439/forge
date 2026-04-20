package observability

import (
	"fmt"
	"log"
	"net/http"
	"runtime"
	"sync"
	"time"
)

// ProfileType represents a type of continuous profile.
type ProfileType string

const (
	ProfileCPU       ProfileType = "cpu"
	ProfileHeap      ProfileType = "heap"
	ProfileGoroutine ProfileType = "goroutine"
	ProfileMutex     ProfileType = "mutex"
	ProfileBlock     ProfileType = "block"
)

// ProfileSnapshot holds a single profiling data point.
type ProfileSnapshot struct {
	Type      ProfileType
	Timestamp time.Time
	Data      map[string]interface{}
}

// ProfilingConfig configures continuous profiling.
type ProfilingConfig struct {
	Enabled  bool
	Interval time.Duration // How often to collect profiles
	Types    []ProfileType // Which profiles to collect
}

// DefaultProfilingConfig returns default profiling settings.
func DefaultProfilingConfig() ProfilingConfig {
	return ProfilingConfig{
		Enabled:  true,
		Interval: 30 * time.Second,
		Types:    []ProfileType{ProfileCPU, ProfileHeap, ProfileGoroutine, ProfileMutex, ProfileBlock},
	}
}

// Profiler collects continuous profiling data.
type Profiler struct {
	config    ProfilingConfig
	snapshots []ProfileSnapshot
	mu        sync.RWMutex
	stopCh    chan struct{}
}

// NewProfiler creates a new continuous profiler.
func NewProfiler(config ProfilingConfig) *Profiler {
	return &Profiler{
		config: config,
		stopCh: make(chan struct{}),
	}
}

// Start begins periodic profile collection.
func (p *Profiler) Start() {
	if !p.config.Enabled {
		return
	}

	// Enable mutex and block profiling.
	runtime.SetMutexProfileFraction(5)
	runtime.SetBlockProfileRate(1000) // nanoseconds

	go p.collectLoop()
	log.Printf("INFO: profiler: started (interval=%v, types=%v)", p.config.Interval, p.config.Types)
}

// Stop stops the profiler.
func (p *Profiler) Stop() {
	select {
	case <-p.stopCh:
	default:
		close(p.stopCh)
	}
}

func (p *Profiler) collectLoop() {
	ticker := time.NewTicker(p.config.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-p.stopCh:
			return
		case <-ticker.C:
			p.collect()
		}
	}
}

func (p *Profiler) collect() {
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)

	now := time.Now()

	for _, profileType := range p.config.Types {
		snapshot := ProfileSnapshot{
			Type:      profileType,
			Timestamp: now,
			Data:      make(map[string]interface{}),
		}

		switch profileType {
		case ProfileHeap:
			snapshot.Data["alloc_bytes"] = memStats.Alloc
			snapshot.Data["total_alloc_bytes"] = memStats.TotalAlloc
			snapshot.Data["sys_bytes"] = memStats.Sys
			snapshot.Data["heap_objects"] = memStats.HeapObjects
			snapshot.Data["gc_cycles"] = memStats.NumGC
			snapshot.Data["gc_pause_ns"] = memStats.PauseTotalNs

		case ProfileGoroutine:
			snapshot.Data["count"] = runtime.NumGoroutine()

		case ProfileCPU:
			snapshot.Data["num_cpu"] = runtime.NumCPU()
			snapshot.Data["gomaxprocs"] = runtime.GOMAXPROCS(0)

		case ProfileMutex:
			snapshot.Data["fraction"] = runtime.SetMutexProfileFraction(-1) // read without changing

		case ProfileBlock:
			snapshot.Data["rate"] = 1000 // current rate
		}

		p.mu.Lock()
		p.snapshots = append(p.snapshots, snapshot)
		// Keep last 1000 snapshots to prevent unbounded growth.
		if len(p.snapshots) > 1000 {
			p.snapshots = p.snapshots[len(p.snapshots)-1000:]
		}
		p.mu.Unlock()
	}
}

// LatestSnapshots returns the most recent n snapshots.
func (p *Profiler) LatestSnapshots(n int) []ProfileSnapshot {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if n > len(p.snapshots) {
		n = len(p.snapshots)
	}
	result := make([]ProfileSnapshot, n)
	copy(result, p.snapshots[len(p.snapshots)-n:])
	return result
}

// SnapshotCount returns the total number of collected snapshots.
func (p *Profiler) SnapshotCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.snapshots)
}

// DebugHandler returns an HTTP handler for profiling debug endpoint.
func (p *Profiler) DebugHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")

		var memStats runtime.MemStats
		runtime.ReadMemStats(&memStats)

		fmt.Fprintf(w, "# Forge Profiling Debug\n\n")
		fmt.Fprintf(w, "goroutines:     %d\n", runtime.NumGoroutine())
		fmt.Fprintf(w, "heap_alloc:     %d MB\n", memStats.Alloc/1024/1024)
		fmt.Fprintf(w, "heap_sys:       %d MB\n", memStats.Sys/1024/1024)
		fmt.Fprintf(w, "heap_objects:   %d\n", memStats.HeapObjects)
		fmt.Fprintf(w, "gc_cycles:      %d\n", memStats.NumGC)
		fmt.Fprintf(w, "gc_pause_total: %d ms\n", memStats.PauseTotalNs/1_000_000)
		fmt.Fprintf(w, "gomaxprocs:     %d\n", runtime.GOMAXPROCS(0))
		fmt.Fprintf(w, "snapshots:      %d\n", p.SnapshotCount())
	})
}
