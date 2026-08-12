package graphrank

import (
	"crypto/sha256"
	"fmt"
	"strings"
)

// DeterministicUUID derives a stable, RFC-4122-shaped (version 5-ish, via a
// keyed SHA-256 digest rather than the standard's namespace+name SHA-1) UUID
// from parts. Shared by every backend so result identifiers (subject
// receipts, relationship paths, driver judgments, ...) are derived
// identically regardless of which graph backend produced them. Ported
// unchanged from zepgraph.deterministicUUID.
func DeterministicUUID(parts ...string) string {
	digest := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	bytes := digest[:16]
	bytes[6] = (bytes[6] & 0x0f) | 0x50
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		bytes[0:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:16])
}

// NormalizeRelation upper-cases and safely names a relationship type for use
// as a stable, attribute-safe relation identifier. Ported unchanged from
// zepgraph.normalizeRelation.
func NormalizeRelation(value string) string {
	value = strings.ToUpper(SafeAttributeName(value))
	if value == "" {
		return "RELATES_TO"
	}
	return value
}

// SafeAttributeName lower-cases value and replaces every character outside
// [a-z0-9_] with '_', bounded to 64 runes. Ported unchanged from
// zepgraph.safeAttributeName.
func SafeAttributeName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			builder.WriteRune(r)
		} else {
			builder.WriteByte('_')
		}
		if builder.Len() >= 64 {
			break
		}
	}
	if builder.Len() == 0 {
		return "value"
	}
	return builder.String()
}
