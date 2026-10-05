package scheduler

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The declared dedup key must shape the event identity: two polls of the same
// work item produce the same ID (deduplicated), a different item a different
// one. Before this, DedupKey was parsed, compiled, and ignored.
func TestPollTriggerDedupKeyShapesEventIDs(t *testing.T) {
	payloads := []map[string]any{
		{"work_item_id": "wi-1", "title": "first"},
		{"work_item_id": "wi-2", "title": "second"},
	}
	poll := NewPollTrigger(PollTriggerConfig{
		Name:         "t",
		Source:       "feishu_mcp",
		DedupKey:     "work_item_id",
		WorkflowName: "wf",
		PollFn: func(_ context.Context, _, _ string) ([]Event, error) {
			events := make([]Event, 0, len(payloads))
			for i, p := range payloads {
				// Without the key the poller's own ID would be used; assert it
				// is overridden by the declared field.
				events = append(events, Event{ID: autoID(i), Payload: p})
			}
			return events, nil
		},
	})

	events, err := poll.Check(context.Background())
	require.NoError(t, err)
	require.Len(t, events, 2)
	assert.Equal(t, "wi-1", events[0].ID, "the declared field becomes the dedup identity")
	assert.Equal(t, "wi-2", events[1].ID)
}

// Without a declared key the poller's own IDs pass through untouched.
func TestPollTriggerWithoutDedupKeyKeepsIDs(t *testing.T) {
	poll := NewPollTrigger(PollTriggerConfig{
		Name:         "t",
		Source:       "feishu_mcp",
		WorkflowName: "wf",
		PollFn: func(_ context.Context, _, _ string) ([]Event, error) {
			return []Event{{ID: "native-1"}}, nil
		},
	})

	events, err := poll.Check(context.Background())
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, "native-1", events[0].ID)
}

// A payload missing the declared key keeps the original ID instead of
// producing an empty identity (which would dedup everything together).
func TestPollTriggerMissingKeyFieldKeepsID(t *testing.T) {
	poll := NewPollTrigger(PollTriggerConfig{
		Name:         "t",
		Source:       "feishu_mcp",
		DedupKey:     "not_there",
		WorkflowName: "wf",
		PollFn: func(_ context.Context, _, _ string) ([]Event, error) {
			return []Event{{ID: "keep-me"}}, nil
		},
	})

	events, err := poll.Check(context.Background())
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, "keep-me", events[0].ID)
}

// autoID fakes a poller-native identifier.
func autoID(i int) string { return "poll-native-" + string(rune('a'+i)) }
