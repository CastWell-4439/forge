// Package bus defines the event bus interface and provides a PostgreSQL
// LISTEN/NOTIFY implementation for lightweight event notification.
package bus

import (
	"context"
	"fmt"
	"log"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"
)

// EventBus is the interface for publishing and subscribing to event channels.
type EventBus interface {
	// Publish sends a payload to the named channel.
	Publish(ctx context.Context, channel, payload string) error
	// Subscribe listens on the named channel and returns a channel of payloads.
	// The returned channel is closed when the context is cancelled.
	Subscribe(ctx context.Context, channel string) (<-chan string, error)
	// Close releases any resources held by the bus.
	Close() error
}

// PGNotifyBus implements EventBus using PostgreSQL LISTEN/NOTIFY.
// This is the default (zero-dependency) event notification layer for Forge.
type PGNotifyBus struct {
	pool   *pgxpool.Pool
	mu     sync.Mutex
	closed bool
}

// NewPGNotifyBus creates a new PGNotifyBus backed by the given pgx connection pool.
func NewPGNotifyBus(pool *pgxpool.Pool) *PGNotifyBus {
	return &PGNotifyBus{pool: pool}
}

// Publish sends a NOTIFY on the given channel with the payload.
func (b *PGNotifyBus) Publish(ctx context.Context, channel, payload string) error {
	_, err := b.pool.Exec(ctx, fmt.Sprintf("SELECT pg_notify($1, $2)"), channel, payload)
	if err != nil {
		return fmt.Errorf("pg notify channel %s: %w", channel, err)
	}
	return nil
}

// Subscribe starts listening on the given PostgreSQL channel and returns a
// channel that receives notification payloads. The returned channel is closed
// when the context is cancelled or an unrecoverable error occurs.
func (b *PGNotifyBus) Subscribe(ctx context.Context, channel string) (<-chan string, error) {
	conn, err := b.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire connection for LISTEN %s: %w", channel, err)
	}

	_, err = conn.Exec(ctx, fmt.Sprintf("LISTEN %s", channel))
	if err != nil {
		conn.Release()
		return nil, fmt.Errorf("listen on channel %s: %w", channel, err)
	}

	out := make(chan string, 64)

	go func() {
		defer conn.Release()
		defer close(out)

		for {
			notification, err := conn.Conn().WaitForNotification(ctx)
			if err != nil {
				if ctx.Err() != nil {
					return // context cancelled, clean shutdown
				}
				log.Printf("ERROR: pg listen %s: %v", channel, err)
				return
			}

			select {
			case out <- notification.Payload:
			case <-ctx.Done():
				return
			}
		}
	}()

	return out, nil
}

// Close marks the bus as closed. Active subscriptions should be cancelled
// via their context.
func (b *PGNotifyBus) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	return nil
}
