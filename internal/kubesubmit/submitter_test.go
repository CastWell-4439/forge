package kubesubmit

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"github.com/castwell/forge/internal/coordinator"
)

func testSpec() coordinator.KueueJobSpec {
	return coordinator.KueueJobSpec{
		Name:       "forge-abc12345-task67890123",
		Namespace:  "forge",
		QueueName:  "forge-local-queue",
		WorkflowID: "wf-1",
		TaskID:     "task-1",
		Image:      "python:3.12",
		Command:    []string{"python", "-c", "print(1)"},
		GPUCount:   1,
		GPUModel:   "T4",
		CPURequest: "2",
		MemRequest: "4Gi",
		TTL:        time.Hour,
		Labels: map[string]string{
			"forge.io/workflow-id": "wf-1",
			"forge.io/task-id":     "task-1",
		},
	}
}

// The whole point of the real submitter: the Job it creates carries the
// Kueue queue LABEL (not annotation), starts suspended, and asks for the
// resources the deploy/kueue config matches on.
func TestSubmitJobCreatesQueueLabelledSuspendedJob(t *testing.T) {
	client := k8sfake.NewSimpleClientset()
	s := NewSubmitter(client)

	require.NoError(t, s.SubmitJob(context.Background(), testSpec()))

	job, err := client.BatchV1().Jobs("forge").Get(context.Background(),
		"forge-abc12345-task67890123", metav1.GetOptions{})
	require.NoError(t, err, "the Job must exist in the (fake) cluster")

	// Kueue selects the queue by label — the bug the old spec had (annotation).
	assert.Equal(t, "forge-local-queue", job.Labels[queueLabel])
	require.NotNil(t, job.Spec.Suspend)
	assert.True(t, *job.Spec.Suspend, "Job must start suspended for Kueue admission")

	// Alignment with deploy/kueue/kueue-config.yaml: ResourceFlavor matches
	// on the gpu.nvidia.com/model node label.
	assert.Equal(t, "T4", job.Spec.Template.Spec.NodeSelector[gpuNodeLabel])

	container := job.Spec.Template.Spec.Containers[0]
	assert.Equal(t, "python:3.12", container.Image)
	gpu := container.Resources.Requests[gpuResourceName]
	gpuLimit := container.Resources.Limits[gpuResourceName]
	assert.Equal(t, int64(1), gpu.Value())
	assert.Equal(t, gpu.Value(), gpuLimit.Value(),
		"GPU requests must equal limits")

	assert.Equal(t, "wf-1", job.Labels["forge.io/workflow-id"])
	require.NotNil(t, job.Spec.TTLSecondsAfterFinished)
	assert.Equal(t, int32(3600), *job.Spec.TTLSecondsAfterFinished)
}

// Refusals happen before anything reaches the API server: an image-less or
// queue-less Job would be rejected (or worse, silently unadmitted) later.
func TestSubmitJobRefusesIncompleteSpecs(t *testing.T) {
	client := k8sfake.NewSimpleClientset()
	s := NewSubmitter(client)

	spec := testSpec()
	spec.Image = ""
	err := s.SubmitJob(context.Background(), spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "image")

	spec = testSpec()
	spec.QueueName = ""
	err = s.SubmitJob(context.Background(), spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "queue name")

	// Nothing was created.
	jobs, err := client.BatchV1().Jobs("forge").List(context.Background(), metav1.ListOptions{})
	require.NoError(t, err)
	assert.Empty(t, jobs.Items)
}

