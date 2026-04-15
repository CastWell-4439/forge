package bus

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPGNotifyBus_Close(t *testing.T) {
	// Test that Close marks the bus as closed without panicking.
	// Full integration tests require a real PostgreSQL connection.
	bus := &PGNotifyBus{}
	err := bus.Close()
	assert.NoError(t, err)
	assert.True(t, bus.closed)
}

func TestPGNotifyBus_DoubleClose(t *testing.T) {
	bus := &PGNotifyBus{}
	assert.NoError(t, bus.Close())
	assert.NoError(t, bus.Close())
}
