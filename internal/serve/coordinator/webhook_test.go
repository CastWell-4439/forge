package coordinator

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/castwell/forge/internal/registry"
)

// entryPointFixture builds entry points over a temp workflow directory.
func entryPointFixture(t *testing.T, secret string, workflows map[string]string) *workflowEntryPoints {
	t.Helper()
	dir := t.TempDir()
	for name, body := range workflows {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name+".yaml"), []byte(body), 0o644))
	}

	reg := registry.NewRegistry()
	require.NoError(t, reg.Load(dir))

	ep := &workflowEntryPoints{registry: reg, dedup: newTriggerDedup(time.Hour)}
	if secret != "" {
		ep.secret = []byte(secret)
	}
	return ep
}

// webhookWorkflow is a minimal workflow that declares a webhook trigger.
const webhookWorkflow = `apiVersion: forge/v1
kind: Workflow
metadata:
  name: hooked
  version: "1"
triggers:
  - type: webhook
    source: test
stages:
  - name: main
    tasks:
      - worker: shell
        action: run
        params:
          command: echo hello
`

// noTriggerWorkflow declares nothing, so it must not be reachable by hook.
const noTriggerWorkflow = `apiVersion: forge/v1
kind: Workflow
metadata:
  name: plain
  version: "1"
stages:
  - name: main
    tasks:
      - worker: shell
        action: run
        params:
          command: echo hello
`

// sign builds the headers a correct caller sends.
func sign(secret string, body []byte, at time.Time) (string, string) {
	ts := strconv.FormatInt(at.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil)), ts
}

func postWithSignature(ep *workflowEntryPoints, path string, body []byte, secret string, at time.Time) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	if secret != "" {
		sig, ts := sign(secret, body, at)
		req.Header.Set(signatureHeader, sig)
		req.Header.Set(timestampHeader, ts)
	}
	rec := httptest.NewRecorder()
	// Route the way the mux does: the /workflows/ prefix wins, including the
	// bare prefix itself (which is a malformed path the handler must refuse).
	if strings.HasPrefix(path, "/api/v1/workflows/") {
		ep.handleManualRun(rec, req)
	} else {
		ep.handleWebhook(rec, req)
	}
	return rec
}

// --- authentication ---

// A correct signature is accepted (the request reaches the submit stage).
func TestWebhookAcceptsValidSignature(t *testing.T) {
	ep := entryPointFixture(t, "s3cret", map[string]string{"hooked": webhookWorkflow})
	rec := postWithSignature(ep, "/api/v1/hooks/hooked", []byte(`{"a":1}`), "s3cret", time.Now())
	// The fixture has no coordinator, so submission fails — but the point is
	// that authentication PASSED: a 401 would mean it did not.
	assert.NotEqual(t, http.StatusUnauthorized, rec.Code, "a valid signature must authenticate")
}

// A wrong signature is refused, and the response says nothing useful.
func TestWebhookRejectsBadSignature(t *testing.T) {
	ep := entryPointFixture(t, "s3cret", map[string]string{"hooked": webhookWorkflow})
	rec := postWithSignature(ep, "/api/v1/hooks/hooked", []byte(`{"a":1}`), "wrong-secret", time.Now())
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Contains(t, rec.Body.String(), "signature mismatch")
}

// Missing headers are refused: without them there is nothing to verify.
func TestWebhookRejectsMissingHeaders(t *testing.T) {
	ep := entryPointFixture(t, "s3cret", map[string]string{"hooked": webhookWorkflow})

	for _, tc := range []struct{ name, sig, ts string }{
		{"no headers", "", ""},
		{"signature only", "abc", ""},
		{"timestamp only", "", "1700000000"},
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/hooks/hooked", bytes.NewReader([]byte("{}")))
		if tc.sig != "" {
			req.Header.Set(signatureHeader, tc.sig)
		}
		if tc.ts != "" {
			req.Header.Set(timestampHeader, tc.ts)
		}
		rec := httptest.NewRecorder()
		ep.handleWebhook(rec, req)
		assert.Equal(t, http.StatusUnauthorized, rec.Code, tc.name)
	}
}

