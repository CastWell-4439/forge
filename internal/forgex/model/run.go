package model

import "time"

// RunStatus describes the lifecycle state of a ForgeX run.
type RunStatus string

const (
	RunPending   RunStatus = "pending"
	RunRunning   RunStatus = "running"
	RunSucceeded RunStatus = "succeeded"
	RunFailed    RunStatus = "failed"
	RunStopped   RunStatus = "stopped"
	RunEscalated RunStatus = "escalated"
	RunPaused    RunStatus = "paused"
)

// Run is the top-level execution record for one ForgeX harness run.
type Run struct {
	ID        string    `json:"id" yaml:"id"`
	TaskID    string    `json:"task_id" yaml:"task_id"`
	Name      string    `json:"name" yaml:"name"`
	Status    RunStatus `json:"status" yaml:"status"`
	StartedAt time.Time `json:"started_at" yaml:"started_at"`
	EndedAt   time.Time `json:"ended_at,omitempty" yaml:"ended_at,omitempty"`
	Summary   string    `json:"summary,omitempty" yaml:"summary,omitempty"`
}
