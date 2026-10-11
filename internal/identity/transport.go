package identity

import (
	"net/http"
	"strings"

	"google.golang.org/grpc/metadata"
)

// The transport headers a caller states its identity in.
//
// Both transports use the same name so a deployment documents one thing. The
// value is a DECLARATION: nothing verifies it in this round, and the Actor it
// produces says so.
const (
	// GRPCMetadataKey is the incoming gRPC metadata key. gRPC lowercases metadata
	// keys, so the constant is lowercase and lookups normalise.
	GRPCMetadataKey = "x-forge-actor"
	// HTTPHeader is the HTTP header name, in its canonical form.
	HTTPHeader = "X-Forge-Actor"
)

// FromGRPC reads a declared actor from incoming gRPC metadata.
//
// It is deliberately tolerant of the key's case: gRPC normalises keys to
// lowercase, but a test or an in-process caller may not, and a lookup that
// missed because of case would silently produce "unattributed" — the one outcome
// an audit trail must not produce by accident.
func FromGRPC(md metadata.MD) Actor {
	if md == nil {
		return Actor{}
	}
	if values := md.Get(GRPCMetadataKey); len(values) > 0 {
		return NewDeclared(values[0])
	}
	// Fall back to a case-insensitive scan. Metadata keys are specified as
	// lowercase, so this only fires for callers that did not follow the spec —
	// and reading their declaration is better than dropping it.
	for key, values := range md {
		if strings.EqualFold(key, GRPCMetadataKey) && len(values) > 0 {
			return NewDeclared(values[0])
		}
	}
	return Actor{}
}

// FromHTTP reads a declared actor from request headers.
func FromHTTP(h http.Header) Actor {
	if h == nil {
		return Actor{}
	}
	if v := h.Get(HTTPHeader); v != "" {
		return NewDeclared(v)
	}
	return Actor{}
}
