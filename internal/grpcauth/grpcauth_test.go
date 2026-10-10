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
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	forgev1 "github.com/castwell/forge/api/proto/gen"
)

// The posture rule: friction appears exactly when the service becomes reachable
// by someone else. These cases are the rule, spelled out.
func TestValidateTiesTheRequirementToReachability(t *testing.T) {
	cases := []struct {
		name    string
		addr    string
		secret  string
		allowed bool
	}{
		{"loopback without a secret", "127.0.0.1:50051", "", true},
		{"localhost without a secret", "localhost:50051", "", true},
		{"ipv6 loopback without a secret", "[::1]:50051", "", true},
		{"loopback with a secret", "127.0.0.1:50051", "s3cret", true},

		{"every interface without a secret", ":50051", "", false},
		{"all zeros without a secret", "0.0.0.0:50051", "", false},
		{"a routable ip without a secret", "10.0.0.5:50051", "", false},
		{"a hostname without a secret", "forge.internal:50051", "", false},

		{"every interface with a secret", ":50051", "s3cret", true},
		{"a routable ip with a secret", "10.0.0.5:50051", "s3cret", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Config{Addr: tc.addr, Secret: tc.secret}.Validate()
			if tc.allowed {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err, "a reachable address without a secret must refuse to serve")
			assert.Contains(t, err.Error(), EnvSecret,
				"the operator is looking at a service that will not start; it must name the variable to set")
			assert.Contains(t, err.Error(), "127.0.0.1:50051",
				"and the other safe way out, since setting a secret may not be their call")
		})
	}
}

// A hostname could resolve anywhere, so it is not treated as loopback. Guessing
// "probably local" is how a deployment ends up exposed while believing it is not.
func TestHostnameIsNotAssumedLocal(t *testing.T) {
	assert.False(t, isLoopbackAddr("myhost:50051"))
	assert.False(t, isLoopbackAddr("forge.example.com:50051"))
	assert.True(t, isLoopbackAddr("localhost:50051"))
	assert.True(t, isLoopbackAddr("127.0.0.1:50051"))
	assert.True(t, isLoopbackAddr("127.0.0.2:50051"), "the whole 127/8 block is loopback")
}

// The empty host is the case this package exists for: ":50051" is every
// interface, which is the opposite of loopback.
func TestEmptyHostMeansEveryInterface(t *testing.T) {
	assert.False(t, isLoopbackAddr(":50051"))
	assert.True(t, Config{Addr: ":50051"}.Validate() != nil)
}

// --- the interceptor, over a real gRPC connection ---

// startServer runs a real gRPC server with the given posture against a bufconn
// listener, and returns a dial target for it.
func startServer(t *testing.T, cfg Config) *bufconn.Listener {
	t.Helper()

	lis := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer(cfg.ServerOptions()...)

	// WorkerService/Register is used as the probe: it is one of the calls that
	// was reachable without authentication, which is what this closes.
	forgev1.RegisterWorkerServiceServer(srv, &stubWorkerService{})

	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return lis
}

// dial opens a client against the bufconn listener, optionally presenting a
// secret.
func dial(t *testing.T, lis *bufconn.Listener, secret string) forgev1.WorkerServiceClient {
	t.Helper()

	var opts []grpc.DialOption
	opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	opts = append(opts, grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
		return lis.DialContext(ctx)
	}))
	if secret != "" {
		opts = append(opts, grpc.WithChainUnaryInterceptor(clientUnaryInterceptor(secret)))
	}

	conn, err := grpc.NewClient("passthrough:///bufconn", opts...)
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	return forgev1.NewWorkerServiceClient(conn)
}

// The call that was reachable from anywhere without authentication is now
// reachable only with the secret.
func TestInboundCallRequiresTheSecret(t *testing.T) {
	lis := startServer(t, Config{Addr: "10.0.0.5:50051", Secret: "s3cret"})

	t.Run("without a secret", func(t *testing.T) {
		client := dial(t, lis, "")
		_, err := client.Register(context.Background(), &forgev1.RegisterRequest{})
		require.Error(t, err)
		assert.Equal(t, codes.Unauthenticated, status.Code(err))
	})

	t.Run("with the wrong secret", func(t *testing.T) {
		client := dial(t, lis, "not-the-secret")
		_, err := client.Register(context.Background(), &forgev1.RegisterRequest{})
		require.Error(t, err)
		assert.Equal(t, codes.Unauthenticated, status.Code(err))
	})

	t.Run("with the right secret", func(t *testing.T) {
		client := dial(t, lis, "s3cret")
		_, err := client.Register(context.Background(), &forgev1.RegisterRequest{})
		require.NoError(t, err)
	})
}

// A missing secret and a wrong secret produce the same reply. A distinct message
// would confirm to someone guessing that they had found the right header, or
// tell them the deployment is misconfigured.
func TestTheReplyDoesNotSayWhichPartFailed(t *testing.T) {
	lis := startServer(t, Config{Addr: "10.0.0.5:50051", Secret: "s3cret"})

	_, errMissing := dial(t, lis, "").Register(context.Background(), &forgev1.RegisterRequest{})
	_, errWrong := dial(t, lis, "wrong").Register(context.Background(), &forgev1.RegisterRequest{})

	require.Error(t, errMissing)
	require.Error(t, errWrong)
	assert.Equal(t, status.Convert(errMissing).Message(), status.Convert(errWrong).Message(),
		"the two failures must be indistinguishable")
	assert.NotContains(t, status.Convert(errWrong).Message(), "s3cret")
}

