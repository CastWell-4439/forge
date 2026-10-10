// Package grpcauth authenticates inbound gRPC calls.
//
// The gap this closes was not "no authentication" but a pair of ports with
// opposite postures, where the difference came from a default rather than from a
// decision:
//
//	HTTP   bound 127.0.0.1:9090 by default; without a secret it refuses to
//	       register its entry points, and says which variable is missing
//	gRPC   bound :50051 — every interface — with no authentication at all
//
// The HTTP side was locked down deliberately (see internal/serve/coordinator's
// webhook.go); the gRPC side was never decided, so it stayed open. A port that
// accepts `SubmitWorkflow` and `ExecuteTask` from anywhere is not a missing
// feature, it is an exposed one.
//
// The posture follows the rule that file states: friction should appear exactly
// when the service becomes reachable by someone else. So this package does not
// add "an auth option" that a deployment can leave off — a check that can be
// switched off is not a check. Instead the requirement is tied to reachability:
//
//	loopback bind, no secret   allowed — the only callers are on this machine
//	routable bind, no secret   REFUSED at startup — the operator is told which
//	                           variable to set
//	secret configured          every RPC must present it, whichever bind
//
// That choice also settles a tension between two rules this project already
// wrote down: "a check you can turn off is no check" argues for always-on, while
// "a governance capability must not start blocking silently in some upgrade"
// argues for default-off. Binding the requirement to the bind address satisfies
// both — the check is mandatory precisely where it can matter, and a loopback
// deployment sees no change.
package grpcauth

import (
	"context"
	"crypto/subtle"
	"fmt"
	"log"
	"net"
	"os"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// EnvSecret is the environment variable holding the shared secret for inbound
// gRPC calls.
//
//	FORGE_GRPC_SECRET   unset means "no secret"; whether that is acceptable
//	                    depends on the bind address (see the package comment).
//
// Workers and the CLI read the same variable, because they are the same secret
// seen from the other end.
const EnvSecret = "FORGE_GRPC_SECRET"

// authorizationHeader is the metadata key the secret travels in. It is the
// conventional one, so grpcurl and other gRPC tools can send it without a
// special flag:
//
//	grpcurl -H "authorization: Bearer $FORGE_GRPC_SECRET" ...
const authorizationHeader = "authorization"

// bearerPrefix is stripped from the header when present. Accepting the bare
// secret as well means a hand-written client that sends only the value still
// works; requiring the prefix would be pedantry with no security value.
const bearerPrefix = "Bearer "

// UnauthenticatedError is returned to a caller whose credential is absent or
// wrong.
//
// The message is deliberately the same in both cases and says nothing about
// which part failed. A distinct "wrong secret" would confirm to someone
// guessing that they had found the right header, and a distinct "no secret"
// would tell them the deployment is misconfigured. Neither helps an honest
// caller, who either has the secret or is about to be told to get it.
var UnauthenticatedError = status.Error(codes.Unauthenticated, "unauthenticated")

// Config is the resolved authentication posture.
type Config struct {
	// Secret is the shared secret, or "" when none is configured.
	Secret string
	// Addr is the address the server will bind, used to decide whether the
	// deployment is reachable by others.
	Addr string
}

// FromEnv reads the configuration from the environment for a given bind address.
func FromEnv(addr string) Config {
	return Config{
		Secret: strings.TrimSpace(os.Getenv(EnvSecret)),
		Addr:   addr,
	}
}

// Enabled reports whether calls will be authenticated.
func (c Config) Enabled() bool { return c.Secret != "" }

// Validate decides whether the deployment may serve, and is the whole of the
// fail-closed behaviour.
//
// A routable bind with no secret is refused rather than warned about. A warning
// would leave the port open, which is the state this exists to end; refusing to
// start makes the operator choose between setting a secret and binding loopback,
// and both of those are safe.
//
// It returns an error naming the variable to set and the two ways out, because
// the person reading it is looking at a service that will not start.
func (c Config) Validate() error {
	if c.Enabled() || isLoopbackAddr(c.Addr) {
		return nil
	}
	return fmt.Errorf(
		"refusing to serve gRPC on %s without authentication: this address is reachable from other machines "+
			"and the API can submit and cancel workflows. Set %s, or bind loopback (FORGE_GRPC_ADDR=127.0.0.1:50051)",
		c.Addr, EnvSecret)
}

// LogPosture states which mode the server is in. An operator should not have to
// infer whether their API is locked from the absence of a log line.
func (c Config) LogPosture() {
	switch {
	case c.Enabled() && isLoopbackAddr(c.Addr):
		log.Printf("INFO: gRPC auth enabled on %s (secret from %s; bound loopback)", c.Addr, EnvSecret)
	case c.Enabled():
		log.Printf("INFO: gRPC auth enabled on %s (secret from %s)", c.Addr, EnvSecret)
	default:
		log.Printf("WARN: gRPC auth DISABLED on %s: bound loopback and no %s set. "+
			"Only processes on this machine can reach it; set %s before exposing the port",
			c.Addr, EnvSecret, EnvSecret)
	}
}

// ServerOptions returns the interceptors that enforce the configuration.
//
// Both unary and stream are covered: Heartbeat is a bidirectional stream, so a
// unary-only interceptor would leave the worker's heartbeat — and its identity —
// unauthenticated.
func (c Config) ServerOptions() []grpc.ServerOption {
	if !c.Enabled() {
		return nil
	}
	return []grpc.ServerOption{
		grpc.ChainUnaryInterceptor(c.UnaryInterceptor()),
		grpc.ChainStreamInterceptor(c.StreamInterceptor()),
	}
}

// UnaryInterceptor authenticates a unary call.
func (c Config) UnaryInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if err := c.authorize(ctx); err != nil {
			return nil, err
		}
		return handler(ctx, req)
	}
}

