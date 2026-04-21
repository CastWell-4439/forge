//go:build !linux || !ebpf

// Package observability — eBPF stub for non-Linux platforms or when ebpf tag is not set.
// This ensures the codebase compiles on Windows/macOS without the cilium/ebpf dependency.
package observability

// IsEBPFAvailable returns false on non-Linux platforms.
func IsEBPFAvailable() bool {
	return false
}
