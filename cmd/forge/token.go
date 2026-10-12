package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/castwell/forge/internal/grpcauth"
)

// The token utilities.
//
// They exist because the recommended way to configure a token is a digest, and
// computing one by hand is the kind of friction that gets skipped. A security
// step that is more awkward than the alternative will be replaced by the
// alternative — that is not a prediction, it is why the identity work took this
// long to be usable.
//
// So: one command that produces exactly the string FORGE_GRPC_TOKENS expects, in
// both forms, with the expiry syntax included.

// tokenUsage explains the command.
func tokenUsage(w io.Writer) {
	fmt.Fprint(w, `forge token — create and hash per-principal credentials

Usage:
  forge token hash [--generate] [--expires <RFC3339>] [<token>]
  forge token verify <token> --tokens <list>

  hash      Print the principal:... entry for a token. With --generate, a fresh
            random token is created and printed alongside its entry.
  verify    Check a token against a token list and report which principal it
            belongs to, so a deployment can be tested before it is relied on.

The server accepts either form:
  alice:plain-token
  alice:sha256:<64 hex characters>

The digest form is preferred: the server then holds only a digest, so reading its
configuration does not yield a usable credential. Add an expiry to make a token
die on its own:
  alice:sha256:<hex>:expires=2026-01-01T00:00:00Z

Exit status is 0 on success, 1 on failure.
`)
}

// runToken dispatches the token subcommands.
func runToken(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		tokenUsage(stderr)
		return 1
	}
	switch args[0] {
	case "hash":
		return runTokenHash(args[1:], stdout, stderr)
	case "verify":
		return runTokenVerify(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "forge token: unknown subcommand %q (want hash or verify)\n\n", args[0])
		tokenUsage(stderr)
		return 1
	}
}

// runTokenHash prints the config entry for a token.
func runTokenHash(args []string, stdout, stderr io.Writer) int {
	var token string
	var generate bool
	var expires string

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--generate":
			generate = true
		case "--expires":
			if i+1 >= len(args) {
				fmt.Fprintln(stderr, "forge token hash: --expires needs a value")
				return 1
			}
			i++
			expires = args[i]
		default:
			if strings.HasPrefix(args[i], "-") {
				fmt.Fprintf(stderr, "forge token hash: unknown flag %q\n", args[i])
				return 1
			}
			if token != "" {
				fmt.Fprintln(stderr, "forge token hash: at most one token")
				return 1
			}
			token = args[i]
		}
	}

	if generate && token != "" {
		fmt.Fprintln(stderr, "forge token hash: --generate and an explicit token are mutually exclusive")
		return 1
	}
	if generate {
		generated, err := generateToken()
		if err != nil {
			fmt.Fprintf(stderr, "forge token hash: %v\n", err)
			return 1
		}
		token = generated
	}
	if token == "" {
		fmt.Fprintln(stderr, "forge token hash: a token is required (or use --generate)")
		return 1
	}

	// The expiry is validated here as well as by the server, so a typo is
	// reported by the command that produced the line rather than by a service
	// that will not start.
	if expires != "" {
		if _, err := time.Parse(time.RFC3339, expires); err != nil {
			fmt.Fprintf(stderr, "forge token hash: --expires must be RFC 3339 (e.g. 2026-01-01T00:00:00Z): %v\n", err)
			return 1
		}
	}

	sum := sha256.Sum256([]byte(token))

	if generate {
		// A generated token is shown because it cannot be recovered afterwards —
		// only its digest is stored.
		fmt.Fprintf(stdout, "token:  %s\n", token)
	}
	fmt.Fprintf(stdout, "digest: %s%s\n", digestPrefix, hex.EncodeToString(sum[:]))
	fmt.Fprintf(stdout, "entry:  %s\n", buildEntry(expires))
	return 0
}

// digestPrefix mirrors the server's marker for a stored digest.
const digestPrefix = "sha256:"

// buildEntry renders the FORGE_GRPC_TOKENS entry for a digest.
//
// The principal is left as a placeholder: this command does not know who the
// token is for, and inventing a name would produce a line that looks ready to use
// while attributing the credential to the wrong person.
func buildEntry(expires string) string {
	entry := "<principal>:" + digestPrefix + "<hex from above>"
	if expires != "" {
		entry += ":expires=" + expires
	}
	return entry
}

// generateToken returns a fresh random token.
//
// 32 bytes from crypto/rand, base64url encoded without padding: 256 bits is far
// beyond guessing, and the encoding avoids characters that need quoting in a
// shell or a YAML file — a token that has to be escaped is one that gets pasted
// wrong.
func generateToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// runTokenVerify checks a token against a list.
func runTokenVerify(args []string, stdout, stderr io.Writer) int {
	var token string
	var list string

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--tokens":
			if i+1 >= len(args) {
				fmt.Fprintln(stderr, "forge token verify: --tokens needs a value")
				return 1
			}
			i++
			list = args[i]
		default:
			if strings.HasPrefix(args[i], "-") {
				fmt.Fprintf(stderr, "forge token verify: unknown flag %q\n", args[i])
				return 1
			}
			if token != "" {
				fmt.Fprintln(stderr, "forge token verify: at most one token")
				return 1
			}
			token = args[i]
		}
	}
	if token == "" || list == "" {
		fmt.Fprintln(stderr, "forge token verify: a token and --tokens <list> are required")
		return 1
	}

	set := grpcauth.ParseTokens(list)
	if err := set.Error(); err != nil {
		// A malformed list is reported as such rather than as "the token did not
		// match": the two have different fixes, and saying "no match" would send
		// an operator looking at the wrong thing.
		fmt.Fprintf(stderr, "forge token verify: the list is malformed: %v\n", err)
		return 1
	}

	actor, ok := set.Resolve(token)
	if !ok {
		// Expired and unknown are reported the same way, matching the server: the
		// distinction is about the credential and not about the request, and this
		// command is a stand-in for the server rather than a debugging channel
		// that reveals more than the server does.
		fmt.Fprintln(stdout, "no match: the token is unknown or expired")
		return 1
	}

	fmt.Fprintf(stdout, "principal: %s\n", actor.ID())
	fmt.Fprintf(stdout, "verified:  %t (by %s)\n", actor.Verified(), actor.Verifier())
	return 0
}