// A loopback deployment with no secret serves normally: it is the case that must
// keep working unchanged, and the only callers are on this machine.
func TestLoopbackWithoutASecretServes(t *testing.T) {
	cfg := Config{Addr: "127.0.0.1:50051"}
	require.NoError(t, cfg.Validate())
	require.False(t, cfg.Enabled())

	lis := startServer(t, cfg)
	_, err := dial(t, lis, "").Register(context.Background(), &forgev1.RegisterRequest{})
	assert.NoError(t, err, "a loopback deployment must not need a secret to work")
}

// The stream interceptor is not optional: Heartbeat is a bidirectional stream, so
// a unary-only guard would leave the worker's heartbeat — and the identity it
// carries — unauthenticated.
func TestStreamCallsAreAlsoGuarded(t *testing.T) {
	lis := startServer(t, Config{Addr: "10.0.0.5:50051", Secret: "s3cret"})

	t.Run("without a secret", func(t *testing.T) {
		stream, err := dial(t, lis, "").Heartbeat(context.Background())
		require.NoError(t, err, "the stream opens lazily")
		require.NoError(t, stream.Send(&forgev1.HeartbeatPing{}))
		_, err = stream.Recv()
		require.Error(t, err, "the first round trip must be refused")
		assert.Equal(t, codes.Unauthenticated, status.Code(err))
	})

	t.Run("with the right secret", func(t *testing.T) {
		opts := []grpc.DialOption{
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
				return lis.DialContext(ctx)
			}),
			grpc.WithChainStreamInterceptor(clientStreamInterceptor("s3cret")),
		}
		conn, err := grpc.NewClient("passthrough:///bufconn", opts...)
		require.NoError(t, err)
		defer conn.Close()

		stream, err := forgev1.NewWorkerServiceClient(conn).Heartbeat(context.Background())
		require.NoError(t, err)
		require.NoError(t, stream.Send(&forgev1.HeartbeatPing{}))
		_, err = stream.Recv()
		assert.NoError(t, err)
	})
}

// A disabled configuration adds no interceptors at all, so an unauthenticated
// deployment pays nothing for this package.
func TestDisabledConfigAddsNoInterceptors(t *testing.T) {
	assert.Empty(t, Config{Addr: "127.0.0.1:50051"}.ServerOptions())
	assert.Empty(t, Config{Addr: "10.0.0.5:50051", Secret: "s"}.ServerOptions() == nil)
	assert.Len(t, Config{Addr: "10.0.0.5:50051", Secret: "s"}.ServerOptions(), 2)
}

// The header is read in both the conventional form and bare, so a hand-written
// client that sends only the value still works. Requiring the prefix would be
// pedantry with no security value.
func TestSecretIsReadWithOrWithoutTheBearerPrefix(t *testing.T) {
	cfg := Config{Secret: "s3cret"}

	ctx := metadataWith(authorizationHeader, "Bearer s3cret")
	assert.NoError(t, cfg.authorize(ctx))

	ctx = metadataWith(authorizationHeader, "s3cret")
	assert.NoError(t, cfg.authorize(ctx))

	ctx = metadataWith(authorizationHeader, "Bearer  s3cret ") // stray whitespace
	assert.NoError(t, cfg.authorize(ctx))
}

// ClientOptions follows the environment, so every dial site authenticates the
// same way without each one deciding for itself.
func TestClientOptionsFollowTheEnvironment(t *testing.T) {
	t.Setenv(EnvSecret, "")
	assert.Empty(t, ClientOptions(), "no secret means no client-side option")

	t.Setenv(EnvSecret, "s3cret")
	assert.Len(t, ClientOptions(), 2)
}

// FromEnv trims whitespace: a trailing newline from a file-mounted secret is a
// realistic way to get a secret that compares unequal for no visible reason.
func TestFromEnvTrimsWhitespace(t *testing.T) {
	t.Setenv(EnvSecret, "  s3cret\n")
	cfg := FromEnv("127.0.0.1:50051")

	assert.Equal(t, "s3cret", cfg.Secret)
	assert.True(t, cfg.Enabled())
}

// --- helper ---

func metadataWith(key, value string) context.Context {
	return metadata.NewIncomingContext(context.Background(), metadata.Pairs(key, value))
}

// stubWorkerService satisfies the service so a call can reach the interceptor;
// the handlers themselves are not what is under test.
type stubWorkerService struct {
	forgev1.UnimplementedWorkerServiceServer
}

func (s *stubWorkerService) Register(context.Context, *forgev1.RegisterRequest) (*forgev1.RegisterResponse, error) {
	return &forgev1.RegisterResponse{}, nil
}

func (s *stubWorkerService) Heartbeat(stream forgev1.WorkerService_HeartbeatServer) error {
	for {
		if _, err := stream.Recv(); err != nil {
			return nil
		}
		if err := stream.Send(&forgev1.HeartbeatPong{WorkerId: "w"}); err != nil {
			return err
		}
	}
}
