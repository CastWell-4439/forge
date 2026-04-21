// Package controllers implements the Forge Operator reconciliation logic.
package controllers

import (
	"context"
	"fmt"
	"log"
	"time"

	v1 "github.com/castwell/forge/operator/api/v1"
)

// ForgeClusterReconciler reconciles a ForgeCluster object.
type ForgeClusterReconciler struct {
	// In a real operator, this would hold:
	// - client.Client (k8s API client)
	// - runtime.Scheme
	// - record.EventRecorder
	// For now, it's a scaffold with the reconciliation skeleton.
}

// ReconcileResult represents the outcome of a reconcile call.
type ReconcileResult struct {
	Requeue      bool
	RequeueAfter time.Duration
}

// Reconcile is the main reconciliation loop.
// It ensures the actual cluster state matches the desired ForgeCluster spec.
func (r *ForgeClusterReconciler) Reconcile(ctx context.Context, cluster *v1.ForgeClusterSpec, status *v1.ForgeClusterStatus) (ReconcileResult, error) {
	log.Printf("INFO: reconciling ForgeCluster (version=%s, coordinators=%d, workers=%d)",
		cluster.Version, cluster.Coordinator.Replicas, len(cluster.Workers))

	// Step 1: Ensure storage backends are ready.
	if err := r.reconcileStorage(ctx, cluster); err != nil {
		status.Phase = v1.ClusterPhasePending
		status.Message = fmt.Sprintf("waiting for storage: %v", err)
		return ReconcileResult{Requeue: true, RequeueAfter: 10 * time.Second}, nil
	}

	// Step 2: Reconcile Coordinator StatefulSet.
	if err := r.reconcileCoordinator(ctx, cluster); err != nil {
		status.Phase = v1.ClusterPhaseDegraded
		status.Message = fmt.Sprintf("coordinator error: %v", err)
		return ReconcileResult{Requeue: true, RequeueAfter: 15 * time.Second}, nil
	}

	// Step 3: Reconcile Worker Deployments.
	for _, pool := range cluster.Workers {
		if err := r.reconcileWorkerPool(ctx, pool); err != nil {
			status.Phase = v1.ClusterPhaseDegraded
			status.Message = fmt.Sprintf("worker pool %q error: %v", pool.Name, err)
			return ReconcileResult{Requeue: true, RequeueAfter: 15 * time.Second}, nil
		}
	}

	// Step 4: Update status.
	now := time.Now()
	status.Phase = v1.ClusterPhaseRunning
	status.Message = "all components healthy"
	status.LastReconcileTime = &now

	return ReconcileResult{RequeueAfter: 60 * time.Second}, nil
}

// reconcileStorage ensures storage backends (PG, Redis, etcd) are accessible.
// Production implementation requires client-go and actual connection probes.
func (r *ForgeClusterReconciler) reconcileStorage(_ context.Context, spec *v1.ForgeClusterSpec) error {
	// Validate required fields before attempting health checks.
	if spec.Storage.PostgreSQL.DSN == "" && !spec.Storage.PostgreSQL.External {
		return fmt.Errorf("postgres DSN not configured and not marked as external")
	}
	if spec.Storage.Redis.Address == "" && !spec.Storage.Redis.External {
		return fmt.Errorf("redis address not configured and not marked as external")
	}
	// When client-go is integrated:
	// 1. For external backends: dial TCP to verify connectivity + run ping.
	// 2. For managed backends: check StatefulSet readiness via k8s API.
	log.Printf("INFO: storage backends validated (postgres + redis configured)")
	return nil
}

// reconcileCoordinator ensures the Coordinator StatefulSet matches desired state.
// Production implementation requires client-go for StatefulSet CRUD.
func (r *ForgeClusterReconciler) reconcileCoordinator(_ context.Context, spec *v1.ForgeClusterSpec) error {
	// Validate coordinator config.
	if spec.Coordinator.Replicas <= 0 {
		return fmt.Errorf("coordinator replicas must be > 0, got %d", spec.Coordinator.Replicas)
	}
	if spec.Version == "" {
		return fmt.Errorf("cluster version not specified")
	}
	// When client-go is integrated:
	// 1. Get or create StatefulSet "forge-coordinator"
	// 2. Compare spec (replicas, image tag from spec.Version, resources from spec.Coordinator.Resources)
	// 3. Update if drift detected
	// 4. Wait for rollout if updated
	log.Printf("INFO: coordinator reconciled (replicas=%d, version=%s)",
		spec.Coordinator.Replicas, spec.Version)
	return nil
}

// reconcileWorkerPool ensures a Worker Deployment matches desired state.
// Production implementation requires client-go for Deployment + HPA CRUD.
func (r *ForgeClusterReconciler) reconcileWorkerPool(_ context.Context, pool v1.WorkerPoolSpec) error {
	// Validate worker pool config.
	if pool.Name == "" {
		return fmt.Errorf("worker pool name is required")
	}
	if pool.Replicas <= 0 {
		return fmt.Errorf("worker pool %q replicas must be > 0, got %d", pool.Name, pool.Replicas)
	}
	// When client-go is integrated:
	// 1. Get or create Deployment "forge-worker-{pool.Name}"
	// 2. Compare spec (replicas, image, resources, GPU requests from pool.Resources)
	// 3. Create/update HPA if pool.MinReplicas/MaxReplicas set
	// 4. Update if drift detected
	log.Printf("INFO: reconciling worker pool %q (lang=%s, replicas=%d)",
		pool.Name, pool.Language, pool.Replicas)
	return nil
}
