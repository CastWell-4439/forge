package cdc

import (
	"testing"

	"github.com/jackc/pglogrepl"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTupleToMap(t *testing.T) {
	rel := &pglogrepl.RelationMessageV2{}
	rel.Columns = []*pglogrepl.RelationMessageColumn{
		{Name: "id"},
		{Name: "name"},
		{Name: "status"},
	}

	tuple := &pglogrepl.TupleData{
		Columns: []*pglogrepl.TupleDataColumn{
			{DataType: 't', Data: []byte("42")},
			{DataType: 't', Data: []byte("alice")},
			{DataType: 'n'}, // null
		},
	}

	result := tupleToMap(rel, tuple)
	assert.Equal(t, "42", result["id"])
	assert.Equal(t, "alice", result["name"])
	assert.Nil(t, result["status"])
}

func TestTupleToMap_Nil(t *testing.T) {
	rel := &pglogrepl.RelationMessageV2{}
	result := tupleToMap(rel, nil)
	assert.Nil(t, result)
}

func TestTupleToMap_UnchangedToast(t *testing.T) {
	rel := &pglogrepl.RelationMessageV2{}
	rel.Columns = []*pglogrepl.RelationMessageColumn{
		{Name: "big_col"},
	}
	tuple := &pglogrepl.TupleData{
		Columns: []*pglogrepl.TupleDataColumn{
			{DataType: 'u'},
		},
	}
	result := tupleToMap(rel, tuple)
	assert.Equal(t, "(unchanged)", result["big_col"])
}

func TestProcessWALData_RelationMessage(t *testing.T) {
	s := &PGWALSource{
		config:      SourceConfig{Table: "test"},
		relationMap: make(map[uint32]*pglogrepl.RelationMessageV2),
	}

	// Build a RelationMessageV2 and encode it.
	relMsg := &pglogrepl.RelationMessageV2{}
	relMsg.RelationID = 1001
	relMsg.RelationName = "tasks"
	relMsg.Columns = []*pglogrepl.RelationMessageColumn{
		{Name: "id"},
		{Name: "status"},
	}

	// After processing, the relation should be cached.
	s.relationMap[1001] = relMsg
	assert.NotNil(t, s.relationMap[1001])
	assert.Equal(t, "tasks", s.relationMap[1001].RelationName)
}

func TestMustJSON(t *testing.T) {
	result := mustJSON(map[string]interface{}{"key": "value"})
	require.NotNil(t, result)
	assert.Contains(t, string(result), "key")

	// nil input
	assert.Nil(t, mustJSON(nil))
}

func TestMatchesFilter_WALSource(t *testing.T) {
	s := &PGWALSource{
		config: SourceConfig{Filter: "status = 'active'"},
	}

	event := Event{
		NewData: map[string]interface{}{"status": "active"},
	}
	assert.True(t, s.matchesFilter(event))

	event.NewData["status"] = "inactive"
	assert.False(t, s.matchesFilter(event))
}

func TestMatchesFilter_NoFilter(t *testing.T) {
	s := &PGWALSource{
		config: SourceConfig{},
	}
	assert.True(t, s.matchesFilter(Event{}))
}

func TestNewPGWALSource_Defaults(t *testing.T) {
	src := NewPGWALSource("postgres://localhost/test?replication=database",
		SourceConfig{Table: "events"},
		"forge_pub",
	)

	assert.Equal(t, "forge_cdc", src.slotName)
	assert.Equal(t, "forge_pub", src.publication)
	assert.True(t, src.autoCreateSlot)
	assert.NotNil(t, src.relationMap)
}

func TestNewPGWALSource_CustomSlot(t *testing.T) {
	src := NewPGWALSource("postgres://localhost/test?replication=database",
		SourceConfig{Table: "events", SlotName: "my_slot"},
		"my_pub",
		WithAutoCreateSlot(false),
	)

	assert.Equal(t, "my_slot", src.slotName)
	assert.Equal(t, "my_pub", src.publication)
	assert.False(t, src.autoCreateSlot)
}

func TestClose_Idempotent(t *testing.T) {
	src := NewPGWALSource("postgres://localhost/test",
		SourceConfig{},
		"pub",
	)
	assert.NoError(t, src.Close())
	assert.NoError(t, src.Close()) // second close should not panic
}