// StreamInterceptor authenticates a streaming call.
func (c Config) StreamInterceptor() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if err := c.authorize(ss.Context()); err != nil {
			return err
		}
		return handler(srv, ss)
	}
}

// authorize checks the credential on a call.
func (c Config) authorize(ctx context.Context) error {
	presented := secretFromContext(ctx)
	// Equal lengths first: ConstantTimeCompare returns 0 immediately for
	// different lengths, and there is no way around that, but doing the length
	// check here keeps the intent explicit rather than implicit in the stdlib.
	if len(presented) != len(c.Secret) {
		return UnauthenticatedError
	}
	// Constant time, so the reply carries no information about how much of the
	// secret was right. `==` on strings stops at the first differing byte, which
	// is enough to recover a secret one character at a time.
	if subtle.ConstantTimeCompare([]byte(presented), []byte(c.Secret)) != 1 {
		return UnauthenticatedError
	}
	return nil
}

// secretFromContext reads the presented credential from incoming metadata.
func secretFromContext(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	values := md.Get(authorizationHeader)
	if len(values) == 0 {
		return ""
	}
	value := strings.TrimSpace(values[0])
	if after, found := strings.CutPrefix(value, bearerPrefix); found {
		return strings.TrimSpace(after)
	}
	return value
}

// ClientOptions returns the dial options a client needs to authenticate.
//
// It writes the metadata by hand rather than using grpc.WithPerRPCCredentials,
// which refuses to send credentials over an insecure transport by default. There
// is no TLS here, so that option would fail closed for the wrong reason and push
// a deployment towards enabling TLS it does not have — a larger decision than
// this one. An explicit interceptor does what the deployment asked for.
//
// No secret configured means no option: a client talking to an unauthenticated
// loopback server should not have to know that.
func ClientOptions() []grpc.DialOption {
	secret := strings.TrimSpace(os.Getenv(EnvSecret))
	if secret == "" {
		return nil
	}
	return []grpc.DialOption{
		grpc.WithChainUnaryInterceptor(clientUnaryInterceptor(secret)),
		grpc.WithChainStreamInterceptor(clientStreamInterceptor(secret)),
	}
}

func clientUnaryInterceptor(secret string) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		return invoker(withSecret(ctx, secret), method, req, reply, cc, opts...)
	}
}

func clientStreamInterceptor(secret string) grpc.StreamClientInterceptor {
	return func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		return streamer(withSecret(ctx, secret), desc, cc, method, opts...)
	}
}

// withSecret attaches the credential to outgoing metadata.
func withSecret(ctx context.Context, secret string) context.Context {
	return metadata.AppendToOutgoingContext(ctx, authorizationHeader, bearerPrefix+secret)
}

// isLoopbackAddr reports whether an address can only be reached from this
// machine.
//
// An empty host means every interface (`:50051`), which is the opposite of
// loopback — that is the case this whole file exists for, so it is checked
// explicitly rather than left to net.SplitHostPort's error path.
func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		// No port, or something unparseable: treat the whole string as a host so
		// "localhost" and "127.0.0.1" are still recognised.
		host = addr
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return false // ":50051" — every interface
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		// A hostname. It could resolve anywhere, so it is not treated as
		// loopback: guessing "probably local" is how a deployment ends up
		// exposed while believing it is not.
		return false
	}
	return ip.IsLoopback()
}
