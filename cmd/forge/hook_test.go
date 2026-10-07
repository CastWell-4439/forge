package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The signing rule is duplicated on the two sides (the server package must not
// import a CLI package), so this known-answer test is what keeps them equal.
// Without it, a change to one side would show up as "signature mismatch" — a
// message that blames the secret for what is really a protocol disagreement.
func TestSignHookRequestKnownAnswer(t *testing.T) {
	// Known answer computed independently: HMAC-SHA256(key="secret",
	// msg="1700000000" + `{"a":1}`).
	mac := hmac.New(sha256.New, []byte("secret"))
	mac.Write([]byte("1700000000"))
	mac.Write([]byte(`{"a":1}`))
	want := hex.EncodeToString(mac.Sum(nil))

	got := signHookRequest([]byte("secret"), "1700000000", []byte(`{"a":1}`))
	assert.Equal(t, want, got)
	assert.Len(t, got, 64)

	// The timestamp is inside the signed material: changing it changes the
	// signature, which is what makes the freshness window enforceable.
	assert.NotEqual(t, got, signHookRequest([]byte("secret"), "1700000001", []byte(`{"a":1}`)))
	assert.NotEqual(t, got, signHookRequest([]byte("secret"), "1700000000", []byte(`{"a":2}`)))
	assert.NotEqual(t, got, signHookRequest([]byte("other"), "1700000000", []byte(`{"a":1}`)))
}

// The command sends a signed request to the right endpoint and reports the
// server's answer.
func TestHookSendSignsAndPosts(t *testing.T) {
	var (
		gotPath string
		gotBody []byte
		gotSig  string
		gotTS   string
		gotCT   string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotSig = r.Header.Get("X-Forge-Signature")
		gotTS = r.Header.Get("X-Forge-Timestamp")
		gotCT = r.Header.Get("Content-Type")
		body := new(bytes.Buffer)
		_, _ = body.ReadFrom(r.Body)
		gotBody = body.Bytes()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"status":"accepted"}`))
	}))
	defer srv.Close()

	t.Setenv(envWebhookSecretCLI, "s3cret")

	var stdout, stderr bytes.Buffer
	code := run([]string{"hook", "send", "--workflow", "deploy", "--data", `{"env":"prod"}`, "--endpoint", srv.URL}, &stdout, &stderr)

	require.Equal(t, 0, code, "stderr: %s", stderr.String())
	assert.Equal(t, "/api/v1/hooks/deploy", gotPath)
	assert.JSONEq(t, `{"env":"prod"}`, string(gotBody))
	assert.Equal(t, "application/json", gotCT)
	assert.NotEmpty(t, gotTS, "the timestamp header is sent")
	assert.Equal(t, signHookRequest([]byte("s3cret"), gotTS, gotBody), gotSig,
		"the signature matches what the server will compute")
	assert.Contains(t, stdout.String(), "202")
	assert.Contains(t, stdout.String(), "accepted")
}

// --manual switches to the run endpoint: two different acts, two paths.
func TestHookSendManualUsesRunEndpoint(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	t.Setenv(envWebhookSecretCLI, "s3cret")
	var stdout, stderr bytes.Buffer
	code := run([]string{"hook", "send", "--workflow", "nightly", "--manual", "--endpoint", srv.URL}, &stdout, &stderr)

	require.Equal(t, 0, code, "stderr: %s", stderr.String())
	assert.Equal(t, "/api/v1/workflows/nightly/run", gotPath)
}

// A missing secret is refused locally, naming the variable — better than a
// request the server answers with an opaque 401.
func TestHookSendRequiresSecret(t *testing.T) {
	t.Setenv(envWebhookSecretCLI, "")

	var stdout, stderr bytes.Buffer
	code := run([]string{"hook", "send", "--workflow", "deploy"}, &stdout, &stderr)

	assert.Equal(t, 1, code)
	assert.Contains(t, stderr.String(), envWebhookSecretCLI)
	assert.Empty(t, stdout.String())
}

// A missing --workflow is refused with usage rather than sending to /hooks/.
func TestHookSendRequiresWorkflow(t *testing.T) {
	t.Setenv(envWebhookSecretCLI, "s3cret")

	var stdout, stderr bytes.Buffer
	code := run([]string{"hook", "send"}, &stdout, &stderr)

	assert.Equal(t, 1, code)
	assert.Contains(t, stderr.String(), "--workflow is required")
}

// An unknown hook subcommand is refused with usage, not silently treated as a
// workflow name.
func TestHookUnknownSubcommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"hook", "fire"}, &stdout, &stderr)
	assert.Equal(t, 1, code)
	assert.Contains(t, stderr.String(), "unknown subcommand")

	code = run([]string{"hook"}, &stdout, &stderr)
	assert.Equal(t, 1, code)
}

// --data @file reads the payload from disk.
func TestHookSendReadsDataFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "payload.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"from":"file"}`), 0o644))

	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := new(bytes.Buffer)
		_, _ = body.ReadFrom(r.Body)
		gotBody = body.Bytes()
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	t.Setenv(envWebhookSecretCLI, "s3cret")
	var stdout, stderr bytes.Buffer
	code := run([]string{"hook", "send", "--workflow", "w", "--data", "@" + path, "--endpoint", srv.URL}, &stdout, &stderr)

	require.Equal(t, 0, code, "stderr: %s", stderr.String())
	assert.JSONEq(t, `{"from":"file"}`, string(gotBody))
}

