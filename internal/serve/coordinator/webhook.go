package coordinator

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/castwell/forge/internal/coordinator"
	"github.com/castwell/forge/internal/registry"
)

// Workflow entry points: the HTTP side of the `webhook` and `manual` triggers.
//
// The YAML has always allowed both types — the parser accepts them and says so
// ("Recognised, wired elsewhere (webhook needs an HTTP entry point)") — but
// nothing ever listened. This file is that entry point. The submit chain
// (registry dialect -> bridge -> SubmitDAG) already existed for cron and poll;
// these handlers only decide WHO may call it and with WHAT payload.
//
// Two paths, because they are two different acts:
//
//	POST /api/v1/hooks/{workflow}        an external event arrived
//	POST /api/v1/workflows/{name}/run    an operator wants this to run now
//
// Security posture, in one paragraph: a hook endpoint is remotely reachable, so
// it must never be open by default. When a secret is configured, requests are
// authenticated by HMAC-SHA256 over the body (which also gives integrity — a
// payload relayed through a proxy cannot be altered unnoticed). Without a
// secret the endpoints are registered ONLY when the HTTP listener is bound to
// loopback, where the only caller is the machine itself; bound to a routable
// address without a secret they are not registered at all, and the log says
// which variable is missing. Friction therefore appears exactly when the
// service becomes reachable by someone else — which is when it is needed.
//
// What this is NOT: system-wide authentication. The coordinator's gRPC port
// remains unauthenticated (tracked separately), so a secret here authenticates
// the SENDER of a hook, not the caller of the API. Saying so plainly is more
// useful than implying the system is now locked down.

// Environment variables for the entry points.
//
//	FORGE_WEBHOOK_SECRET   shared secret for hook/run authentication. Unset
//	                       means "no secret"; whether that is acceptable
//	                       depends on the bind address (see above).
//	FORGE_HTTP_ADDR        listener address; defaults to loopback (127.0.0.1:9090)
//	                       so exposing it is a deliberate act.
const (
	envWebhookSecret = "FORGE_WEBHOOK_SECRET"
	envHTTPAddr      = "FORGE_HTTP_ADDR"

	// signatureHeader carries the hex HMAC of the raw body.
	signatureHeader = "X-Forge-Signature"
	// timestampHeader carries the unix seconds the sender signed at. It is part
	// of the signed material, so it cannot be altered without breaking the
	// signature — which is what makes the freshness window meaningful.
	timestampHeader = "X-Forge-Timestamp"

	// webhookTimestampSkew is how far a signature may be from now. A wider
	// window is friendlier to clock skew; a narrower one is safer against
	// replay. Five minutes is the usual compromise.
	webhookTimestampSkew = 5 * time.Minute

	// defaultHTTPAddr binds loopback. Exposing the API to a network is then an
	// explicit choice, and the secret requirement follows from it.
	defaultHTTPAddr = "127.0.0.1:9090"
)

// workflowEntryPoints serves the webhook and manual triggers.
type workflowEntryPoints struct {
	registry *registry.Registry
	coord    *coordinator.Coordinator
	secret   []byte
	// dedup remembers recently accepted requests so a replayed body does not
	// start a second run (see triggerDedup).
	dedup *triggerDedup
}

// httpAddr resolves the listener address, defaulting to loopback.
func httpAddr() string {
	return envOrDefault(envHTTPAddr, defaultHTTPAddr)
}

// isLoopbackAddr reports whether an address only accepts local connections.
//
// This is the switch the whole posture hangs on, so it is deliberately strict:
// an address we cannot parse is treated as NOT loopback, which means it must
// have a secret.
func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		// ":9090" (all interfaces) parses with an empty host; a bare port does
		// not. Either way an unparseable address is not provably local.
		host = strings.TrimSuffix(addr, ":")
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return false // empty host = all interfaces
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// registerWorkflowEntryPoints mounts the handlers, or explains why it will not.
//
// The decision is made once, at startup, from the bind address and the presence
// of a secret — not per request. A per-request check would leave the endpoint
// reachable and merely refuse the call, which is a worse default than not
// listening at all.
func registerWorkflowEntryPoints(mux *http.ServeMux, coord *coordinator.Coordinator) {
	secret := strings.TrimSpace(os.Getenv(envWebhookSecret))
	addr := httpAddr()
	loopback := isLoopbackAddr(addr)

	if secret == "" && !loopback {
		log.Printf("INFO: webhook/manual entry points NOT registered: %s is bound to %s (not loopback) "+
			"and no %s is set. Set %s to enable them, or bind %s to a loopback address.",
			envHTTPAddr, addr, envWebhookSecret, envWebhookSecret, envHTTPAddr)
		return
	}

	// The entry points read the same workflow directory the trigger assembly
	// does; loading it here keeps the two independent (one being off must not
	// disable the other).
	workflowsDir := envOrDefault(envWorkflowsDir, defaultWorkflows)
	reg := registry.NewRegistry()
	if err := reg.Load(workflowsDir); err != nil {
		log.Printf("WARN: webhook/manual entry points NOT registered: load workflows from %s: %v", workflowsDir, err)
		return
	}

	ep := &workflowEntryPoints{
		registry: reg,
		coord:    coord,
		dedup:    newTriggerDedup(triggerDedupTTL()),
	}
	if secret != "" {
		ep.secret = []byte(secret)
	}

	mux.HandleFunc("/api/v1/hooks/", ep.handleWebhook)
	mux.HandleFunc("/api/v1/workflows/", ep.handleManualRun)

	if secret == "" {
		log.Printf("WARN: webhook/manual entry points registered WITHOUT authentication: %s is bound to %s "+
			"(loopback only) and no %s is set. Local processes may trigger any declared workflow; "+
			"set %s before exposing this listener.",
			envHTTPAddr, addr, envWebhookSecret, envWebhookSecret)
	} else {
		log.Printf("INFO: webhook/manual entry points registered with HMAC authentication (addr=%s, workflows=%s)",
			addr, workflowsDir)
	}
}

