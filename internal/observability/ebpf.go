//go:build linux && ebpf

// Package observability — eBPF kernel-level TCP latency tracing.
// This file is only compiled on Linux with the "ebpf" build tag.
// It uses cilium/ebpf to load BPF programs and read perf events.
package observability

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"log"
	"net"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/link"
	"github.com/cilium/ebpf/perf"
)

// TCPEvent represents a TCP connection latency event from the eBPF program.
type TCPEvent struct {
	PID       uint32
	SrcAddr   net.IP
	DstAddr   net.IP
	DstPort   uint16
	LatencyNs uint64
	Comm      string // process name
}

// rawTCPEvent matches the C struct tcp_event layout.
type rawTCPEvent struct {
	PID       uint32
	SAddr     uint32
	DAddr     uint32
	DPort     uint16
	_         [2]byte // padding
	LatencyNs uint64
	Comm      [16]byte
}

// EBPFObserver loads and manages eBPF programs for kernel-level observability.
type EBPFObserver struct {
	coll       *ebpf.Collection
	perfReader *perf.Reader
	links      []link.Link
}

// NewEBPFObserver loads the pre-compiled BPF bytecode and attaches probes.
func NewEBPFObserver(bpfPath string) (*EBPFObserver, error) {
	spec, err := ebpf.LoadCollectionSpec(bpfPath)
	if err != nil {
		return nil, fmt.Errorf("ebpf: load spec %s: %w", bpfPath, err)
	}

	coll, err := ebpf.NewCollection(spec)
	if err != nil {
		return nil, fmt.Errorf("ebpf: create collection: %w", err)
	}

	obs := &EBPFObserver{coll: coll}

	// Attach kprobes.
	if prog, ok := coll.Programs["trace_tcp_v4_connect"]; ok {
		l, err := link.Kprobe("tcp_v4_connect", prog, nil)
		if err != nil {
			coll.Close()
			return nil, fmt.Errorf("ebpf: attach kprobe tcp_v4_connect: %w", err)
		}
		obs.links = append(obs.links, l)
	}

	if prog, ok := coll.Programs["trace_tcp_rcv_state"]; ok {
		l, err := link.Kprobe("tcp_rcv_state_process", prog, nil)
		if err != nil {
			obs.Close()
			return nil, fmt.Errorf("ebpf: attach kprobe tcp_rcv_state_process: %w", err)
		}
		obs.links = append(obs.links, l)
	}

	// Open perf reader for the events map.
	eventsMap, ok := coll.Maps["events"]
	if !ok {
		obs.Close()
		return nil, fmt.Errorf("ebpf: events map not found")
	}

	reader, err := perf.NewReader(eventsMap, 4096)
	if err != nil {
		obs.Close()
		return nil, fmt.Errorf("ebpf: create perf reader: %w", err)
	}
	obs.perfReader = reader

	return obs, nil
}

// ReadEvents reads TCP latency events from the eBPF perf buffer.
// It blocks until ctx is cancelled or an error occurs.
func (o *EBPFObserver) ReadEvents(ctx context.Context, handler func(TCPEvent)) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		record, err := o.perfReader.Read()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("ebpf: read perf: %w", err)
		}

		if record.LostSamples > 0 {
			log.Printf("WARN: ebpf: lost %d samples", record.LostSamples)
			continue
		}

		var raw rawTCPEvent
		if err := binary.Read(bytes.NewReader(record.RawSample), binary.LittleEndian, &raw); err != nil {
			log.Printf("WARN: ebpf: parse event: %v", err)
			continue
		}

		event := TCPEvent{
			PID:       raw.PID,
			SrcAddr:   intToIP(raw.SAddr),
			DstAddr:   intToIP(raw.DAddr),
			DstPort:   raw.DPort,
			LatencyNs: raw.LatencyNs,
			Comm:      nullTermStr(raw.Comm[:]),
		}

		handler(event)
	}
}

// Close releases all eBPF resources.
func (o *EBPFObserver) Close() error {
	if o.perfReader != nil {
		o.perfReader.Close()
	}
	for _, l := range o.links {
		l.Close()
	}
	if o.coll != nil {
		o.coll.Close()
	}
	return nil
}

// IsAvailable returns true — on Linux with ebpf tag, eBPF is available.
func IsEBPFAvailable() bool {
	return true
}

func intToIP(n uint32) net.IP {
	ip := make(net.IP, 4)
	binary.LittleEndian.PutUint32(ip, n)
	return ip
}

func nullTermStr(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}
