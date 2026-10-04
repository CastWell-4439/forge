package coordinator

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	forgev1 "github.com/castwell/forge/api/proto/gen"
	"github.com/castwell/forge/internal/storage"
)

// The registration path an out-of-process worker uses: the worker dials
// WorkerService/Register on the COORDINATOR's listener. Before this RPC
// existed server-side, that call always returned "unknown service" and every
// production worker died at startup — tests never caught it because they
// call RegisterWorker in-process.
func TestRegisterRPCAddsWorkerToDispatch(t *testing.T) {
	store, err := storage.NewBoltStorage(t.TempDir() + "/test.db")
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	coord := NewCoordinator(store)

	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	forgev1.RegisterCoordinatorServiceServer(srv, coord)
	forgev1.RegisterWorkerServiceServer(srv, coord)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	client := forgev1.NewWorkerServiceClient(conn)
	resp, err := client.Register(ctx, &forgev1.RegisterRequest{
		Registration: &forgev1.WorkerRegistration{
			Id:       "worker-rpc-1",
			Addr:     "127.0.0.1:65001",
			Handlers: []string{"shell", "wasm"},
			Capacity: 7,
		},
	})
	require.NoError(t, err, "registration over the wire must succeed")
	assert.True(t, resp.GetAccepted())

	// The worker is now discoverable by dispatch (legacy path).
	coord.mu.RLock()
	defer coord.mu.RUnlock()
	entry := coord.workers["worker-rpc-1"]
	require.NotNil(t, entry, "the registered worker must be dispatchable")
	assert.Equal(t, 7, entry.Capacity)
	assert.ElementsMatch(t, []string{"shell", "wasm"}, entry.Handlers)
}

// Malformed registrations are rejected with a proper status instead of
// silently creating an unusable entry.
func TestRegisterRPCRejectsBadRegistration(t *testing.T) {
	store, err := storage.NewBoltStorage(t.TempDir() + "/test.db")
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	coord := NewCoordinator(store)

	_, err = coord.Register(context.Background(), &forgev1.RegisterRequest{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "registration")

	_, err = coord.Register(context.Background(), &forgev1.RegisterRequest{
		Registration: &forgev1.WorkerRegistration{Id: "w"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "address")
}
