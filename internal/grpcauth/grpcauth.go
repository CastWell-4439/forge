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

	"github.com/castwell/forge/internal/identity"
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
	//
	// It still admits a caller, and a caller admitted this way has NO verified
	// identity: a shared secret cannot distinguish its holders. That is not a
	// wart to be removed — it is the difference between the two credentials, and
	// both are kept because they answer different questions. A deployment that
	// only needs "not open to the world" sets this; one that needs "who" adds
	// tokens.
	Secret string
	// Tokens are the per-principal credentials, or the zero value when none are
	// configured.
	Tokens TokenSet
	// Addr is the address the server will bind, used to decide whether the
	// deployment is reachable by others.
	Addr string
}

// FromEnv reads the configuration from the environment for a given bind address.
func FromEnv(addr string) Config {
	return Config{
		Secret: strings.TrimSpace(os.Getenv(EnvSecret)),
		Tokens: TokensFromEnv(),
		Addr:   addr,
	}
}

// Enabled reports whether calls will be authenticated.
//
// Either credential is enough to require one: a deployment that configured only
// tokens must still reject an unauthenticated call, which is the bug that would
// follow from checking the shared secret alone here.
func (c Config) Enabled() bool { return c.Secret != "" || c.Tokens.Configured() }

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
	// A malformed token entry is refused wherever it occurs, including on
	// loopback. It is a configuration mistake rather than a posture choice: the
	// named principal would silently be unable to authenticate, and the symptom
	// would surface much later as a confusing rejection. This is the opposite
	// direction from an undefined policy role, and deliberately so — there the
	// alternative was refusing to start over a rule, here the alternative is a
	// principal who cannot get in at all.
	if err := c.Tokens.Error(); err != nil {
		return fmt.Errorf("refusing to serve gRPC: %w", err)
	}
	if c.Enabled() || isLoopbackAddr(c.Addr) {
		return nil
	}
	return fmt.Errorf(
		"refusing to serve gRPC on %s without authentication: this address is reachable from other machines "+
			"and the API can submit and cancel workflows. Set %s (or %s for per-principal identities), "+
			"or bind loopback (FORGE_GRPC_ADDR=127.0.0.1:50051)",
		c.Addr, EnvSecret, EnvTokens)
}

// LogPosture states which mode the server is in. An operator should not have to
// infer whether their API is locked from the absence of a log line.
//
// The identity half is reported separately from the locked half, because they
// are genuinely different questions: a deployment can be closed to the world and
// still unable to say who called. Saying only "auth enabled" would let an
// operator believe attribution works when it does not.
func (c Config) LogPosture() {
	who := fmt.Sprintf("%s with %d principal(s)", EnvTokens, c.Tokens.Len())
	if !c.Tokens.Configured() {
		who = "no per-principal identities (" + EnvTokens + " unset): callers are indistinguishable"
	}

	switch {
	case c.Enabled() && isLoopbackAddr(c.Addr):
		log.Printf("INFO: gRPC auth enabled on %s (bound loopback); %s", c.Addr, who)
	case c.Enabled():
		log.Printf("INFO: gRPC auth enabled on %s; %s", c.Addr, who)
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
		authed, err := c.authorize(ctx)
		if err != nil {
			return nil, err
		}
		return handler(authed, req)
	}
}

// StreamInterceptor authenticates a streaming call.
func (c Config) StreamInterceptor() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		authed, err := c.authorize(ss.Context())
		if err != nil {
			return err
		}
		return handler(srv, &authenticatedStream{ServerStream: ss, ctx: authed})
	}
}

// authorize checks the credential on a call and returns the context the handler
// should see.
//
// It returns a context rather than only an error because a valid token
// ESTABLISHES an identity: the principal the token belongs to becomes a verified
// Actor on the context. Returning only an error would leave the caller to
// re-derive that, and the second derivation would be a second place that decides
// what a token means.
func (c Config) authorize(ctx context.Context) (context.Context, error) {
	presented := secretFromContext(ctx)

	// A per-principal token is tried first, and it wins.
	//
	// Order matters for a security reason rather than a stylistic one: if the
	// shared secret were accepted first, a caller holding it could also state any
	// identity in the header and that declaration — being the only one present —
	// would be used. Trying the token first means that when one is valid, the
	// principal comes from the credential instead of from a claim.
	if actor, ok := c.Tokens.Resolve(presented); ok {
		auditSuccess(actor.ID(), actor.Verified(), actor.Verifier())
		return identity.WithActor(ctx, actor), nil
	}

	// A caller with a token configured but a WRONG one falls through to the
	// shared secret rather than being rejected here, so that a deployment adding
	// tokens does not lock out clients still using the old credential. The
	// rejection below is the same one an unknown credential always got.
	if c.Secret == "" {
		if c.Tokens.Configured() {
			auditFailure(tokenFingerprint(presented), true, false)
			return nil, UnauthenticatedError
		}
		// No credential is configured at all. Validate has already refused a
		// routable bind in that state, so reaching here means loopback, where the
		// only callers are on this machine.
		//
		// Nothing is audited: there was no authentication to record, and a line
		// per call would bury the real events under routine loopback traffic.
		return ctx, nil
	}

	// Equal lengths first: ConstantTimeCompare returns 0 immediately for
	// different lengths, and there is no way around that, but doing the length
	// check here keeps the intent explicit rather than implicit in the stdlib.
	if len(presented) != len(c.Secret) {
		auditFailure(tokenFingerprint(presented), c.Tokens.Configured(), true)
		return nil, UnauthenticatedError
	}
	// Constant time, so the reply carries no information about how much of the
	// secret was right. `==` on strings stops at the first differing byte, which
	// is enough to recover a secret one character at a time.
	if subtle.ConstantTimeCompare([]byte(presented), []byte(c.Secret)) != 1 {
		auditFailure(tokenFingerprint(presented), c.Tokens.Configured(), true)
		return nil, UnauthenticatedError
	}

	// The shared secret admits the caller but establishes NO identity: every
	// holder of it is indistinguishable from every other, so there is no
	// principal to record. The context is returned unchanged, which lets the
	// identity interceptor fill in an unverified declaration if the caller made
	// one — the correct outcome, and an honest one.
	//
	// The call is still audited, with no principal and the mechanism named. An
	// investigation asking "was this the shared secret or a token" needs the
	// distinction, and a line that omitted it would be indistinguishable from a
	// token call with no identity.
	auditSuccess("", false, "shared-secret")
	return ctx, nil
}

// authenticatedStream overrides Context so the handler sees the context
// authorize produced, including any verified principal.
type authenticatedStream struct {
	grpc.ServerStream
	ctx context.Context
}

// Context implements grpc.ServerStream.
func (s *authenticatedStream) Context() context.Context { return s.ctx }

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
// A per-principal token is preferred when BOTH are set. The client presents one
// credential, not two: the server accepts either, and sending the token is what
// makes the call attributable. A deployment that has issued a token for this
// process wants that identity used, not the anonymous one the shared secret
// gives it.
//
// Neither configured means no option: a client talking to an unauthenticated
// loopback server should not have to know that.
func ClientOptions() []grpc.DialOption {
	credential := strings.TrimSpace(os.Getenv(EnvClientToken))
	if credential == "" {
		credential = strings.TrimSpace(os.Getenv(EnvSecret))
	}
	if credential == "" {
		return nil
	}
	return []grpc.DialOption{
		grpc.WithChainUnaryInterceptor(clientUnaryInterceptor(credential)),
		grpc.WithChainStreamInterceptor(clientStreamInterceptor(credential)),
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
