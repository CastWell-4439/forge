package observability

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"time"
)

// eBPF environment.
//
//	FORGE_EBPF_ENABLED  1 = load and attach the probes (default off)
//	FORGE_EBPF_OBJECT   path to the compiled .o (default bpf/tcp_latency.o)
//
// Off by default is the deliberate part. Loading kernel probes needs privileges
// and a matching kernel, so a deployment that did not ask for it must not have
// it attempted — a failure there would otherwise be a startup failure for an
// observability feature nobody requested.
const (
	envEBPFEnabled = "FORGE_EBPF_ENABLED"
	envEBPFObject  = "FORGE_EBPF_OBJECT"
)

// defaultEBPFObject is where the Makefile in bpf/ puts the compiled program.
// Pre-compiled objects are checked in for environments without clang.
const defaultEBPFObject = "bpf/tcp_latency.o"

// StartEBPFObserver attaches the kernel probes and reports TCP connection setup
// time to the given histogram until ctx is cancelled.
//
// It returns a stop function that is always safe to call, including when eBPF
// was never started — so the caller can defer it unconditionally.
//
// Every unavailable path is a log line and a no-op rather than an error. eBPF
// observes the network; it does not serve the workflow, and a node without
// kernel support must still be able to run Forge.
func StartEBPFObserver(ctx context.Context, metrics *Metrics) func() {
	noop := func() {}

	if !envBool(envEBPFEnabled) {
		return noop
	}
	if metrics == nil {
		log.Printf("WARN: ebpf: enabled but no metrics sink was supplied; not starting")
		return noop
	}
	if !IsEBPFAvailable() {
		// Say why rather than only that it did not start: the common causes are
		// a non-Linux host and a build without the ebpf tag, and they need
		// different fixes.
		log.Printf("INFO: %s is set but eBPF is unavailable on this platform "+
			"(needs linux built with the ebpf tag); not starting", envEBPFEnabled)
		return noop
	}

	object := os.Getenv(envEBPFObject)
	if object == "" {
		object = defaultEBPFObject
	}

	observer, err := NewEBPFObserver(object)
	if err != nil {
		if errors.Is(err, ErrEBPFUnavailable) {
			// The build cannot do this, and one line saying so is enough.
			log.Printf("INFO: ebpf: unavailable: %v", err)
			return noop
		}
		// A probe that could not attach is worth a warning: this platform can do
		// it, so something about this deployment is wrong.
		log.Printf("WARN: ebpf: could not attach probes from %s: %v", object, err)
		return noop
	}

	go func() {
		log.Printf("INFO: ebpf: observing TCP connect latency (object=%s)", object)
		err := observer.ReadEvents(ctx, func(e TCPEvent) {
			if e.LatencyNs == 0 {
				return
			}
			comm := e.Comm
			if comm == "" {
				comm = "unknown"
			}
			metrics.TCPConnectLatency.Observe(float64(e.LatencyNs)/float64(time.Second), comm)
		})
		if err != nil && ctx.Err() == nil && !errors.Is(err, ErrEBPFUnavailable) {
			log.Printf("WARN: ebpf: reader stopped: %v", err)
		}
	}()

	return func() {
		if err := observer.Close(); err != nil {
			log.Printf("WARN: ebpf: close: %v", err)
		}
	}
}

// envBool reads a boolean environment variable, accepting the spellings the rest
// of the project uses for its switches.
func envBool(name string) bool {
	v := os.Getenv(name)
	if v == "" {
		return false
	}
	switch v {
	case "1", "true", "TRUE", "yes", "YES", "on", "ON":
		return true
	}
	// A value that is neither truthy nor falsy still means "enabled" if it parses
	// as a non-zero number, which is what an operator writing FORGE_EBPF_ENABLED=2
	// would expect.
	if n, err := strconv.Atoi(v); err == nil {
		return n != 0
	}
	return false
}

// DescribeEBPF reports whether kernel observation is active and why, for a
// startup log an operator can act on.
func DescribeEBPF() string {
	if !envBool(envEBPFEnabled) {
		return fmt.Sprintf("off (set %s=1 to enable)", envEBPFEnabled)
	}
	if !IsEBPFAvailable() {
		return "unavailable on this platform"
	}
	return "enabled"
}
