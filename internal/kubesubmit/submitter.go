// Package kubesubmit is the real KueueSubmitter: it creates, reads and
// deletes the Kubernetes Jobs that Kueue queues — the client-go half the
// coordinator's KueueManager was defined against but never received.
//
// The integration follows Kueue's standard Job pattern: a suspended Job
// carrying the kueue.x-k8s.io/queue-name LABEL is admitted (and un-suspended)
// by Kueue when quota allows, then runs like any Job. Queueing and priority
// preemption stay Kueue's job; this package only has to express the task as
// a correct Job.
package kubesubmit

import (
	"context"
	"fmt"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/castwell/forge/internal/coordinator"
)

// Labels and settings shared with deploy/kueue/kueue-config.yaml: the queue
// name must match the LocalQueue, and the GPU node selector must match the
// ResourceFlavor nodeLabels (gpu.nvidia.com/model: A100|T4), otherwise the
// Job is admitted to the queue but can never be placed.
const (
	queueLabel      = "kueue.x-k8s.io/queue-name"
	admittedKey     = "kueue.x-k8s.io/job-admitted"
	gpuNodeLabel    = "gpu.nvidia.com/model"
	gpuResourceName = corev1.ResourceName("nvidia.com/gpu")
	kubeconfigEnv   = "FORGE_KUBECONFIG"
)

// Submitter implements coordinator.KueueSubmitter with a real API client.
type Submitter struct {
	client kubernetes.Interface
}

// NewSubmitter wraps an already-built client (assembly and tests inject the
// one they have — tests use client-go's in-memory fake, no cluster needed).
func NewSubmitter(client kubernetes.Interface) *Submitter {
	return &Submitter{client: client}
}

// NewSubmitterFromKubeconfig builds a client from an explicit kubeconfig
// path (FORGE_KUBECONFIG) or, failing that, the in-cluster service account.
// It probes the API server once: a configured-but-unreachable cluster fails
// here, at construction, instead of at the first GPU task.
func NewSubmitterFromKubeconfig(kubeconfig string) (*Submitter, error) {
	var (
		cfg *rest.Config
		err error
	)
	if kubeconfig != "" {
		cfg, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			return nil, fmt.Errorf("kubesubmit: kubeconfig %s: %w", kubeconfig, err)
		}
	} else {
		cfg, err = rest.InClusterConfig()
		if err != nil {
			return nil, fmt.Errorf("kubesubmit: no %s set and not running in cluster: %w", kubeconfigEnv, err)
		}
	}
	cfg.Timeout = 5 * time.Second

	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("kubesubmit: build client: %w", err)
	}
	if _, err := client.Discovery().ServerVersion(); err != nil {
		return nil, fmt.Errorf("kubesubmit: api server unreachable: %w", err)
	}
	return &Submitter{client: client}, nil
}