// handleWebhook accepts an external event for a workflow that DECLARED a
// webhook trigger. The declaration is the allow-list: a workflow without one
// cannot be triggered here, however well-formed the request.
func (ep *workflowEntryPoints) handleWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "webhook endpoints accept POST")
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/api/v1/hooks/")
	if name == "" {
		writeJSONError(w, http.StatusBadRequest, "missing workflow name in the path")
		return
	}

	body, ok := ep.authenticate(w, r)
	if !ok {
		return
	}

	cw, err := ep.registry.Get(name)
	if err != nil {
		// Unknown workflow and undeclared hook are answered identically: a
		// different response would let a caller enumerate which workflows exist.
		writeJSONError(w, http.StatusNotFound, fmt.Sprintf("no workflow %q with a webhook trigger", name))
		return
	}
	if !declaresTrigger(cw, "webhook") {
		writeJSONError(w, http.StatusNotFound, fmt.Sprintf("no workflow %q with a webhook trigger", name))
		return
	}

	inputs, err := decodeInputs(body)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Everything checks out: from here the request WILL run something, so this
	// is where a replay becomes meaningful.
	if !ep.claimSignature(w, r) {
		return
	}

	ep.submit(w, r, cw, inputs, "webhook")
}

// handleManualRun runs a workflow on demand. Unlike a hook it does not require
// a declared webhook trigger: the operator is asking for this workflow by name,
// and every workflow is runnable by hand — that is what "manual" means.
func (ep *workflowEntryPoints) handleManualRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "run endpoints accept POST")
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/workflows/")
	name, ok := strings.CutSuffix(path, "/run")
	if !ok || name == "" {
		// Shape is checked BEFORE authentication: a malformed path is a client
		// mistake, and answering 401 for it would suggest the request was
		// well-formed but unauthenticated.
		writeJSONError(w, http.StatusBadRequest, "expected POST /api/v1/workflows/{name}/run")
		return
	}

	body, ok := ep.authenticate(w, r)
	if !ok {
		return
	}

	cw, err := ep.registry.Get(name)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, fmt.Sprintf("no workflow named %q", name))
		return
	}

	inputs, err := decodeInputs(body)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	if !ep.claimSignature(w, r) {
		return
	}

	ep.submit(w, r, cw, inputs, "manual")
}

// authenticate verifies the request when a secret is configured, and reports
// whether the caller may proceed. The response is already written on refusal.
//
// It returns the body so the caller does not read the stream twice — the body
// must be intact for the signature check, so it is read here and passed on.
func (ep *workflowEntryPoints) authenticate(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBody))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "could not read the request body")
		return nil, false
	}

	if len(ep.secret) == 0 {
		// No secret: only reachable when the listener is loopback-bound, which
		// registerWorkflowEntryPoints established at startup.
		return body, true
	}

	signature := strings.TrimSpace(r.Header.Get(signatureHeader))
	timestamp := strings.TrimSpace(r.Header.Get(timestampHeader))
	if signature == "" || timestamp == "" {
		// One message for both: saying which header is missing helps an
		// attacker more than an operator (the operator has the docs).
		writeJSONError(w, http.StatusUnauthorized, "missing "+signatureHeader+" or "+timestampHeader)
		return nil, false
	}

	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusUnauthorized, "malformed "+timestampHeader)
		return nil, false
	}
	drift := time.Since(time.Unix(ts, 0))
	if drift < 0 {
		drift = -drift
	}
	if drift > webhookTimestampSkew {
		// Outside the window the request is either badly skewed or replayed;
		// either way it is not accepted.
		writeJSONError(w, http.StatusUnauthorized,
			fmt.Sprintf("%s is outside the %s window", timestampHeader, webhookTimestampSkew))
		return nil, false
	}

	if !verifySignature(ep.secret, timestamp, body, signature) {
		writeJSONError(w, http.StatusUnauthorized, "signature mismatch")
		return nil, false
	}

	// A valid signature can still be REPLAYED, so it is recorded — but only
	// once the request is going to be ACTED ON. Marking it here, before the
	// caller has resolved the workflow, would let a request that found nothing
	// burn the signature and turn a retry after a fix into a 409.
	return body, true
}

