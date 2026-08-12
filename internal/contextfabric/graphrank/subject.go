package graphrank

import (
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// NodeSubject reconstructs the canonical SubjectRef a node's projected
// attributes describe. Ported unchanged from zepgraph.nodeSubject.
func NodeSubject(node CandidateNode) (contextfabric.SubjectRef, bool) {
	kind := contextfabric.SubjectKind(StringAttribute(node.Attributes, "subject_kind"))
	canonicalID := strings.TrimSpace(StringAttribute(node.Attributes, "canonical_id"))
	label := strings.TrimSpace(StringAttribute(node.Attributes, "label"))
	if label == "" {
		label = strings.TrimSpace(node.Name)
	}
	subject := contextfabric.SubjectRef{Kind: kind, CanonicalID: canonicalID, Label: label}
	if err := subject.Validate(); err != nil {
		return contextfabric.SubjectRef{}, false
	}
	return subject, true
}

// EvidenceRefs reads a node/edge's "evidence_refs" attribute, which every
// backend stores as a plain []string (never the authorization wildcard
// convention -- evidence refs have no "*" meaning).
func EvidenceRefs(attributes map[string]interface{}) []string {
	if refs, ok := attributes["evidence_refs"].([]string); ok {
		return refs
	}
	return nil
}

// IsObservationSubjectKind reports whether kind describes an observation
// about a canonical entity (a document or episode) rather than a
// first-class subject in its own right.
func IsObservationSubjectKind(kind contextfabric.SubjectKind) bool {
	return kind == contextfabric.SubjectDocument || kind == contextfabric.SubjectEpisode
}

// IsObservationAttributionRelation reports whether normalizedName (already
// run through NormalizeRelation) is one of the specific relation kinds a
// backend's content/episode projection uses to attach a document or episode
// to the canonical subject it is authoritatively about ("DOCUMENTED_BY",
// "HAS_EPISODE"). Traversal must not follow any other edge that happens to
// point at an observation node -- see zepgraph.isObservationAttributionRelation
// for the original rationale (a generic MENTIONS/REFERENCES relationship
// is a much weaker, not-necessarily-singular association).
func IsObservationAttributionRelation(normalizedName string) bool {
	switch normalizedName {
	case "DOCUMENTED_BY", "HAS_EPISODE":
		return true
	default:
		return false
	}
}