// SubmitJob creates the suspended, queue-labelled Job.
func (s *Submitter) SubmitJob(ctx context.Context, spec coordinator.KueueJobSpec) error {
	if spec.Image == "" {
		return fmt.Errorf("kubesubmit: job %s: image is required (task param \"image\")", spec.Name)
	}
	if spec.QueueName == "" {
		// An empty queue label is a Job Kueue will never admit — it would
		// hang silently. Refuse at the door instead.
		return fmt.Errorf("kubesubmit: job %s: queue name is required", spec.Name)
	}
	ns := spec.Namespace
	if ns == "" {
		ns = "default"
	}

	labels := make(map[string]string, len(spec.Labels)+2)
	for k, v := range spec.Labels {
		labels[k] = v
	}
	labels[queueLabel] = spec.QueueName

	annotations := make(map[string]string, len(spec.Annotations))
	for k, v := range spec.Annotations {
		annotations[k] = v
	}

	suspend := true
	ttlSeconds := int32(0)
	if spec.TTL > 0 {
		ttlSeconds = int32(spec.TTL.Seconds())
	}

	requests := corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse(orDefault(spec.CPURequest, "2")),
		corev1.ResourceMemory: resource.MustParse(orDefault(spec.MemRequest, "4Gi")),
	}
	limits := corev1.ResourceList{
		corev1.ResourceCPU:    requests[corev1.ResourceCPU],
		corev1.ResourceMemory: requests[corev1.ResourceMemory],
	}
	if spec.GPUCount > 0 {
		gpu, err := resource.ParseQuantity(fmt.Sprintf("%d", spec.GPUCount))
		if err != nil {
			return fmt.Errorf("kubesubmit: job %s: gpu count: %w", spec.Name, err)
		}
		// GPU resources require requests == limits.
		requests[gpuResourceName] = gpu
		limits[gpuResourceName] = gpu
	}

	container := corev1.Container{
		Name:      "main",
		Image:     spec.Image,
		Command:   spec.Command,
		Resources: corev1.ResourceRequirements{Requests: requests, Limits: limits},
	}
	podSpec := corev1.PodSpec{
		RestartPolicy: corev1.RestartPolicyNever,
		Containers:    []corev1.Container{container},
	}
	if spec.GPUModel != "" {
		// Must match the ResourceFlavor nodeLabels in deploy/kueue-config.yaml.
		podSpec.NodeSelector = map[string]string{gpuNodeLabel: spec.GPUModel}
	}

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:        spec.Name,
			Namespace:   ns,
			Labels:      labels,
			Annotations: annotations,
		},
		Spec: batchv1.JobSpec{
			Suspend:                 &suspend,
			TTLSecondsAfterFinished: jobTTL(ttlSeconds),
			Template: corev1.PodTemplateSpec{
				Spec: podSpec,
			},
		},
	}

	if _, err := s.client.BatchV1().Jobs(ns).Create(ctx, job, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("kubesubmit: create job %s/%s: %w", ns, spec.Name, err)
	}
	return nil
}

// GetJobStatus maps Job lifecycle onto KueueManager's phase vocabulary.
func (s *Submitter) GetJobStatus(ctx context.Context, namespace, name string) (coordinator.KueueJobStatus, error) {
	job, err := s.client.BatchV1().Jobs(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return coordinator.KueueJobStatus{}, fmt.Errorf("kubesubmit: get job %s/%s: %w", namespace, name, err)
	}

	out := coordinator.KueueJobStatus{Phase: "Pending"}
	for _, cond := range job.Status.Conditions {
		switch cond.Type {
		case batchv1.JobComplete:
			if cond.Status == corev1.ConditionTrue {
				out.Phase = "Succeeded"
				out.FinishTime = metaTimePtr(job.Status.CompletionTime)
			}
		case batchv1.JobFailed:
			if cond.Status == corev1.ConditionTrue {
				out.Phase = "Failed"
				out.Message = fmt.Sprintf("%s: %s", cond.Reason, cond.Message)
				out.FinishTime = metaTimePtr(job.Status.CompletionTime)
			}
		}
	}
	if out.Phase == "Pending" {
		admitted := job.Labels[admittedKey] == "true" || job.Annotations[admittedKey] == "true"
		suspended := job.Spec.Suspend != nil && *job.Spec.Suspend
		// Precedence: started beats everything else in this branch. Kueue
		// un-suspends on admission, so !suspended + no start time yet is the
		// Admitted window; still suspended is Pending (waiting for quota).
		switch {
		case job.Status.StartTime != nil:
			out.Phase = "Running"
		case !suspended || admitted:
			out.Phase = "Admitted"
		}
	}
	out.StartTime = metaTimePtr(job.Status.StartTime)
	return out, nil
}

// CancelJob deletes the Job (foreground so its pods go too). A job that is
// already gone counts as cancelled — cancel is idempotent by contract.
func (s *Submitter) CancelJob(ctx context.Context, namespace, name string) error {
	policy := metav1.DeletePropagationBackground
	if err := s.client.BatchV1().Jobs(namespace).Delete(ctx, name, metav1.DeleteOptions{
		PropagationPolicy: &policy,
	}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("kubesubmit: cancel job %s/%s: %w", namespace, name, err)
	}
	return nil
}

func jobTTL(seconds int32) *int32 {
	if seconds <= 0 {
		return nil
	}
	return &seconds
}

func metaTimePtr(t *metav1.Time) *time.Time {
	if t == nil {
		return nil
	}
	tt := t.Time
	return &tt
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
