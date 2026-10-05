package cdc

import (
	"context"
	"fmt"
	"log"
	"strings"
)

// CapabilityError reports whether an error means "this database cannot do
// logical replication" rather than "something transient broke".
//
// This is the classifier behind README's "WAL 不可用时（权限不足 / 版本不支持）
// 自动降级为轮询模式": only a capability problem — no permission, no plugin,
// unsupported server — justifies silently switching capture strategies. A
// connection reset or a syntax bug must keep failing loudly, because polling
// would hide it and lose the guarantee it was supposed to provide.
func CapabilityError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())

	// PostgreSQL SQLSTATEs that mean "the feature cannot be used here".
	for _, code := range []string{
		"sqlstate 0a000", // feature_not_supported
		"sqlstate 42501", // insufficient_privilege
		"sqlstate 3f000", // invalid_schema_name
		"sqlstate 42704", // undefined_object (slot/publication missing and undeletable)
	} {
		if strings.Contains(msg, code) {
			return true
		}
	}

	// Human-readable phrasings PG produces around logical replication.
	for _, phrase := range []string{
		"permission denied",
		"must be superuser",
		"wal_level",
		"logical decoding",
		"does not exist", // replication slot / publication / output plugin
		"access to replication slots",
	} {
		if strings.Contains(msg, phrase) {
			return true
		}
	}
	return false
}

// FallbackSource tries the primary capture strategy (WAL streaming) and
// switches to the fallback (SELECT polling) when — and only when — the
// primary fails for lack of capability. Any other failure propagates: a
// broken connection should not be papered over by a weaker strategy.
type FallbackSource struct {
	name     string
	primary  Source
	fallback Source
}

// NewFallbackSource wires the automatic downgrade described in README.
func NewFallbackSource(name string, primary, fallback Source) *FallbackSource {
	return &FallbackSource{name: name, primary: primary, fallback: fallback}
}

// Subscribe runs the primary until it succeeds (returns on cancel) or fails
// on capability, in which case the fallback takes over the same handler.
func (f *FallbackSource) Subscribe(ctx context.Context, handler func(Event)) error {
	err := f.primary.Subscribe(ctx, handler)
	if err == nil || ctx.Err() != nil {
		return err
	}
	if !CapabilityError(err) {
		return fmt.Errorf("cdc %s: primary source failed (not downgrading): %w", f.name, err)
	}
	log.Printf("WARN: cdc %s: WAL unavailable (%v) — auto-downgraded to SELECT polling", f.name, err)
	return f.fallback.Subscribe(ctx, handler)
}

// Close releases both strategies; the one never started closes harmlessly.
func (f *FallbackSource) Close() error {
	first := f.primary.Close()
	second := f.fallback.Close()
	if first != nil {
		return first
	}
	return second
}
