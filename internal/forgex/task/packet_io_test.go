package task

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/castwell/forge/internal/forgex/model"
)

func TestLoadPacketValidatesAndNormalizes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "task.yaml")
	packet := model.TaskPacket{
		ID:   " task_1 ",
		Name: " demo ",
		Goal: " do the thing ",
		Inputs: map[string]any{
			"x": 1,
		},
	}
	if err := SavePacket(path, packet); err != nil {
		t.Fatalf("SavePacket() error = %v", err)
	}

	got, err := LoadPacket(path)
	if err != nil {
		t.Fatalf("LoadPacket() error = %v", err)
	}
	if got.ID != "task_1" || got.Name != "demo" || got.Goal != "do the thing" {
		t.Fatalf("packet not normalized: %+v", got)
	}
	if got.Inputs["x"] == nil {
		t.Fatalf("inputs not preserved: %+v", got.Inputs)
	}
}

func TestLoadPacketSupportsWrappedTaskPacketAndTitleAlias(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wrapped.yaml")
	content := []byte(`task_packet:
  id: task_wrapped
  title: Wrapped title
  goal: Wrapped goal
  constraints:
    - keep it local
`)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write wrapped packet: %v", err)
	}
	got, err := LoadPacket(path)
	if err != nil {
		t.Fatalf("LoadPacket() error = %v", err)
	}
	if got.ID != "task_wrapped" || got.Name != "Wrapped title" || got.Goal != "Wrapped goal" {
		t.Fatalf("wrapped packet mismatch: %+v", got)
	}
}

func TestValidatePacketRequiresIDAndGoal(t *testing.T) {
	if err := ValidatePacket(model.TaskPacket{Goal: "goal"}); err == nil {
		t.Fatalf("ValidatePacket() expected missing id error")
	}
	if err := ValidatePacket(model.TaskPacket{ID: "task"}); err == nil {
		t.Fatalf("ValidatePacket() expected missing goal error")
	}
}
