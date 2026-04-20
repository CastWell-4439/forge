package coordinator

import (
	"context"
	"fmt"
	"log"
	"time"
)

// KueueConfig holds configuration for Kueue integration.
type KueueConfig struct {
	Enabled    bool
	Namespace  string
	QueueName  string
	DefaultTTL time.Duration
}

// DefaultKueueConfig returns default Kueue settings.
func DefaultKueueConfig() KueueConfig {
	return KueueConfig{
		Enabled:    false,
		Namespace:  "forge",
		QueueName:  "forge-local-queue",
		DefaultTTL: 1 * time.Hour,
	}
}

// KueueJobSpec represents a Kueue-managed K8s Job for GPU tasks.
type KueueJobSpec struct {
	Name        string
	Namespace   string
	QueueName   string
	WorkflowID  string
	TaskID      string
	Image       string
	Command     []string
	GPUCount    int
	GPUModel    string // "A100", "T4", etc.
	CPURequest  string
	MemRequest  string
	TTL         time.Duration
	Labels      map[string]string
	Annotations map[string]string
}

// KueueSubmitter defines the interface for submitting jobs to Kueue.
// Real implementation uses k8s client-go; mock returns nil for testing.
type KueueSubmitter interface {
	SubmitJob(ctx context.Context, spec KueueJobSpec) error
	GetJobStatus(ctx context.Context, namespace, name string) (KueueJobStatus, error)
	CancelJob(ctx context.Context, namespace, name string) error
}

// KueueJobStatus represents the status of a Kueue-managed job.
type KueueJobStatus struct {
	Phase      string // "Pending", "Admitted", "Running", "Succeeded", "Failed"
	Message    string
	StartTime  *time.Time
	FinishTime *time.Time
}

// KueueManager manages GPU task submission through Kueue.
type KueueManager struct {
	config    KueueConfig
	submitter KueueSubmitter
}

// NewKueueManager creates a new KueueManager.
func NewKueueManager(config KueueConfig, submitter KueueSubmitter) *KueueManager {
	return &KueueManager{
		config:    config,
		submitter: submitter,
	}
}

// SubmitGPUTask creates a Kueue-managed K8s Job for a GPU task.
func (m *KueueManager) SubmitGPUTask(ctx context.Context, workflowID, taskID string, taskDef TaskDef) error {
	if !m.config.Enabled {
		return fmt.Errorf("kueue: not enabled")
	}
	if m.submitter == nil {
		return fmt.Errorf("kueue: submitter not configured")
	}

	gpuCount := 1
	gpuModel := "T4"
	if taskDef.Params != nil {
		if model, ok := taskDef.Params["gpu.model"].(string); ok {
			gpuModel = model
		}
	}

	ttl := m.config.DefaultTTL
	if taskDef.Timeout > 0 {
		ttl = taskDef.Timeout
	}

	// Image and command come from task params (convention: "image", "command").
	image := ""
	if img, ok := taskDef.Params["image"].(string); ok {
		image = img
	}

	spec := KueueJobSpec{
		Name:       fmt.Sprintf("forge-%s-%s", truncateID(workflowID, 8), truncateID(taskID, 8)),
		Namespace:  m.config.Namespace,
		QueueName:  m.config.QueueName,
		WorkflowID: workflowID,
		TaskID:     taskID,
		Image:      image,
		GPUCount:   gpuCount,
		GPUModel:   gpuModel,
		CPURequest: "2",
		MemRequest: "4Gi",
		TTL:        ttl,
		Labels: map[string]string{
			"forge.io/workflow-id": workflowID,
			"forge.io/task-id":    taskID,
			"forge.io/handler":    taskDef.Handler,
		},
		Annotations: map[string]string{
			"kueue.x-k8s.io/queue-name": m.config.QueueName,
		},
	}

	log.Printf("INFO: kueue: submitting GPU task %s/%s (gpu=%s×%d, ttl=%v)",
		truncateID(workflowID, 8), truncateID(taskID, 8), gpuModel, gpuCount, ttl)

	return m.submitter.SubmitJob(ctx, spec)
}

// truncateID safely truncates an ID string.
func truncateID(id string, maxLen int) string {
	if len(id) <= maxLen {
		return id
	}
	return id[:maxLen]
}

// IsGPUTask checks whether a task requires GPU resources.
func IsGPUTask(taskDef TaskDef) bool {
	if taskDef.Params == nil {
		return false
	}
	_, hasGPU := taskDef.Params["gpu.required"]
	return hasGPU
}

// MockKueueSubmitter is a no-op submitter for testing.
type MockKueueSubmitter struct {
	Submitted []KueueJobSpec
}

func (m *MockKueueSubmitter) SubmitJob(_ context.Context, spec KueueJobSpec) error {
	m.Submitted = append(m.Submitted, spec)
	return nil
}

func (m *MockKueueSubmitter) GetJobStatus(_ context.Context, _, _ string) (KueueJobStatus, error) {
	return KueueJobStatus{Phase: "Succeeded"}, nil
}

func (m *MockKueueSubmitter) CancelJob(_ context.Context, _, _ string) error {
	return nil
}
