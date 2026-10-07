package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/castwell/forge/internal/agent/core"
)

// scriptedTransport answers a scripted sequence, recording what was asked.
// It lets the negotiation be tested without a server process.
type scriptedTransport struct {
	// responder maps method -> response. A nil entry means "answer with a
	// method-not-found error", which is how a server says it does not know a
	// method.
	responder map[string]func(Request) Response
	sent      []Request
	notifies  []Notification
	closed    bool
}

func (s *scriptedTransport) Send(_ context.Context, req Request) (Response, error) {
	s.sent = append(s.sent, req)
	if fn, ok := s.responder[req.Method]; ok && fn != nil {
		return fn(req), nil
	}
	// Method not found — what an older server answers to server/discover.
	return Response{ID: req.ID, Error: &ResponseError{Code: CodeMethodNotFound, Message: "method not found"}}, nil
}

func (s *scriptedTransport) Notify(_ context.Context, n Notification) error {
	s.notifies = append(s.notifies, n)
	return nil
}

func (s *scriptedTransport) Close() error { s.closed = true; return nil }

func result(t *testing.T, v any) Response {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	return Response{Result: raw}
}

// A modern server is negotiated by probe, with no handshake at all: the spec
// removed it, so a correct client must not send initialize.
func TestModernNegotiationSkipsHandshake(t *testing.T) {
	tr := &scriptedTransport{responder: map[string]func(Request) Response{
		"server/discover": func(req Request) Response {
			return result(t, discoverResult{ProtocolVersions: []string{ProtocolModern}})
		},
		// initialize must NOT be needed; answering it would hide a client bug.
		"initialize": nil,
	}}

	client, err := NewClient(context.Background(), tr)
	require.NoError(t, err)

	assert.True(t, client.IsModern(), "the modern revision was chosen")
	assert.Equal(t, ProtocolModern, client.ProtocolVersion())

	for _, req := range tr.sent {
		assert.NotEqual(t, "initialize", req.Method, "the modern revision has no handshake")
	}
	assert.Empty(t, tr.notifies, "and no initialized notification")
}

// Every modern request carries the `_meta` envelope: version, capabilities and
// identity travel per request once the session is gone.
func TestModernRequestsCarryMeta(t *testing.T) {
	tr := &scriptedTransport{responder: map[string]func(Request) Response{
		"server/discover": func(req Request) Response {
			return result(t, discoverResult{ProtocolVersions: []string{ProtocolModern}})
		},
		"tools/list": func(req Request) Response {
			return result(t, toolsListResult{})
		},
	}}

	client, err := NewClient(context.Background(), tr)
	require.NoError(t, err)
	_, err = client.ListTools(context.Background())
	require.NoError(t, err)

	var listReq *Request
	for i := range tr.sent {
		if tr.sent[i].Method == "tools/list" {
			listReq = &tr.sent[i]
		}
	}
	require.NotNil(t, listReq, "tools/list was sent")

	var params map[string]any
	require.NoError(t, json.Unmarshal(listReq.Params, &params))
	meta, ok := params["_meta"].(map[string]any)
	require.True(t, ok, "the request carries _meta: %v", params)
	assert.Equal(t, ProtocolModern, meta[metaKeyProtocolVersion])
	assert.Contains(t, meta, metaKeyClientCapabilities)
	assert.Contains(t, meta, metaKeyClientInfo)
}

// The probe's own request carries _meta too: it is a request, and once the
// handshake is gone every request declares its version.
func TestDiscoverProbeCarriesMeta(t *testing.T) {
	tr := &scriptedTransport{responder: map[string]func(Request) Response{
		"server/discover": func(req Request) Response {
			return result(t, discoverResult{ProtocolVersions: []string{ProtocolModern}})
		},
	}}
	_, err := NewClient(context.Background(), tr)
	require.NoError(t, err)

	require.NotEmpty(t, tr.sent)
	var params map[string]any
	require.NoError(t, json.Unmarshal(tr.sent[0].Params, &params))
	assert.Contains(t, params, "_meta")
}

// A server that does not know server/discover is a legacy server: we fall back
// to the handshake rather than treating its refusal as a failure. Dropping
// these would silently disconnect every existing MCP integration.
func TestLegacyFallbackUsesHandshake(t *testing.T) {
	tr := &scriptedTransport{responder: map[string]func(Request) Response{
		// server/discover: absent → method not found
		"initialize": func(req Request) Response {
			return result(t, initializeResult{
				ProtocolVersion: ProtocolLegacy,
				ServerInfo:      ServerInfo{Name: "legacy-server", Version: "0.1"},
			})
		},
	}}

	client, err := NewClient(context.Background(), tr)
	require.NoError(t, err)

	assert.False(t, client.IsModern(), "a legacy server speaks the handshake revision")
	assert.Equal(t, ProtocolLegacy, client.ProtocolVersion())
	assert.Equal(t, "legacy-server", client.ServerName())

	// The handshake shape is preserved: initialize, then initialized.
	var sawInit, sawNotify bool
	for _, req := range tr.sent {
		if req.Method == "initialize" {
			sawInit = true
		}
	}
	for _, n := range tr.notifies {
		if n.Method == "notifications/initialized" {
			sawNotify = true
		}
	}
	assert.True(t, sawInit, "the handshake was attempted")
	assert.True(t, sawNotify, "and completed")
}

