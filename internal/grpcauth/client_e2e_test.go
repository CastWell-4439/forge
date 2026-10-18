package grpcauth

import (
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	forgev1 "github.com/castwell/forge/api/proto/gen"
)

// The client side of authentication, over a real connection.
//
// Everything else in this package tests the SERVER: it presents credentials by
// writing metadata by hand. That leaves a whole path unverified — ClientOptions,
// which is what every worker actually calls. A bug there (reading the wrong
// variable, preferring the wrong credential, dropping the interceptor) would
// leave every worker unable to authenticate while all the server tests stayed
// green.
//
// These tests therefore drive the real client options against a real server.

// A worker presenting a per-principal token through ClientOptions is admitted
// and identified.
func TestClientOptionsPresentAPerPrincipalToken(t *testing.T) {
	cfg := Config{
		Addr:   "10.0.0.5:50051",
		Secret: "shared-fallback",
		Tokens: ParseTokens("worker-1:worker-one-token"),
	}

	lis, stub := startServerWithIdentity(t, cfg)

	// The deployment gives this process a token AND keeps the shared secret set,
	// which is the rotation-friendly configuration: existing clients keep
	// working while new ones identify themselves.
	t.Setenv(EnvSecret, "shared-fallback")
	t.Setenv(EnvClientToken, "worker-one-token")

	client := dialWithProductionOptions(t, lis)
	_, err := client.Register(context.Background(), &forgev1.RegisterRequest{})
	require.NoError(t, err, "a token presented through ClientOptions must be accepted")

	actor := stub.lastActor()
	assert.Equal(t, "worker-1", actor.ID(),
		"and the caller must be identified, not merely admitted")
	assert.True(t, actor.Verified())
}

// THE PREFERENCE RULE: when both a token and a shared secret are configured, the
// client presents the TOKEN.
//
// This is what makes attribution survive a rotation-era deployment. If the client
// preferred the shared secret, every worker would keep authenticating anonymously
// even after being issued a token — and the audit trail would show
// "unattributed" for calls that had an identity available. Server-side tests
// cannot see this: they never choose which credential to send.
func TestClientOptionsPreferTheTokenOverTheSharedSecret(t *testing.T) {
	cfg := Config{
		Addr:   "10.0.0.5:50051",
		Secret: "shared-fallback",
		Tokens: ParseTokens("worker-1:worker-one-token"),
	}

	lis, stub := startServerWithIdentity(t, cfg)

	t.Setenv(EnvSecret, "shared-fallback")
	t.Setenv(EnvClientToken, "worker-one-token")

	client := dialWithProductionOptions(t, lis)
	_, err := client.Register(context.Background(), &forgev1.RegisterRequest{})
	require.NoError(t, err)

	actor := stub.lastActor()
	assert.Equal(t, "worker-1", actor.ID(),
		"the token must win: preferring the shared secret would leave an identified "+
			"worker recorded as unattributed")
	assert.True(t, actor.Verified())
}

// With no token configured the shared secret is presented, and the caller is
// admitted without an identity — the pre-D-71 behaviour, preserved.
func TestClientOptionsFallBackToTheSharedSecret(t *testing.T) {
	cfg := Config{Addr: "10.0.0.5:50051", Secret: "shared-only"}

	lis, stub := startServerWithIdentity(t, cfg)

	t.Setenv(EnvSecret, "shared-only")
	t.Setenv(EnvClientToken, "")

	client := dialWithProductionOptions(t, lis)
	_, err := client.Register(context.Background(), &forgev1.RegisterRequest{})
	require.NoError(t, err, "a deployment without tokens must still work")

	actor := stub.lastActor()
	_, declared := actor.Declared()
	assert.False(t, declared, "a shared secret establishes no identity")
	assert.False(t, actor.Verified())
}

// A wrong token with no shared secret is rejected, and the handler never runs.
//
// The failure has to surface at the CLIENT, because that is where an operator
// would see it: a worker whose token was mistyped must not appear to connect.
func TestClientOptionsWithABadTokenAreRejected(t *testing.T) {
	cfg := Config{Addr: "10.0.0.5:50051", Tokens: ParseTokens("worker-1:the-real-token")}

	lis, stub := startServerWithIdentity(t, cfg)

	t.Setenv(EnvSecret, "")
	t.Setenv(EnvClientToken, "not-the-real-token")

	client := dialWithProductionOptions(t, lis)
	_, err := client.Register(context.Background(), &forgev1.RegisterRequest{})
	require.Error(t, err)
	assert.Equal(t, codes.Unauthenticated, status.Code(err))
	assert.Zero(t, stub.registerHits(), "the handler must not run")
}

// With nothing configured, ClientOptions adds nothing and the call still works
// on loopback — the historical behaviour for a local deployment.
func TestClientOptionsWithoutCredentialsStillWork(t *testing.T) {
	cfg := Config{Addr: "127.0.0.1:50051"} // loopback, no credential: allowed

	lis, _ := startServerWithIdentity(t, cfg)

	t.Setenv(EnvSecret, "")
	t.Setenv(EnvClientToken, "")

	assert.Empty(t, ClientOptions(), "no credential means no client-side option")

	client := dialWithProductionOptions(t, lis)
	_, err := client.Register(context.Background(), &forgev1.RegisterRequest{})
	require.NoError(t, err, "a loopback deployment with no credential admits the call")
}

// The token variable is read in preference to the shared secret even when the
// token is whitespace: a deployment that clears the token wants the fallback,
// not a call presenting an empty token.
func TestBlankClientTokenFallsBack(t *testing.T) {
	t.Setenv(EnvSecret, "shared-secret")
	t.Setenv(EnvClientToken, "   ")

	// Two options means one interceptor pair was installed; which credential it
	// carries is asserted by the end-to-end tests above.
	assert.Len(t, ClientOptions(), 2,
		"a blank token must not suppress authentication entirely")
}

// dialWithProductionOptions is the harness the tests above share.
//
// It applies ClientOptions() unchanged — the point is to exercise the production
// path, so nothing here injects metadata by hand.
func dialWithProductionOptions(t *testing.T, lis *bufconn.Listener) forgev1.WorkerServiceClient {
	t.Helper()

	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
	}
	opts = append(opts, ClientOptions()...)

	conn, err := grpc.NewClient("passthrough:///bufconn", opts...)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return forgev1.NewWorkerServiceClient(conn)
}
