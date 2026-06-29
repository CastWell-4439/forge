package failure

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"

	"github.com/castwell/forge/internal/forgex/model"
)

var (
	// reUUID matches canonical 8-4-4-4-12 UUIDs.
	reUUID = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	// reHexID matches long hex/id blobs (project ids, hashes, etc.).
	reHexID = regexp.MustCompile(`\b[0-9a-f]{8,}\b`)
	// reNumber matches standalone digit runs.
	reNumber = regexp.MustCompile(`\d+`)
	// reSpace collapses any whitespace run into a single space.
	reSpace = regexp.MustCompile(`\s+`)
)

// Fingerprint produces a stable short hash for an ErrorEnvelope based on its
// source, operation, category and a normalized form of its message. Errors that
// differ only by ids, UUIDs or numbers collapse to the same fingerprint.
func Fingerprint(envelope model.ErrorEnvelope) string {
	parts := []string{
		strings.ToLower(strings.TrimSpace(envelope.Source)),
		strings.ToLower(strings.TrimSpace(envelope.Operation)),
		strings.ToLower(strings.TrimSpace(envelope.Category)),
		normalizeMessage(envelope.Message),
	}
	canonical := strings.Join(parts, "|")
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])[:16]
}

// normalizeMessage lowercases the message and replaces volatile tokens (UUIDs,
// long hex/ids, numbers) with stable placeholders, then compresses whitespace.
func normalizeMessage(message string) string {
	m := strings.ToLower(message)
	m = reUUID.ReplaceAllString(m, "<uuid>")
	m = reHexID.ReplaceAllString(m, "<id>")
	m = reNumber.ReplaceAllString(m, "<num>")
	m = reSpace.ReplaceAllString(m, " ")
	return strings.TrimSpace(m)
}
