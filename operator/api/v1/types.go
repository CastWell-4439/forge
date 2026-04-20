// Package v1 contains API types for the Forge Operator.
package v1

import "time"

// ForgeClusterSpec defines the desired state of a ForgeCluster.
type ForgeClusterSpec struct {
	// Version is the Forge version to deploy.
	Version string `json:"version"`

	// Coordinator defines coordinator settings.
	Coordinator CoordinatorSpec `json:"coordinator"`

	// Workers defines worker pools.
	Workers []WorkerPoolSpec `json:"workers"`

	// Storage defines storage backends.
	Storage StorageSpec `json:"storage"`
}

// CoordinatorSpec configures the Coordinator StatefulSet.
type CoordinatorSpec struct {
	Replicas  int32             `json:"replicas"`
	Image     string            `json:"image,omitempty"`
	Resources ResourceSpec      `json:"resources,omitempty"`
	Labels    map[string]string `json:"labels,omitempty"`
}

// WorkerPoolSpec configures a pool of workers.
type WorkerPoolSpec struct {
	Name      string            `json:"name"`
	Language  string            `json:"language"` // "go", "python", "cpp"
	Replicas  int32             `json:"replicas"`
	Image     string            `json:"image,omitempty"`
	Resources ResourceSpec      `json:"resources,omitempty"`
	Labels    map[string]string `json:"labels,omitempty"`
	HPA       *HPASpec          `json:"hpa,omitempty"`
}

// HPASpec configures HorizontalPodAutoscaler.
type HPASpec struct {
	MinReplicas int32 `json:"minReplicas"`
	MaxReplicas int32 `json:"maxReplicas"`
	TargetCPU   int32 `json:"targetCPU"`
}

// StorageSpec defines storage backend configuration.
type StorageSpec struct {
	PostgreSQL PostgreSQLSpec `json:"postgresql"`
	Redis      RedisSpec      `json:"redis"`
	Etcd       EtcdSpec       `json:"etcd"`
}

// PostgreSQLSpec configures PostgreSQL.
type PostgreSQLSpec struct {
	DSN      string `json:"dsn,omitempty"`
	External bool   `json:"external"` // Use external PG (don't deploy)
}

// RedisSpec configures Redis.
type RedisSpec struct {
	Address  string `json:"address,omitempty"`
	External bool   `json:"external"`
}

// EtcdSpec configures etcd.
type EtcdSpec struct {
	Endpoints []string `json:"endpoints,omitempty"`
	External  bool     `json:"external"`
}

// ResourceSpec defines CPU/Memory resource requests and limits.
type ResourceSpec struct {
	CPURequest    string `json:"cpuRequest,omitempty"`
	CPULimit      string `json:"cpuLimit,omitempty"`
	MemoryRequest string `json:"memoryRequest,omitempty"`
	MemoryLimit   string `json:"memoryLimit,omitempty"`
}

// ForgeClusterStatus defines the observed state of a ForgeCluster.
type ForgeClusterStatus struct {
	// Phase is the overall cluster phase.
	Phase ClusterPhase `json:"phase"`

	// Message provides human-readable status.
	Message string `json:"message,omitempty"`

	// ReadyCoordinators is the number of ready coordinator pods.
	ReadyCoordinators int32 `json:"readyCoordinators"`

	// ReadyWorkers is the total number of ready worker pods.
	ReadyWorkers int32 `json:"readyWorkers"`

	// LastReconcileTime is the last time the operator reconciled.
	LastReconcileTime *time.Time `json:"lastReconcileTime,omitempty"`

	// Conditions represent detailed status conditions.
	Conditions []Condition `json:"conditions,omitempty"`
}

// ClusterPhase represents the lifecycle phase of a ForgeCluster.
type ClusterPhase string

const (
	ClusterPhasePending  ClusterPhase = "Pending"
	ClusterPhaseRunning  ClusterPhase = "Running"
	ClusterPhaseDegraded ClusterPhase = "Degraded"
	ClusterPhaseFailed   ClusterPhase = "Failed"
)

// Condition represents a detailed status condition.
type Condition struct {
	Type    string `json:"type"`
	Status  string `json:"status"` // "True", "False", "Unknown"
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}
