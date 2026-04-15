package discovery

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEventTypeConstants(t *testing.T) {
	assert.Equal(t, EventType(0), EventAdd)
	assert.Equal(t, EventType(1), EventDelete)
	assert.Equal(t, EventType(2), EventUpdate)
}

func TestNodeInfo(t *testing.T) {
	node := NodeInfo{
		ID:       "node-1",
		Addr:     "localhost:8080",
		Labels:   map[string]string{"gpu": "true"},
		Metadata: map[string]string{"version": "1.0"},
	}
	assert.Equal(t, "node-1", node.ID)
	assert.Equal(t, "localhost:8080", node.Addr)
	assert.Equal(t, "true", node.Labels["gpu"])
	assert.Equal(t, "1.0", node.Metadata["version"])
}

func TestEvent(t *testing.T) {
	evt := Event{
		Type: EventAdd,
		Node: NodeInfo{ID: "worker-1", Addr: "10.0.0.1:9090"},
	}
	assert.Equal(t, EventAdd, evt.Type)
	assert.Equal(t, "worker-1", evt.Node.ID)
}

// Compile-time interface satisfaction check.
var _ Discovery = (*mockDiscovery)(nil)

type mockDiscovery struct{}

func (m *mockDiscovery) LeaderElect(_ context.Context) (<-chan bool, error) {
	return nil, nil
}

func (m *mockDiscovery) Register(_ context.Context, _ NodeInfo) error {
	return nil
}

func (m *mockDiscovery) Watch(_ context.Context, _ string) (<-chan Event, error) {
	return nil, nil
}

func (m *mockDiscovery) Lock(_ context.Context, _ string) (func(), error) {
	return nil, nil
}