// claimSignature records a signature as handled, refusing a replay. It is
// called after the request is known to be actionable (the workflow exists and
// the payload parsed), so a request that could not have run anything does not
// consume the caller's one attempt.
func (ep *workflowEntryPoints) claimSignature(w http.ResponseWriter, r *http.Request) bool {
	if len(ep.secret) == 0 {
		return true
	}
	if !ep.dedup.firstSeen(r.Header.Get(signatureHeader)) {
		writeJSONError(w, http.StatusConflict, "this request was already accepted")
		return false
	}
	return true
}

// maxWebhookBody caps the payload. An entry point reachable by others must not
// let one request exhaust memory.
const maxWebhookBody = 1 << 20 // 1 MiB

// verifySignature recomputes the HMAC over timestamp+body and compares in
// constant time.
//
// The timestamp is inside the signed material on purpose: signing only the body
// would let an attacker change the timestamp to keep a captured request inside
// the freshness window.
func verifySignature(secret []byte, timestamp string, body []byte, provided string) bool {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(timestamp))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(strings.ToLower(provided)))
}

// signBody produces the signature a caller must send. Exported for the CLI
// helper and for tests; keeping one implementation means the helper and the
// server cannot disagree about what is signed.
func signBody(secret []byte, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(timestamp))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// decodeInputs turns the request body into the workflow's inputs.
//
// An empty body is allowed (a manual run with no parameters is the common
// case). A non-empty body must be a JSON object: an array or a bare string has
// no field names to render into the workflow's inputs, so accepting it would
// only move the failure deeper.
func decodeInputs(body []byte) (map[string]any, error) {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return map[string]any{}, nil
	}
	var inputs map[string]any
	if err := json.Unmarshal(body, &inputs); err != nil {
		return nil, fmt.Errorf("request body must be a JSON object of inputs: %w", err)
	}
	return inputs, nil
}

// submit hands the request to the same path cron and poll use.
func (ep *workflowEntryPoints) submit(w http.ResponseWriter, r *http.Request, cw *registry.CompiledWorkflow, inputs map[string]any, source string) {
	if ep.coord == nil {
		// Not reachable in production (the coordinator is always present when
		// the entry points are registered), but a nil here would panic inside
		// a request handler — and a panic in an HTTP handler takes the process
		// down. Answer instead.
		log.Printf("ERROR: %s trigger for %q: no coordinator wired", source, cw.Name)
		writeJSONError(w, http.StatusServiceUnavailable, "coordinator is not available")
		return
	}
	inputs["trigger_source"] = source

	if err := submitRegistryWorkflow(r.Context(), ep.registry, ep.coord, cw, inputs); err != nil {
		log.Printf("ERROR: %s trigger for %q failed: %v", source, cw.Name, err)
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("submit: %v", err))
		return
	}

	// 202, not 200: the workflow has been accepted, not finished. Claiming
	// success would be a lie the caller cannot detect until much later.
	writeJSON(w, http.StatusAccepted, map[string]any{
		"status":   "accepted",
		"workflow": cw.Name,
		"source":   source,
	})
}

// declaresTrigger reports whether a workflow declared a trigger of this type.
func declaresTrigger(cw *registry.CompiledWorkflow, kind string) bool {
	for _, tr := range cw.Triggers {
		if tr.Type == kind {
			return true
		}
	}
	return false
}

// writeJSON writes a JSON response.
func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		log.Printf("WARN: write response: %v", err)
	}
}

// writeJSONError writes a JSON error. The message is deliberately coarse: an
// entry point that explains itself in detail also explains itself to whoever is
// probing it.
func writeJSONError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": message})
}

// triggerDedup remembers recently seen signatures.
//
// It is intentionally small and in-memory: its job is to stop a replayed
// request within the freshness window (minutes), not to be a durable record.
// The window bounds the memory it can use.
type triggerDedup struct {
	seen map[string]time.Time
	ttl  time.Duration
}

func newTriggerDedup(ttl time.Duration) *triggerDedup {
	return &triggerDedup{seen: map[string]time.Time{}, ttl: ttl}
}

// firstSeen records a signature and reports whether it is new.
func (d *triggerDedup) firstSeen(signature string) bool {
	now := time.Now()
	for key, at := range d.seen {
		if now.Sub(at) > d.ttl {
			delete(d.seen, key)
		}
	}
	if _, ok := d.seen[signature]; ok {
		return false
	}
	d.seen[signature] = now
	return true
}

// triggerDedupTTL resolves FORGE_TRIGGER_DEDUP_TTL, the same variable the poll
// path uses — one knob for "how long do we remember that we handled this".
func triggerDedupTTL() time.Duration {
	raw := strings.TrimSpace(os.Getenv(envTriggerDedupTTL))
	if raw == "" {
		return defaultDedupTTL
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		log.Printf("WARN: unknown %s %q (want a duration like 72h); using the default", envTriggerDedupTTL, raw)
		return defaultDedupTTL
	}
	return d
}
