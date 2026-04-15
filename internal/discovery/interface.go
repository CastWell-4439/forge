// Package discovery defines the service discovery and coordination interface
// for Forge. It abstracts leader election, service registration, watch-based
// discovery, and distributed locking behind a pluggable interface.
package discovery

import "context"

// EventType indicates the kind of change observed through Watch.
type EventType int

const (
	// EventAdd indicates a new node was registered.
	EventAdd EventType = iota
	// EventDelete indicates a node was removed.
	EventDelete
	// EventUpdate indicates a node's metadata changed.
	EventUpdate
)

// NodeInfo describes a service node registered in the discovery system.
type NodeInfo struct {
	ID       string
	Addr     string
	Labels   map[string]string
	Metadata map[string]string
}

// Event represents a change in the set of registered nodes.
type Event struct {
	Type EventType
	Node NodeInfo
}

// Discovery defines the service discovery and coordination interface.
// Implementations include embedded etcd (primary) and HashiCorp Raft (backup).
type Discovery interface {
	// LeaderElect starts leader election and returns a channel that signals
	// whether this node is the current leader (true) or has lost leadership (false).
	LeaderElect(ctx context.Context) (<-chan bool, error)

	// Register registers this node with the discovery system so other nodes
	// can find it via Watch.
	Register(ctx context.Context, node NodeInfo) error

	// Watch observes changes under the given key prefix and sends events
	// for added, deleted, or updated nodes.
	Watch(ctx context.Context, prefix string) (<-chan Event, error)

	// Lock acquires a distributed lock for the given key. The returned unlock
	// function must be called to release the lock.
	Lock(ctx context.Context, key string) (unlock func(), err error)
}
