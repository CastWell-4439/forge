package identity

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// The gRPC interceptor that records a caller's declared identity.
//
// It is a SEPARATE interceptor rather than a branch inside the tracing one,
// because the two concerns are unrelated: tracing records what happened, this
// records who asked. Folding identity into the tracer would mean a deployment
// that turned tracing off silently lost attribution — and attribution is the
// thing an audit trail cannot do without.
//
// Ordering matters in one direction only: this must run before any handler that
// reads FromContext, which the chain guarantees for a server interceptor
// registered anywhere in it.
//
// It never rejects. A call with no declared identity proceeds as "unattributed",
// because refusing unauthenticated-but-unidentified callers would break every
// existing client for a field that is not yet verified anyway. When per-principal
// credentials arrive, the check belongs here — and it will be a different
// interceptor, so that the recording and the enforcing can be enabled
// separately.

// UnaryServerInterceptor records the declared actor on the context.
func UnaryServerInterceptor() grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		return handler(withDeclaredActor(ctx), req)
	}
}

// StreamServerInterceptor records the declared actor on the stream's context.
//
// A stream needs its own interceptor: wrapping the context once at the start is
// what makes the actor visible to the handler for the whole stream, and a
// unary-only interceptor would leave every streaming call unattributed.
func StreamServerInterceptor() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		return handler(srv, &actorStream{ServerStream: ss, ctx: withDeclaredActor(ss.Context())})
	}
}

// withDeclaredActor reads the actor from incoming metadata and stores it.
func withDeclaredActor(ctx context.Context) context.Context {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		// No metadata at all: an in-process call or a transport that does not
		// carry it. The zero Actor is stored rather than nothing, so a reader can
		// tell "checked, nothing declared" from "nobody looked".
		return WithActor(ctx, Actor{})
	}
	return WithActor(ctx, FromGRPC(md))
}

// actorStream overrides Context so the handler sees the actor-carrying context.
type actorStream struct {
	grpc.ServerStream
	ctx context.Context
}

// Context implements grpc.ServerStream.
func (s *actorStream) Context() context.Context { return s.ctx }
