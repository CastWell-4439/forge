//go:build !linux || !ebpf

// Package observability — eBPF stub for non-Linux platforms or when the ebpf
// build tag is not set.
//
// This ensures the codebase compiles on Windows/macOS without the cilium/ebpf
// dependency. It mirrors the real API rather than exposing only
// IsEBPFAvailable(): callers that wire eBPF in have to compile on every
// platform, and a stub with a different shape would force them to guard every
// reference with a build tag of their own. The real definitions live in
// ebpf.go behind `linux && ebpf`.
package observability

import (
	"context"
	"errors"
	"net"
)

// ErrEBPFUnavailable is the sentinel for "this build or host cannot do kernel
// observation". A distinct value rather than only a message, so a caller can tell
// an unsupported platform apart from a probe that failed to attach — those need
// different responses (carry on quietly versus tell someone).
var ErrEBPFUnavailable = errors.New("ebpf: not available on this platform (needs linux with the ebpf build tag)")

// TCPEvent is the stub's view of a TCP latency event. The fields match the real
// one so callers can handle both without conditional code.
type TCPEvent struct {
	PID       uint32
	SrcAddr   net.IP
	DstAddr   net.IP
	DstPort   uint16
	LatencyNs uint64
	Comm      string
}

// EBPFObserver is the stub observer. It holds nothing and does nothing; the
// only thing callers can rely on is that its methods are safe to call.
type EBPFObserver struct{}

// NewEBPFObserver always reports that eBPF is unavailable here, rather than
// returning a broken observer that would fail later and less clearly.
func NewEBPFObserver(bpfPath string) (*EBPFObserver, error) {
	return nil, ErrEBPFUnavailable
}

// ReadEvents returns the sentinel: there is nothing to read.
func (o *EBPFObserver) ReadEvents(ctx context.Context, handler func(TCPEvent)) error {
	return ErrEBPFUnavailable
}

// Close is a no-op.
func (o *EBPFObserver) Close() error { return nil }

// IsEBPFAvailable returns false on non-Linux platforms or without the build tag.
func IsEBPFAvailable() bool {
	return false
}
