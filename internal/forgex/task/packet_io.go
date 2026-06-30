package task

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/castwell/forge/internal/forgex/model"
	"gopkg.in/yaml.v3"
)

// LoadPacket reads a TaskPacket YAML file and normalizes compatible aliases.
func LoadPacket(path string) (model.TaskPacket, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return model.TaskPacket{}, fmt.Errorf("read task packet %s: %w", path, err)
	}
	packet, err := parsePacketYAML(data)
	if err != nil {
		return model.TaskPacket{}, fmt.Errorf("parse task packet %s: %w", path, err)
	}
	packet = NormalizePacket(packet)
	if err := ValidatePacket(packet); err != nil {
		return model.TaskPacket{}, fmt.Errorf("validate task packet %s: %w", path, err)
	}
	return packet, nil
}

type packetYAML struct {
	ID          string            `json:"id" yaml:"id"`
	Name        string            `json:"name" yaml:"name"`
	Title       string            `json:"title" yaml:"title"`
	Goal        string            `json:"goal" yaml:"goal"`
	Inputs      map[string]any    `json:"inputs" yaml:"inputs"`
	Constraints []string          `json:"constraints,omitempty" yaml:"constraints,omitempty"`
	Success     []string          `json:"success,omitempty" yaml:"success,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty" yaml:"metadata,omitempty"`
}

type packetFile struct {
	packetYAML `yaml:",inline"`
	TaskPacket packetYAML `json:"task_packet" yaml:"task_packet"`
}

func parsePacketYAML(data []byte) (model.TaskPacket, error) {
	var file packetFile
	if err := yaml.Unmarshal(data, &file); err != nil {
		return model.TaskPacket{}, err
	}
	packet := file.packetYAML.toModel()
	if packet.ID == "" && packet.Goal == "" {
		packet = file.TaskPacket.toModel()
	}
	return packet, nil
}

func (p packetYAML) toModel() model.TaskPacket {
	name := p.Name
	if name == "" {
		name = p.Title
	}
	return model.TaskPacket{
		ID:          p.ID,
		Name:        name,
		Goal:        p.Goal,
		Inputs:      p.Inputs,
		Constraints: p.Constraints,
		Success:     p.Success,
		Metadata:    p.Metadata,
	}
}

// SavePacket writes a TaskPacket as YAML, creating parent directories.
func SavePacket(path string, packet model.TaskPacket) error {
	packet = NormalizePacket(packet)
	if err := ValidatePacket(packet); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := yaml.Marshal(packet)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// NormalizePacket fills compatible field aliases used by early ForgeX examples.
func NormalizePacket(packet model.TaskPacket) model.TaskPacket {
	if packet.Name == "" && packet.Metadata != nil {
		packet.Name = packet.Metadata["title"]
	}
	packet.ID = strings.TrimSpace(packet.ID)
	packet.Name = strings.TrimSpace(packet.Name)
	packet.Goal = strings.TrimSpace(packet.Goal)
	return packet
}

// ValidatePacket checks the minimal fields required by the local harness.
func ValidatePacket(packet model.TaskPacket) error {
	if strings.TrimSpace(packet.ID) == "" {
		return fmt.Errorf("id is required")
	}
	if strings.TrimSpace(packet.Goal) == "" {
		return fmt.Errorf("goal is required")
	}
	return nil
}