// Status vocabulary: Pending while suspended, Admitted after un-suspend
// but before start, Running once started, Succeeded/Failed from conditions.
func TestGetJobStatusPhaseMapping(t *testing.T) {
	ctx := context.Background()

	newJob := func(mutate func(*batchv1.Job)) *batchv1.Job {
		job := &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{Name: "j", Namespace: "forge"},
			Spec:       batchv1.JobSpec{Suspend: boolPtr(true)},
		}
		mutate(job)
		return job
	}
	statusOf := func(t *testing.T, job *batchv1.Job) coordinator.KueueJobStatus {
		t.Helper()
		client := k8sfake.NewSimpleClientset(job)
		s := NewSubmitter(client)
		st, err := s.GetJobStatus(ctx, "forge", "j")
		require.NoError(t, err)
		return st
	}

	// Suspended, waiting for quota.
	assert.Equal(t, "Pending", statusOf(t, newJob(func(*batchv1.Job) {})).Phase)

	// Un-suspended (Kueue admission), no start time yet.
	assert.Equal(t, "Admitted", statusOf(t, newJob(func(j *batchv1.Job) {
		j.Spec.Suspend = boolPtr(false)
	})).Phase)

	// Started.
	started := metav1.NewTime(time.Now().Add(-time.Minute))
	assert.Equal(t, "Running", statusOf(t, newJob(func(j *batchv1.Job) {
		j.Spec.Suspend = boolPtr(false)
		j.Status.StartTime = &started
	})).Phase)

	// Completed.
	completed := metav1.NewTime(time.Now())
	succeeded := statusOf(t, newJob(func(j *batchv1.Job) {
		j.Spec.Suspend = boolPtr(false)
		j.Status.StartTime = &started
		j.Status.CompletionTime = &completed
		j.Status.Conditions = []batchv1.JobCondition{{
			Type: batchv1.JobComplete, Status: "True",
		}}
	}))
	assert.Equal(t, "Succeeded", succeeded.Phase)
	require.NotNil(t, succeeded.FinishTime)

	// Failed with a reason.
	failed := statusOf(t, newJob(func(j *batchv1.Job) {
		j.Spec.Suspend = boolPtr(false)
		j.Status.Conditions = []batchv1.JobCondition{{
			Type: batchv1.JobFailed, Status: "True",
			Reason: "BackoffLimitExceeded", Message: "Job has reached the specified backoff limit",
		}}
	}))
	assert.Equal(t, "Failed", failed.Phase)
	assert.Contains(t, failed.Message, "BackoffLimitExceeded")
}

// Cancel deletes the Job; cancelling a job that is already gone succeeds
// (idempotent), while other API errors surface.
func TestCancelJobIsIdempotent(t *testing.T) {
	ctx := context.Background()
	client := k8sfake.NewSimpleClientset(&batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "j", Namespace: "forge"},
	})
	s := NewSubmitter(client)

	require.NoError(t, s.CancelJob(ctx, "forge", "j"))
	_, err := client.BatchV1().Jobs("forge").Get(ctx, "j", metav1.GetOptions{})
	require.Error(t, err, "Job deleted")

	// Already gone = cancelled.
	require.NoError(t, s.CancelJob(ctx, "forge", "j"))
}

// Component chain: TaskDef → KueueManager → submitter → fake cluster Job.
// This is the wiring the audit found zero-called: manager builds the spec,
// submitter materialises it.
func TestManagerToSubmitterComponentChain(t *testing.T) {
	ctx := context.Background()
	client := k8sfake.NewSimpleClientset()
	s := NewSubmitter(client)

	cfg := coordinator.DefaultKueueConfig()
	cfg.Enabled = true
	mgr := coordinator.NewKueueManager(cfg, s)

	taskDef := coordinator.TaskDef{
		Handler: "gpu-worker",
		Params: map[string]any{
			"image":     "train:latest",
			"gpu.model": "A100",
		},
	}
	require.NoError(t, mgr.SubmitGPUTask(ctx, "wf-42", "task-7", taskDef))

	jobs, err := client.BatchV1().Jobs(cfg.Namespace).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, jobs.Items, 1)

	job := jobs.Items[0]
	assert.Equal(t, "forge-wf-42-task-7", job.Name)
	assert.Equal(t, "wf-42", job.Labels["forge.io/workflow-id"])
	assert.Equal(t, "task-7", job.Labels["forge.io/task-id"])
	assert.Equal(t, cfg.QueueName, job.Labels[queueLabel])
	assert.Equal(t, "A100", job.Spec.Template.Spec.NodeSelector[gpuNodeLabel])
	assert.Equal(t, "train:latest", job.Spec.Template.Spec.Containers[0].Image)
}

// Without kubeconfig and outside a cluster, construction says exactly that
// instead of inventing a connection.
func TestNewSubmitterFromKubeconfigIsHonest(t *testing.T) {
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	_, err := NewSubmitterFromKubeconfig("")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not running in cluster")
}

func boolPtr(b bool) *bool { return &b }