// The timestamp is INSIDE the signed material, so a stale one cannot be
// refreshed without breaking the signature. This test proves the window is
// actually enforced.
func TestWebhookRejectsStaleTimestamp(t *testing.T) {
	ep := entryPointFixture(t, "s3cret", map[string]string{"hooked": webhookWorkflow})
	body := []byte(`{}`)

	// Signed correctly, but an hour ago.
	rec := postWithSignature(ep, "/api/v1/hooks/hooked", body, "s3cret", time.Now().Add(-time.Hour))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Contains(t, rec.Body.String(), "outside the")

	// Signed correctly and recent: accepted.
	rec = postWithSignature(ep, "/api/v1/hooks/hooked", body, "s3cret", time.Now().Add(-time.Minute))
	assert.NotEqual(t, http.StatusUnauthorized, rec.Code)
}

// A replayed request (same signature, inside the window) is refused the second
// time: a captured request must not start a second run.
func TestWebhookRejectsReplay(t *testing.T) {
	ep := entryPointFixture(t, "s3cret", map[string]string{"hooked": webhookWorkflow})
	body := []byte(`{"a":1}`)
	at := time.Now()

	first := postWithSignature(ep, "/api/v1/hooks/hooked", body, "s3cret", at)
	require.NotEqual(t, http.StatusUnauthorized, first.Code, "the first request authenticates")

	second := postWithSignature(ep, "/api/v1/hooks/hooked", body, "s3cret", at)
	assert.Equal(t, http.StatusConflict, second.Code, "the replay is refused")
	assert.Contains(t, second.Body.String(), "already accepted")
}

// --- the declaration is the allow-list ---

// A workflow without a webhook trigger is not reachable by hook, however valid
// the request — the declaration is what makes an endpoint exist.
func TestWebhookRequiresDeclaredTrigger(t *testing.T) {
	ep := entryPointFixture(t, "s3cret", map[string]string{"plain": noTriggerWorkflow})
	rec := postWithSignature(ep, "/api/v1/hooks/plain", []byte(`{}`), "s3cret", time.Now())
	assert.Equal(t, http.StatusNotFound, rec.Code, "no declared hook means no endpoint")
}

// An unknown workflow and an undeclared one answer with the SAME status and the
// same wording, differing only in the name the caller already supplied. That is
// what makes the endpoint useless for enumerating which workflows exist: the
// response tells a prober nothing it did not already know.
func TestWebhookUnknownAndUndeclaredLookTheSame(t *testing.T) {
	ep := entryPointFixture(t, "s3cret", map[string]string{"plain": noTriggerWorkflow})

	// Different bodies so the two requests do not share a signature (which
	// would make the second one a replay rather than a second probe).
	unknown := postWithSignature(ep, "/api/v1/hooks/nope", []byte(`{"probe":"unknown"}`), "s3cret", time.Now())
	undeclared := postWithSignature(ep, "/api/v1/hooks/plain", []byte(`{"probe":"undeclared"}`), "s3cret", time.Now())

	require.Equal(t, http.StatusNotFound, unknown.Code)
	assert.Equal(t, unknown.Code, undeclared.Code, "the status must not distinguish the two cases")

	// The wording is the same template, with only the requested name echoed.
	assert.Equal(t,
		strings.ReplaceAll(unknown.Body.String(), "nope", "NAME"),
		strings.ReplaceAll(undeclared.Body.String(), "plain", "NAME"),
		"the message shape is identical")
}

// --- manual run ---

