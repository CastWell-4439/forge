package grpcauth

import (
	"context"
	"net"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	forgev1 "github.com/castwell/forge/api/proto/gen"
	"github.com/castwell/forge/internal/identity"
)

// The end-to-end property: a per-principal token produces a VERIFIED actor that
// the handler can read, through the real interceptor chain.
//
// This is what the identity package could not do alone. It had a constructor for
// a verified actor and nothing that called it; the token check is what calls it,
// and this test shows the two fit together over a real connection rather than in
// a unit test of either half.
func TestTokenYieldsAVerifiedActorToTheHandler(t *testing.T) {
	cfg := Config{
		Addr:   "10.0.0.5:50051",
		Secret: "shared-fallback",
		Tokens: ParseTokens("alice:alice-token"),
	}

	lis, stub := startServerWithIdentity(t, cfg)

	// The caller presents alice's token AND claims to be bob.
	ctx := outgoingMetadata(context.Background(),
		authorizationHeader, bearerPrefix+"alice-token",
		identity.GRPCMetadataKey, "bob",
	)
	_, err := dialWith(t, lis, ctx).Register(context.Background(), &forgev1.RegisterRequest{})
	require.NoError(t, err)

	actor := stub.lastActor()
	assert.Equal(t, "alice", actor.ID(), "the token decides; the header is only a claim")
	assert.True(t, actor.Verified(), "a token check is a verification")
	assert.Equal(t, verifierName, actor.Verifier())
}

// The shared secret still admits a caller, and yields an unverified declaration.
func TestSharedSecretYieldsAnUnverifiedActor(t *testing.T) {
	cfg := Config{Addr: "10.0.0.5:50051", Secret: "shared"}

	lis, stub := startServerWithIdentity(t, cfg)

	ctx := outgoingMetadata(context.Background(),
		authorizationHeader, bearerPrefix+"shared",
		identity.GRPCMetadataKey, "carol",
	)
	_, err := dialWith(t, lis, ctx).Register(context.Background(), &forgev1.RegisterRequest{})
	require.NoError(t, err)

	actor := stub.lastActor()
	assert.Equal(t, "carol", actor.ID())
	assert.False(t, actor.Verified(), "a shared secret cannot establish who is calling")
}

// No credential is rejected, and the handler never runs.
func TestNoCredentialIsRejectedEndToEnd(t *testing.T) {
	cfg := Config{Addr: "10.0.0.5:50051", Tokens: ParseTokens("alice:alice-token")}

	lis, stub := startServerWithIdentity(t, cfg)

	_, err := dialWith(t, lis, context.Background()).Register(context.Background(), &forgev1.RegisterRequest{})
	require.Error(t, err)
	assert.Equal(t, codes.Unauthenticated, status.Code(err))
	assert.Zero(t, stub.registerHits(), "the handler must not run for an unauthenticated call")
}

// A wrong token and a missing one are indistinguishable, so the reply reveals
// nothing about which part of the credential was wrong.
func TestWrongAndMissingTokensAreIndistinguishableEndToEnd(t *testing.T) {
	cfg := Config{Addr: "10.0.0.5:50051", Tokens: ParseTokens("alice:alice-token")}
	lis, _ := startServerWithIdentity(t, cfg)

	_, errMissing := dialWith(t, lis, context.Background()).
		Register(context.Background(), &forgev1.RegisterRequest{})

	wrongCtx := outgoingMetadata(context.Background(), authorizationHeader, bearerPrefix+"not-the-token")
	_, errWrong := dialWith(t, lis, wrongCtx).
		Register(context.Background(), &forgev1.RegisterRequest{})

	require.Error(t, errMissing)
	require.Error(t, errWrong)
	assert.Equal(t, codes.Unauthenticated, status.Code(errMissing))
	assert.Equal(t, codes.Unauthenticated, status.Code(errWrong))
	assert.Equal(t, errMissing.Error(), errWrong.Error())
}

// --- harness ---

// startServerWithIdentity stands up a real server with BOTH interceptors in
// production order: credentials first, identity second.
func startServerWithIdentity(t *testing.T, cfg Config) (*bufconn.Listener, *capturingStub) {
	t.Helper()

	lis := bufconn.Listen(1024 * 1024)
	opts := cfg.ServerOptions()
	opts = append(opts,
		grpc.ChainUnaryInterceptor(identity.UnaryServerInterceptor()),
		grpc.ChainStreamInterceptor(identity.StreamServerInterceptor()),
	)
	srv := grpc.NewServer(opts...)

	stub := &capturingStub{}
	forgev1.RegisterWorkerServiceServer(srv, stub)

	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return lis, stub
}

// dialWith opens a client against the listener, applying outgoingCtx as
// per-call outgoing metadata.
func dialWith(t *testing.T, lis *bufconn.Listener, outgoingCtx context.Context) forgev1.WorkerServiceClient {
	t.Helper()

	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithChainUnaryInterceptor(func(
			ctx context.Context, method string, req, reply any,
			cc *grpc.ClientConn, invoker grpc.UnaryInvoker, callOpts ...grpc.CallOption,
		) error {
			// The metadata from outgoingCtx is copied onto the call's context, so
			// a test can present a credential without hand-rolling an interceptor.
			if md, ok := metadata.FromOutgoingContext(outgoingCtx); ok {
				ctx = metadata.NewOutgoingContext(ctx, md)
			}
			return invoker(ctx, method, req, reply, cc, callOpts...)
		}),
	}

	conn, err := grpc.NewClient("passthrough:///bufconn", opts...)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return forgev1.NewWorkerServiceClient(conn)
}

// outgoingMetadata builds a context carrying outgoing metadata pairs.
func outgoingMetadata(ctx context.Context, kv ...string) context.Context {
	return metadata.NewOutgoingContext(ctx, metadata.Pairs(kv...))
}

// capturingStub records what the handler saw and whether it ran.
//
// It is separate from stubWorkerService (in grpcauth_test.go) because that one
// counts hits for the guard tests, while this one also captures the Actor from
// the context — the thing the token check is supposed to produce.
type capturingStub struct {
	forgev1.UnimplementedWorkerServiceServer

	mu    sync.Mutex
	actor identity.Actor
	hits  int
}

func (s *capturingStub) Register(ctx context.Context, _ *forgev1.RegisterRequest) (*forgev1.RegisterResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.actor = identity.FromContext(ctx)
	s.hits++
	return &forgev1.RegisterResponse{}, nil
}

func (s *capturingStub) lastActor() identity.Actor {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.actor
}

func (s *capturingStub) registerHits() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits
}
