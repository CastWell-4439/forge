package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// Protocol versions and the negotiation that replaced the handshake (H1).
//
// The 2026-07-28 revision removed the initialize handshake and protocol-level
// sessions entirely: there is no negotiation step, every request declares the
// version it speaks in its `_meta`, and a server that does not implement that
// version answers with UnsupportedProtocolVersionError listing what it does
// support. The client picks from that list and retries.
//
// We stay DUAL-ERA on purpose. The ecosystem still runs many servers built on
// the handshake revisions, and dropping them would silently disconnect every
// existing MCP integration. So: probe for the modern revision first, and fall
// back to the legacy handshake when the server does not know it.
const (
	// ProtocolModern is the per-request-metadata revision: no handshake, no
	// session, version and capabilities travel with each request.
	ProtocolModern = "2026-07-28"
	// ProtocolLegacy is the handshake revision this client used before, kept
	// as the fallback for servers that predate the modern revision.
	ProtocolLegacy = "2024-11-05"
	// ProtocolLegacyPrevious is the revision between the two. Listed so the
	// client can recognise it when a server offers it as its newest supported
	// version; we speak it through the same legacy path (the handshake shape is
	// compatible).
	ProtocolLegacyPrevious = "2025-11-25"

	// unsupportedProtocolVersionCode is the JSON-RPC error code the modern
	// specification reserves for "I do not speak that version".
	unsupportedProtocolVersionCode = -32022
)

// KnownProtocolVersions is what this client can speak, newest first. It is a
// closed list rather than a "latest" constant: we only claim versions whose
// behaviour we actually implement.
var KnownProtocolVersions = []string{ProtocolModern, ProtocolLegacyPrevious, ProtocolLegacy}

// ModernProtocols reports whether a version uses per-request metadata (as
// opposed to a session established by a handshake).
func ModernProtocols(version string) bool {
	return version == ProtocolModern
}

// metaKey* are the reserved `_meta` keys the modern revision defines for
// per-request identity, version and capabilities.
const (
	metaKeyProtocolVersion    = "io.modelcontextprotocol/protocolVersion"
	metaKeyClientCapabilities = "io.modelcontextprotocol/clientCapabilities"
	metaKeyClientInfo         = "io.modelcontextprotocol/clientInfo"
)

// clientCapabilities is what this client can do. Only the features we actually
// implement are declared: advertising a capability we do not honour would make
// a server structure its replies around something that never happens.
//
// Sampling, roots and elicitation are NOT declared — those are exactly the
// server-to-client requests H4 still has to implement.
type clientCapabilities struct {
	Extensions map[string]json.RawMessage `json:"extensions,omitempty"`
}

// metaEnvelope builds the `_meta` object every modern request carries.
func metaEnvelope() map[string]any {
	return map[string]any{
		metaKeyProtocolVersion:    ProtocolModern,
		metaKeyClientCapabilities: clientCapabilities{},
		metaKeyClientInfo: map[string]string{
			"name":    "forge-agent",
			"version": "1.0.0",
		},
	}
}

// unsupportedProtocolError extracts the version list from an
// UnsupportedProtocolVersionError. ok is false for any other error, including
// ordinary JSON-RPC failures.
func unsupportedProtocolError(err error) (supported []string, requested string, ok bool) {
	var rpcErr *ResponseError
	if !errors.As(err, &rpcErr) {
		return nil, "", false
	}
	if rpcErr.Code != unsupportedProtocolVersionCode {
		return nil, "", false
	}
	var data struct {
		Supported []string `json:"supported"`
		Requested string   `json:"requested"`
	}
	if len(rpcErr.Data) == 0 {
		return nil, "", true
	}
	if uerr := json.Unmarshal(rpcErr.Data, &data); uerr != nil {
		// The code is unambiguous even when the payload is not; report the
		// condition without a list rather than swallowing it.
		return nil, "", true
	}
	return data.Supported, data.Requested, true
}

// selectProtocol picks the best version both sides can speak, preferring our
// order of preference (newest first).
func selectProtocol(serverSupported []string) (string, bool) {
	for _, ours := range KnownProtocolVersions {
		for _, theirs := range serverSupported {
			if ours == theirs {
				return ours, true
			}
		}
	}
	return "", false
}

// negotiateProtocol establishes which revision this connection will use.
//
// Modern first: on STDIO a probe is the only way to find out (there is no
// handshake to fail), and on HTTP it saves a doomed first request. A server
// that does not know `server/discover` is a legacy server, so we fall back to
// the handshake path rather than treating its refusal as a failure.
func (c *Client) negotiateProtocol(ctx context.Context) error {
	// --- Try the modern revision ---
	discovered, err := c.discover(ctx)
	if err == nil {
		// The server answered. Pick a mutually supported version; if none
		// exists, say so with both lists rather than guessing.
		version, ok := selectProtocol(discovered.ProtocolVersions)
		if !ok {
			return fmt.Errorf("mcp: no common protocol version (client speaks %v, server offers %v)",
				KnownProtocolVersions, discovered.ProtocolVersions)
		}
		c.version = version
		c.modern = ModernProtocols(version)
		if discovered.ServerInfo != nil {
			c.info = *discovered.ServerInfo
		}
		return nil
	}

	if supported, requested, ok := unsupportedProtocolError(err); ok {
		// The server knows the modern revision exists but not the one we
		// asked for. Choose from its list — this is the exact flow the
		// specification prescribes.
		version, found := selectProtocol(supported)
		if !found {
			return fmt.Errorf("mcp: no common protocol version (client speaks %v, server offers %v; requested %q)",
				KnownProtocolVersions, supported, requested)
		}
		c.version = version
		c.modern = ModernProtocols(version)
		return nil
	}

	// --- Legacy fallback ---
	// A server that rejects server/discover predates the per-request-metadata
	// revision. That is not an error: the handshake is how we spoke to it
	// before, and it still works.
	if err := c.initializeLegacy(ctx); err != nil {
		return fmt.Errorf("mcp: neither modern nor legacy handshake succeeded: %w", err)
	}
	c.version = ProtocolLegacy
	c.modern = false
	return nil
}

// discoverResult is what server/discover returns: the server's supported
// versions, its capabilities and its identity.
type discoverResult struct {
	ProtocolVersions []string    `json:"protocolVersions"`
	Capabilities     interface{} `json:"capabilities,omitempty"`
	ServerInfo       *ServerInfo `json:"serverInfo,omitempty"`
}

// discover calls server/discover. An older server answers with a method-not-found
// error, which the caller treats as "legacy".
func (c *Client) discover(ctx context.Context) (discoverResult, error) {
	resp, err := c.callMeta(ctx, "server/discover", nil)
	if err != nil {
		return discoverResult{}, err
	}
	var result discoverResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return discoverResult{}, fmt.Errorf("parse server/discover result: %w", err)
	}
	if len(result.ProtocolVersions) == 0 {
		return discoverResult{}, fmt.Errorf("mcp: server/discover returned no protocol versions")
	}
	return result, nil
}

// initializeLegacy performs the handshake revisions' initialisation.
func (c *Client) initializeLegacy(ctx context.Context) error {
	params := initializeParams{
		ProtocolVersion: ProtocolLegacy,
		ClientInfo:      clientInfo{Name: "forge-agent", Version: "1.0.0"},
	}
	resp, err := c.call(ctx, "initialize", params)
	if err != nil {
		return err
	}
	var result initializeResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return fmt.Errorf("parse initialize result: %w", err)
	}
	c.info = result.ServerInfo
	return c.transport.Notify(ctx, Notification{Method: "notifications/initialized"})
}
