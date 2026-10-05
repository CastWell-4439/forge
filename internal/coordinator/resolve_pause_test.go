package coordinator

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	forgev1 "github.com/castwell/forge/api/proto/gen"
	"github.com/castwell/forge/internal/storage"
)

// The approval entry point end to end: a paused task, an approval arriving
// over the wire, and the task back in the queue where the scheduler claims it.
func TestResolveTaskPauseOverGRPC(t *testing.T) {
	c, _, taskID := newPauseTestCoordinator(t, storage.TaskStatusRunning)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	require.NoError(t, c.OnTaskPaused(ctx, taskID, "needs approval"))

	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	forgev1.RegisterCoordinatorServiceServer(srv, c)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	client := forgev1.NewCoordinatorServiceClient(conn)

	// Approve: the response confirms the transition instead of leaving it to
	// be inferred.
	resp, err := client.ResolveTaskPause(ctx, &forgev1.ResolveTaskPauseRequest{
		TaskId: taskID, Approve: true, Reason: "operator approved",
	})
	require.NoError(t, err)
	assert.Equal(t, string(storage.TaskStatusReady), resp.GetNewStatus(),
		"approval puts the task back in the queue")

	// It is genuinely claimable now.
	claimed, err := c.store.ClaimTask(ctx, "worker-9", []string{"shell"})
	require.NoError(t, err)
	require.NotNil(t, claimed)
	assert.Equal(t, taskID, claimed.ID)

	// A second resolve has stale state: conflict, not a silent success.
	_, err = client.ResolveTaskPause(ctx, &forgev1.ResolveTaskPauseRequest{
		TaskId: taskID, Approve: true,
	})
	require.Error(t, err)
	assert.Equal(t, codes.FailedPrecondition, status.Code(err))

	// Missing task id is a caller error.
	_, err = client.ResolveTaskPause(ctx, &forgev1.ResolveTaskPauseRequest{})
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
}

// Rejection over the wire: FAILED with the reason carried through.
func TestResolveTaskPauseRejectsOverGRPC(t *testing.T) {
	c, _, taskID := newPauseTestCoordinator(t, storage.TaskStatusRunning)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	require.NoError(t, c.OnTaskPaused(ctx, taskID, "needs approval"))

	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	forgev1.RegisterCoordinatorServiceServer(srv, c)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	client := forgev1.NewCoordinatorServiceClient(conn)
	resp, err := client.ResolveTaskPause(ctx, &forgev1.ResolveTaskPauseRequest{
		TaskId: taskID, Approve: false, Reason: "denied by operator",
	})
	require.NoError(t, err)
	assert.Equal(t, string(storage.TaskStatusFailed), resp.GetNewStatus())

	task, err := c.store.GetTask(ctx, taskID)
	require.NoError(t, err)
	assert.Contains(t, task.ErrorMsg, "denied by operator")
}