// An empty --data sends an empty object: a workflow with no parameters is the
// common case and must not require inventing one.
func TestHookSendDefaultsToEmptyObject(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := new(bytes.Buffer)
		_, _ = body.ReadFrom(r.Body)
		gotBody = body.Bytes()
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	t.Setenv(envWebhookSecretCLI, "s3cret")
	var stdout, stderr bytes.Buffer
	code := run([]string{"hook", "send", "--workflow", "w", "--endpoint", srv.URL}, &stdout, &stderr)

	require.Equal(t, 0, code, "stderr: %s", stderr.String())
	assert.JSONEq(t, `{}`, string(gotBody))
}

// A server refusal is reported and turned into a non-zero exit: a script that
// pipelines this command must see the failure.
func TestHookSendNonSuccessExitsNonZero(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"signature mismatch"}`))
	}))
	defer srv.Close()

	t.Setenv(envWebhookSecretCLI, "s3cret")
	var stdout, stderr bytes.Buffer
	code := run([]string{"hook", "send", "--workflow", "w", "--endpoint", srv.URL}, &stdout, &stderr)

	assert.Equal(t, 1, code)
	assert.Contains(t, stdout.String(), "401")
	assert.Contains(t, stdout.String(), "signature mismatch")
}

// The endpoint falls back to the env var, then to the loopback default — which
// matches the server's own default bind address.
func TestHookEndpointResolution(t *testing.T) {
	t.Setenv(envHookEndpointCLI, "")
	assert.Equal(t, defaultHookEndpoint, defaultHookEndpoint, "sanity")
	assert.True(t, strings.HasPrefix(defaultHookEndpoint, "http://127.0.0.1"),
		"the default points at the server's default (loopback) bind address")
}

// readHookData is small but has three distinct behaviours worth pinning.
func TestReadHookData(t *testing.T) {
	body, err := readHookData("")
	require.NoError(t, err)
	assert.JSONEq(t, `{}`, string(body))

	body, err = readHookData(`{"x":1}`)
	require.NoError(t, err)
	assert.JSONEq(t, `{"x":1}`, string(body))

	dir := t.TempDir()
	path := filepath.Join(dir, "p.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"y":2}`), 0o644))
	body, err = readHookData("@" + path)
	require.NoError(t, err)
	assert.JSONEq(t, `{"y":2}`, string(body))

	_, err = readHookData("@")
	assert.Error(t, err, "a bare @ is not a path")

	_, err = readHookData("@" + filepath.Join(dir, "missing.json"))
	assert.Error(t, err)
}

// The help text lists the command and its env vars, so `forge help` stays a
// usable entry point.
func TestUsageMentionsHook(t *testing.T) {
	var buf bytes.Buffer
	usage(&buf)
	assert.Contains(t, buf.String(), "hook send")
}

// hookUsage documents the flags and the secret requirement.
func TestHookUsageDocumentsRequirements(t *testing.T) {
	var buf bytes.Buffer
	hookUsage(&buf)
	out := buf.String()
	assert.Contains(t, out, "--workflow")
	assert.Contains(t, out, "--data")
	assert.Contains(t, out, "--manual")
	assert.Contains(t, out, envWebhookSecretCLI)
}
