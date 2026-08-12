package zepgraph

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

const scopeSeparator = "|"

// scopeDeniedSentinel is what encodeScope returns for a non-empty input that
// produced zero usable encoded values (every value was empty after
// trimming, or contained the separator character). It is deliberately never
// "*": encoding nothing from a non-empty list must fail closed -- authorize
// or return nothing -- rather than widen to "matches everything", which is
// what a bare-separator encoding ("|") previously collapsed to. scopeContains
// and decodeScope both special-case this value so it can never match or
// decode to anything.
const scopeDeniedSentinel = "\x00scope-encoding-rejected\x00"

func graphID(prefix, orgID string) string {
	digest := sha256.Sum256([]byte("context-fabric-graph\x00" + orgID))
	return strings.TrimSpace(prefix) + "-" + hex.EncodeToString(digest[:16])
}

func deterministicUUID(parts ...string) string {
	digest := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	bytes := digest[:16]
	bytes[6] = (bytes[6] & 0x0f) | 0x50
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		bytes[0:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:16])
}

func nodeUUID(orgID string, subject contextfabric.SubjectRef) string {
	return deterministicUUID("context-fabric-node", orgID, string(subject.Kind), subject.CanonicalID)
}

func contentUUID(orgID, kind, canonicalID string) string {
	return deterministicUUID("context-fabric-content", orgID, kind, canonicalID)
}

func relationshipUUID(orgID, relationshipID string) string {
	return deterministicUUID("context-fabric-edge", orgID, relationshipID)
}

func organizationRoot(orgID string) contextfabric.SubjectRef {
	return contextfabric.SubjectRef{
		Kind: contextfabric.SubjectOrganization, CanonicalID: "organization-root", Label: "Organization",
	}
}

func markerSubject(source string) contextfabric.SubjectRef {
	return contextfabric.SubjectRef{
		Kind: contextfabric.SubjectMetric, CanonicalID: "projection-watermark:" + source, Label: "Projection watermark " + source,
	}
}

func encodeScope(values []string) string {
	if len(values) == 0 {
		return "*"
	}
	copyValues := append([]string(nil), values...)
	sort.Strings(copyValues)
	var builder strings.Builder
	builder.WriteString(scopeSeparator)
	for _, value := range copyValues {
		value = strings.TrimSpace(value)
		if value == "" || strings.Contains(value, scopeSeparator) {
			// A single unusable value must not silently narrow the encoded
			// scope to whatever values did survive -- that reads later as
			// "this list was always shorter", not "an input value was
			// rejected". Fail closed for the whole list instead.
			return scopeDeniedSentinel
		}
		builder.WriteString(value)
		builder.WriteString(scopeSeparator)
	}
	return builder.String()
}

func scopeContains(encoded, value string) bool {
	if encoded == scopeDeniedSentinel {
		return false
	}
	if encoded == "*" {
		return true
	}
	return strings.Contains(encoded, scopeSeparator+value+scopeSeparator)
}

func subjectKey(subject contextfabric.SubjectRef) string {
	return string(subject.Kind) + "\x00" + subject.CanonicalID
}
