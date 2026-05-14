package checkpoint

import (
	"context"
	"fmt"
	"testing"

	"github.com/castwell/forge/internal/agent/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInMemoryStore_SaveAndLoad(t *testing.T) {
	s := NewInMemoryStore(20)
	ctx := context.Background()

	cp := &core.Checkpoint{
		ID:        "cp-1",
		SessionID: "sess-1",
		StepIndex: 1,
		Messages:  []core.Message{{Role: "user", Content: "hello"}},
	}

	err := s.Save(ctx, cp)
	require.NoError(t, err)

	loaded, err := s.Load(ctx, "cp-1")
	require.NoError(t, err)
	assert.Equal(t, cp.ID, loaded.ID)
	assert.Equal(t, cp.SessionID, loaded.SessionID)
	assert.Len(t, loaded.Messages, 1)
}

func TestInMemoryStore_LoadNotFound(t *testing.T) {
	s := NewInMemoryStore(20)
	_, err := s.Load(context.Background(), "nonexistent")
	assert.Error(t, err)
}

func TestInMemoryStore_Latest(t *testing.T) {
	s := NewInMemoryStore(20)
	ctx := context.Background()

	_ = s.Save(ctx, &core.Checkpoint{ID: "cp-1", SessionID: "s1", StepIndex: 1})
	_ = s.Save(ctx, &core.Checkpoint{ID: "cp-3", SessionID: "s1", StepIndex: 3})
	_ = s.Save(ctx, &core.Checkpoint{ID: "cp-2", SessionID: "s1", StepIndex: 2})

	latest, err := s.Latest(ctx, "s1")
	require.NoError(t, err)
	assert.Equal(t, 3, latest.StepIndex)
	assert.Equal(t, "cp-3", latest.ID)
}

func TestInMemoryStore_LatestNoCheckpoints(t *testing.T) {
	s := NewInMemoryStore(20)
	_, err := s.Latest(context.Background(), "nope")
	assert.Error(t, err)
}

func TestInMemoryStore_Upsert(t *testing.T) {
	s := NewInMemoryStore(20)
	ctx := context.Background()

	_ = s.Save(ctx, &core.Checkpoint{ID: "cp-1", SessionID: "s1", StepIndex: 1,
		Messages: []core.Message{{Role: "user", Content: "v1"}}})
	_ = s.Save(ctx, &core.Checkpoint{ID: "cp-1-updated", SessionID: "s1", StepIndex: 1,
		Messages: []core.Message{{Role: "user", Content: "v2"}}})

	latest, err := s.Latest(ctx, "s1")
	require.NoError(t, err)
	assert.Equal(t, "v2", latest.Messages[0].Content)
}

func TestInMemoryStore_MaxPerSession(t *testing.T) {
	s := NewInMemoryStore(3) // keep only 3
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		_ = s.Save(ctx, &core.Checkpoint{
			ID: fmt.Sprintf("cp-%d", i), SessionID: "s1", StepIndex: i,
		})
	}

	latest, err := s.Latest(ctx, "s1")
	require.NoError(t, err)
	assert.Equal(t, 4, latest.StepIndex) // last one saved
}

func TestInMemoryStore_RequiredFields(t *testing.T) {
	s := NewInMemoryStore(20)
	ctx := context.Background()

	err := s.Save(ctx, &core.Checkpoint{SessionID: "s1"})
	assert.Error(t, err) // missing ID

	err = s.Save(ctx, &core.Checkpoint{ID: "cp-1"})
	assert.Error(t, err) // missing SessionID
}
