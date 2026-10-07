package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// forge hook send — sign and deliver a webhook/manual request.
//
// The server side (internal/serve/coordinator/webhook.go) authenticates with
// HMAC-SHA256 over timestamp+body. That is easy to describe and tedious to do
// by hand: computing a hex digest, formatting a unix timestamp and setting two
// headers is exactly the kind of friction that makes an operator paste a secret
// into a shell history file or, worse, disable the check.
//
// This command exists so the signature is never computed by hand. It reuses the
// SAME signing rule as the server (see signHookRequest): one implementation on
// each side would be a chance for them to disagree, and a disagreement here
// looks like "the secret is wrong" when it is really "the two sides signed
// different bytes".
//
// Env:
//
//	FORGE_WEBHOOK_SECRET   required; the shared secret
//	FORGE_HOOK_ENDPOINT    optional; default http://127.0.0.1:9090
const (
	envWebhookSecretCLI = "FORGE_WEBHOOK_SECRET"
	envHookEndpointCLI  = "FORGE_HOOK_ENDPOINT"

	defaultHookEndpoint = "http://127.0.0.1:9090"
)

func hookUsage(w io.Writer) {
	fmt.Fprint(w, `forge hook send — trigger a workflow over the HTTP entry point

Usage:
  forge hook send --workflow NAME [--data JSON|@FILE] [--endpoint URL]
  forge hook send --workflow NAME --manual   [--data JSON|@FILE]

Flags:
  --workflow NAME   workflow to trigger (required)
  --data VALUE      JSON object of inputs, or @path to a file (default {})
  --manual          use the manual-run endpoint instead of the webhook one
  --endpoint URL    coordinator base URL (default $FORGE_HOOK_ENDPOINT or
                    http://127.0.0.1:9090)

Environment:
  FORGE_WEBHOOK_SECRET   required — the shared secret the server verifies
  FORGE_HOOK_ENDPOINT    optional — default endpoint

Examples:
  forge hook send --workflow deploy --data '{"env":"prod"}'
  forge hook send --workflow nightly --manual
  forge hook send --workflow deploy --data @payload.json --endpoint http://ci:9090
`)
}

// runHookSend parses flags, signs the body and reports the server's answer.
func runHookSend(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("hook send", flag.ContinueOnError)
	fs.SetOutput(stderr)
	workflow := fs.String("workflow", "", "workflow to trigger (required)")
	data := fs.String("data", "", "JSON object of inputs, or @path to a file")
	manual := fs.Bool("manual", false, "use the manual-run endpoint")
	endpoint := fs.String("endpoint", "", "coordinator base URL")

	if err := fs.Parse(args); err != nil {
		return 1
	}
	if strings.TrimSpace(*workflow) == "" {
		fmt.Fprintln(stderr, "forge hook send: --workflow is required")
		hookUsage(stderr)
		return 1
	}

	secret := strings.TrimSpace(os.Getenv(envWebhookSecretCLI))
	if secret == "" {
		// Refusing here rather than sending an unsigned request: the server
		// would answer 401 anyway, and this message names the variable.
		fmt.Fprintf(stderr, "forge hook send: %s is not set (the server verifies this signature)\n", envWebhookSecretCLI)
		return 1
	}

	body, err := readHookData(*data)
	if err != nil {
		fmt.Fprintf(stderr, "forge hook send: %v\n", err)
		return 1
	}

	base := strings.TrimSpace(*endpoint)
	if base == "" {
		base = strings.TrimSpace(os.Getenv(envHookEndpointCLI))
	}
	if base == "" {
		base = defaultHookEndpoint
	}

	path := "/api/v1/hooks/" + *workflow
	if *manual {
		path = "/api/v1/workflows/" + *workflow + "/run"
	}
	url := strings.TrimRight(base, "/") + path

	status, respBody, err := sendHook(url, secret, body)
	if err != nil {
		fmt.Fprintf(stderr, "forge hook send: %v\n", err)
		return 1
	}

	// The server's answer is the useful part (202 accepted, or the reason it
	// refused), so it is printed as-is rather than summarised.
	fmt.Fprintf(stdout, "%d %s\n", status, strings.TrimSpace(string(respBody)))
	if status < 200 || status >= 300 {
		return 1
	}
	return 0
}

// readHookData resolves the --data value: empty means an empty object, a value
// starting with @ is read from that file, anything else is the JSON itself.
func readHookData(value string) ([]byte, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return []byte("{}"), nil
	}
	if strings.HasPrefix(trimmed, "@") {
		path := strings.TrimSpace(strings.TrimPrefix(trimmed, "@"))
		if path == "" {
			return nil, fmt.Errorf("--data @ requires a file path")
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		return content, nil
	}
	return []byte(trimmed), nil
}

// sendHook performs the signed POST and returns the status and body.
func sendHook(url, secret string, body []byte) (int, []byte, error) {
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	signature := signHookRequest([]byte(secret), timestamp, body)

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forge-Signature", signature)
	req.Header.Set("X-Forge-Timestamp", timestamp)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, respBody, nil
}

// signHookRequest is the client half of the server's verifySignature.
//
// It must stay byte-for-byte identical in what it signs (timestamp, then body)
// or every request fails with "signature mismatch" while looking correct. The
// two sides live in different packages (the server must not import a CLI
// package), so the rule is duplicated — and pinned by a test that checks the
// known-answer vector rather than by hoping.
func signHookRequest(secret []byte, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(timestamp))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}
