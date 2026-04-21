// SPDX-License-Identifier: GPL-2.0 OR BSD-3-Clause
//
// tcp_latency.c — eBPF program for tracing TCP connection establishment latency.
//
// Hooks:
//   kprobe/tcp_v4_connect — records the timestamp when a TCP connect() is initiated
//   kprobe/tcp_rcv_state_process — records when the connection reaches ESTABLISHED state
//
// The delta (ESTABLISHED - SYN_SENT) is emitted to a perf event buffer
// for the Go userspace program to read and export as Prometheus metrics.
//
// Build: clang -O2 -g -target bpf -D__TARGET_ARCH_x86 -c tcp_latency.c -o tcp_latency.o
// Requires: linux headers, vmlinux.h (BTF CO-RE) or manual struct definitions
//
// This is a CO-RE (Compile Once – Run Everywhere) BPF program that uses BTF
// type information to work across different kernel versions without recompilation.

#include "vmlinux.h"
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>
#include <bpf/bpf_core_read.h>

// TCP states we care about
#define TCP_SYN_SENT  2
#define TCP_ESTABLISHED 1

// Event emitted to userspace
struct tcp_event {
    __u32 pid;
    __u32 saddr;
    __u32 daddr;
    __u16 dport;
    __u64 latency_ns;   // connect() to ESTABLISHED duration in nanoseconds
    char comm[16];       // process name
};

// Map: stores connect() start timestamps keyed by sock pointer
struct {
    __uint(type, BPF_MAP_TYPE_HASH);
    __uint(max_entries, 65536);
    __type(key, struct sock *);
    __type(value, __u64);
} connect_start SEC(".maps");

// Perf event buffer for sending events to userspace
struct {
    __uint(type, BPF_MAP_TYPE_PERF_EVENT_ARRAY);
    __uint(key_size, sizeof(__u32));
    __uint(value_size, sizeof(__u32));
} events SEC(".maps");

// kprobe: tcp_v4_connect — called when userspace initiates a TCP connection
SEC("kprobe/tcp_v4_connect")
int BPF_KPROBE(trace_tcp_v4_connect, struct sock *sk)
{
    __u64 ts = bpf_ktime_get_ns();
    bpf_map_update_elem(&connect_start, &sk, &ts, BPF_ANY);
    return 0;
}

// kprobe: tcp_rcv_state_process — called on TCP state transitions
// We check for transition to ESTABLISHED and calculate latency.
SEC("kprobe/tcp_rcv_state_process")
int BPF_KPROBE(trace_tcp_rcv_state, struct sock *sk)
{
    // Read current socket state
    int state = BPF_CORE_READ(sk, __sk_common.skc_state);

    // We only care about connections transitioning to ESTABLISHED
    // At this point, the socket state hasn't been updated yet,
    // so we check if it's currently in SYN_SENT
    if (state != TCP_SYN_SENT)
        return 0;

    // Look up the connect start timestamp
    __u64 *start_ts = bpf_map_lookup_elem(&connect_start, &sk);
    if (!start_ts)
        return 0;

    // Calculate latency
    __u64 latency = bpf_ktime_get_ns() - *start_ts;

    // Build event
    struct tcp_event event = {};
    event.pid = bpf_get_current_pid_tgid() >> 32;
    event.latency_ns = latency;
    event.saddr = BPF_CORE_READ(sk, __sk_common.skc_rcv_saddr);
    event.daddr = BPF_CORE_READ(sk, __sk_common.skc_daddr);
    event.dport = BPF_CORE_READ(sk, __sk_common.skc_dport);
    bpf_get_current_comm(&event.comm, sizeof(event.comm));

    // Send to userspace via perf buffer
    bpf_perf_event_output(sk, &events, BPF_F_CURRENT_CPU, &event, sizeof(event));

    // Cleanup
    bpf_map_delete_elem(&connect_start, &sk);

    return 0;
}

char LICENSE[] SEC("license") = "Dual BSD/GPL";