// Every workflow is runnable by hand: that is what "manual" means. No trigger
// declaration is required.
func TestManualRunNeedsNoTriggerDeclaration(t *testing.T) {
	ep := entryPointFixture(t, "s3cret", map[string]string{"plain": noTriggerWorkflow})
	rec := postWithSignature(ep, "/api/v1/workflows/plain/run", []byte(`{}`), "s3cret", time.Now())
	assert.NotEqual(t, http.StatusNotFound, rec.Code, "manual run does not need a declared trigger")
	assert.NotEqual(t, http.StatusUnauthorized, rec.Code)
}

// The manual path is authenticated too.
func TestManualRunRequiresSignature(t *testing.T) {
	ep := entryPointFixture(t, "s3cret", map[string]string{"plain": noTriggerWorkflow})
	rec := postWithSignature(ep, "/api/v1/workflows/plain/run", []byte(`{}`), "", time.Now())
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

// A malformed manual path is refused with the expected shape, rather than being
// silently treated as a workflow named "plain/run".
func TestManualRunPathShape(t *testing.T) {
	ep := entryPointFixture(t, "s3cret", map[string]string{"plain": noTriggerWorkflow})

	for _, path := range []string{"/api/v1/workflows/", "/api/v1/workflows/plain"} {
		rec := postWithSignature(ep, path, []byte(`{}`), "s3cret", time.Now())
		assert.Equal(t, http.StatusBadRequest, rec.Code, path)
	}
}

// --- request shape ---

// Only POST: a GET must not be able to trigger anything.
func TestEntryPointsRejectNonPost(t *testing.T) {
	ep := entryPointFixture(t, "", map[string]string{"hooked": webhookWorkflow})

	for _, tc := range []struct {
		name    string
		handler func(http.ResponseWriter, *http.Request)
		path    string
	}{
		{"webhook", ep.handleWebhook, "/api/v1/hooks/hooked"},
		{"manual", ep.handleManualRun, "/api/v1/workflows/hooked/run"},
	} {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		rec := httptest.NewRecorder()
		tc.handler(rec, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code, tc.name)
	}
}

// A non-object body is refused here rather than deeper: an array has no field
// names to render into the workflow's inputs.
func TestBodyMustBeJSONObject(t *testing.T) {
	ep := entryPointFixture(t, "", map[string]string{"hooked": webhookWorkflow})

	rec := postWithSignature(ep, "/api/v1/hooks/hooked", []byte(`[1,2,3]`), "", time.Now())
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "JSON object")

	rec = postWithSignature(ep, "/api/v1/hooks/hooked", []byte(`not json`), "", time.Now())
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// An empty body is fine: a manual run with no parameters is the common case.
func TestEmptyBodyIsAccepted(t *testing.T) {
	inputs, err := decodeInputs(nil)
	require.NoError(t, err)
	assert.Empty(t, inputs)

	// The whole path accepts it too (not just the decoder).
	ep := entryPointFixture(t, "", map[string]string{"plain": noTriggerWorkflow})
	rec := postWithSignature(ep, "/api/v1/workflows/plain/run", nil, "", time.Now())
	assert.NotEqual(t, http.StatusBadRequest, rec.Code, "an empty body is not a client error")
}

// --- no-secret posture ---

// Without a secret the handlers still authenticate (they are only registered
// when the listener is loopback-bound — that decision is made in
// registerWorkflowEntryPoints, and is tested separately).
func TestNoSecretSkipsSignatureCheck(t *testing.T) {
	ep := entryPointFixture(t, "", map[string]string{"hooked": webhookWorkflow})
	rec := postWithSignature(ep, "/api/v1/hooks/hooked", []byte(`{"a":1}`), "", time.Now())
	assert.NotEqual(t, http.StatusUnauthorized, rec.Code)
}

// --- bind-address decision ---

// The loopback test is what the whole posture hangs on, so it is strict: an
// address that cannot be proven local must require a secret.
func TestIsLoopbackAddr(t *testing.T) {
	for _, tc := range []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:9090", true},
		{"localhost:9090", true},
		{"[::1]:9090", true},
		{"127.0.0.1", true},
		{":9090", false},        // all interfaces
		{"0.0.0.0:9090", false}, // all interfaces, explicitly
		{"192.168.1.10:9090", false},
		{"example.com:9090", false},
		{"", false}, // unparseable: not provably local
	} {
		assert.Equal(t, tc.want, isLoopbackAddr(tc.addr), tc.addr)
	}
}