// When the server answers the probe with UnsupportedProtocolVersionError, the
// client picks from the list it was given — the exact flow the spec prescribes.
func TestUnsupportedVersionSelectsFromServerList(t *testing.T) {
	tr := &scriptedTransport{responder: map[string]func(Request) Response{
		"server/discover": func(req Request) Response {
			data, _ := json.Marshal(map[string]any{
				"supported": []string{ProtocolLegacyPrevious},
				"requested": ProtocolModern,
			})
			return Response{ID: req.ID, Error: &ResponseError{
				Code: unsupportedProtocolVersionCode, Message: "Unsupported protocol version", Data: data,
			}}
		},
	}}

	client, err := NewClient(context.Background(), tr)
	require.NoError(t, err, "a version mismatch is negotiated, not fatal")
	assert.Equal(t, ProtocolLegacyPrevious, client.ProtocolVersion())
	assert.False(t, client.IsModern(), "2025-11-25 still uses the handshake shape")
}

// No overlap means no way to talk: say so with both lists rather than guessing.
func TestNoCommonVersionIsAnError(t *testing.T) {
	tr := &scriptedTransport{responder: map[string]func(Request) Response{
		"server/discover": func(req Request) Response {
			return result(t, discoverResult{ProtocolVersions: []string{"1900-01-01"}})
		},
	}}

	_, err := NewClient(context.Background(), tr)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no common protocol version")
	assert.Contains(t, err.Error(), "1900-01-01", "the server's list is reported")
}

// selectProtocol prefers our newest supported version, whatever order the
// server lists its own.
func TestSelectProtocolPrefersNewest(t *testing.T) {
	v, ok := selectProtocol([]string{ProtocolLegacy, ProtocolModern, ProtocolLegacyPrevious})
	require.True(t, ok)
	assert.Equal(t, ProtocolModern, v)

	v, ok = selectProtocol([]string{ProtocolLegacy})
	require.True(t, ok)
	assert.Equal(t, ProtocolLegacy, v)

	_, ok = selectProtocol([]string{"9999-01-01"})
	assert.False(t, ok, "an unknown version is not adopted")
}

// ModernProtocols classifies revisions, which is what decides whether requests
// carry `_meta`.
func TestModernProtocolsClassification(t *testing.T) {
	assert.True(t, ModernProtocols(ProtocolModern))
	assert.False(t, ModernProtocols(ProtocolLegacy))
	assert.False(t, ModernProtocols(ProtocolLegacyPrevious))
	assert.False(t, ModernProtocols(""))
}

// The error decoder must not mistake an ordinary failure for a version
// mismatch — that would send negotiation down the wrong path.
func TestUnsupportedProtocolErrorOnlyForItsCode(t *testing.T) {
	_, _, ok := unsupportedProtocolError(&ResponseError{Code: CodeMethodNotFound, Message: "nope"})
	assert.False(t, ok)

	_, _, ok = unsupportedProtocolError(assert.AnError)
	assert.False(t, ok)

	data, _ := json.Marshal(map[string]any{"supported": []string{"a"}, "requested": "b"})
	supported, requested, ok := unsupportedProtocolError(&ResponseError{
		Code: unsupportedProtocolVersionCode, Data: data,
	})
	require.True(t, ok)
	assert.Equal(t, []string{"a"}, supported)
	assert.Equal(t, "b", requested)

	// The code alone is enough, even with an unreadable payload.
	_, _, ok = unsupportedProtocolError(&ResponseError{Code: unsupportedProtocolVersionCode})
	assert.True(t, ok, "an unambiguous code is acted on even without a list")
}

// --- H5: annotation mapping ---

// A destruction claim is believed: believing it can only make us more careful.
func TestAnnotationsDestructiveBecomesDelete(t *testing.T) {
	yes := true
	assert.Equal(t, core.EffectDelete, effectFromAnnotations(&core.MCPToolAnnotations{DestructiveHint: &yes}))
}

// A read-only claim is accepted, but only on its own axis.
func TestAnnotationsReadOnlyBecomesRead(t *testing.T) {
	yes := true
	assert.Equal(t, core.EffectRead, effectFromAnnotations(&core.MCPToolAnnotations{ReadOnlyHint: &yes}))
}

// No annotation means write: the cautious middle, same as an undeclared local
// tool. A remote server gets no benefit of the doubt it did not ask for.
func TestAnnotationsAbsentMeansWrite(t *testing.T) {
	assert.Equal(t, core.EffectWrite, effectFromAnnotations(nil))
	assert.Equal(t, core.EffectWrite, effectFromAnnotations(&core.MCPToolAnnotations{}))
}

// Contradictory claims resolve toward caution: a server saying "read-only AND
// destructive" gets the destructive reading.
func TestAnnotationsContradictionResolvesCautiously(t *testing.T) {
	yes := true
	assert.Equal(t, core.EffectDelete, effectFromAnnotations(&core.MCPToolAnnotations{
		ReadOnlyHint: &yes, DestructiveHint: &yes,
	}))
}

// An explicit false is not "no opinion": readOnlyHint=false means the server
// says it writes, which is write — not the undeclared default.
func TestAnnotationsExplicitFalseIsRespected(t *testing.T) {
	no := false
	assert.Equal(t, core.EffectWrite, effectFromAnnotations(&core.MCPToolAnnotations{ReadOnlyHint: &no}))
	assert.Equal(t, core.EffectWrite, effectFromAnnotations(&core.MCPToolAnnotations{DestructiveHint: &no}))
}

// The bridge carries annotations from the wire into the tool definition, so the
// permission gate can see them.
func TestBridgeMapsAnnotationsToEffect(t *testing.T) {
	readOnly := true
	def := &core.ToolDef{}
	_ = def
	// exercised end to end through Sync in the manager tests; here we pin the
	// mapping used by the bridge.
	assert.Equal(t, core.EffectRead, effectFromAnnotations(&core.MCPToolAnnotations{ReadOnlyHint: &readOnly}))
}