// The default bind address is loopback, so exposing the API is a deliberate
// act rather than the starting condition.
func TestDefaultHTTPAddrIsLoopback(t *testing.T) {
	t.Setenv(envHTTPAddr, "")
	assert.True(t, isLoopbackAddr(httpAddr()), "the default listener is loopback")
	assert.Equal(t, defaultHTTPAddr, httpAddr())
}

// A routable address without a secret gets NO entry point: the safer default is
// not to listen, not to listen and refuse.
func TestRoutableWithoutSecretRegistersNothing(t *testing.T) {
	t.Setenv(envHTTPAddr, "0.0.0.0:9090")
	t.Setenv(envWebhookSecret, "")
	t.Setenv(envWorkflowsDir, t.TempDir())

	mux := http.NewServeMux()
	registerWorkflowEntryPoints(mux, nil)

	// A request to the hook path must 404: nothing is mounted.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/hooks/anything", nil))
	assert.Equal(t, http.StatusNotFound, rec.Code, "no entry point is registered")
}

// With a secret, the entry points register even on a routable address.
func TestRoutableWithSecretRegisters(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "hooked.yaml"), []byte(webhookWorkflow), 0o644))

	t.Setenv(envHTTPAddr, "0.0.0.0:9090")
	t.Setenv(envWebhookSecret, "s3cret")
	t.Setenv(envWorkflowsDir, dir)

	mux := http.NewServeMux()
	registerWorkflowEntryPoints(mux, nil)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/hooks/hooked", nil))
	// 401 (not 404) proves the handler is mounted and demanding a signature.
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

// Loopback without a secret registers (with a warning): the local case must
// stay frictionless.
func TestLoopbackWithoutSecretRegisters(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "hooked.yaml"), []byte(webhookWorkflow), 0o644))

	t.Setenv(envHTTPAddr, "127.0.0.1:9090")
	t.Setenv(envWebhookSecret, "")
	t.Setenv(envWorkflowsDir, dir)

	mux := http.NewServeMux()
	registerWorkflowEntryPoints(mux, nil)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/hooks/hooked", nil))
	assert.NotEqual(t, http.StatusNotFound, rec.Code, "the entry point is mounted on loopback")
}

// --- dedup helper ---

func TestTriggerDedupRemembersAndExpires(t *testing.T) {
	d := newTriggerDedup(50 * time.Millisecond)
	assert.True(t, d.firstSeen("sig-1"), "first sighting")
	assert.False(t, d.firstSeen("sig-1"), "second sighting is a replay")
	assert.True(t, d.firstSeen("sig-2"))

	time.Sleep(60 * time.Millisecond)
	assert.True(t, d.firstSeen("sig-1"), "after the TTL it is forgettable again")
}

// --- signing helper ---

// signBody is the one implementation the CLI helper and the server share, so
// they cannot disagree about what is signed.
func TestSignBodyIsStable(t *testing.T) {
	a := signBody([]byte("k"), "1700000000", []byte(`{"x":1}`))
	b := signBody([]byte("k"), "1700000000", []byte(`{"x":1}`))
	assert.Equal(t, a, b)
	assert.NotEqual(t, a, signBody([]byte("k"), "1700000001", []byte(`{"x":1}`)),
		"the timestamp is part of the signed material")
	assert.NotEqual(t, a, signBody([]byte("k"), "1700000000", []byte(`{"x":2}`)))
	assert.Len(t, a, 64, "hex-encoded SHA-256")
}
