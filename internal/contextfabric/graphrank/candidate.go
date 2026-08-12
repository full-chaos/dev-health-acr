package graphrank

import (
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// NodeCandidate converts one search/lookup result node into a
// SubjectCandidate, or reports false if the node is unauthorized, does not
// describe a valid canonical subject, or (per isInternal) is one of the
// backend's own bookkeeping nodes. Ported from zepgraph.nodeCandidate;
// isInternal externalizes the one backend-specific concern (zepgraph's
// anchor/marker nodes forced onto Zep's addressing scheme -- a backend that
// has no such nodes, e.g. falkorgraph, passes a predicate that always
// returns false).
func NodeCandidate(principal storage.Principal, scope contextfabric.RequestedScope, term string, node CandidateNode, isInternal func(contextfabric.SubjectRef) bool) (contextfabric.SubjectCandidate, bool) {
	if !AuthorizedAttributes(principal, scope, node.Attributes) {
		return contextfabric.SubjectCandidate{}, false
	}
	subject, ok := NodeSubject(node)
	if !ok || isInternal(subject) {
		return contextfabric.SubjectCandidate{}, false
	}
	confidence := ResultConfidence(node.Relevance, node.Score)
	matched := strings.EqualFold(strings.TrimSpace(term), node.Name) || strings.EqualFold(strings.TrimSpace(term), subject.Label)
	if matched {
		confidence = 1
	}
	if confidence == 0 {
		confidence = 0.5
	}
	reason := "Hybrid graph search matched the subject label or indexed context."
	if matched {
		reason = "Exact canonical subject label match."
	}
	return contextfabric.SubjectCandidate{
		ReceiptID: DeterministicUUID("context-fabric-subject-receipt", node.UUID, strings.ToLower(term)),
		Subject:   subject, State: contextfabric.ResolutionProposed,
		MatchedTerms: []string{term}, MatchReasons: []string{reason}, Confidence: confidence,
		EvidenceRefIDs: EvidenceRefs(node.Attributes),
	}, true
}

// SubjectTerms collects the distinct, order-preserving, case-insensitively
// deduped search terms a ResolveSubjects call should try: every interpreted
// subject term plus every requested-scope subject hint's label. Ported
// unchanged from zepgraph.subjectTerms.
func SubjectTerms(request contextfabric.InvestigationRequest, interpreted contextfabric.InterpretedQuestion) []string {
	values := append([]string(nil), interpreted.SubjectTerms...)
	for _, hint := range request.RequestedScope.SubjectHints {
		if strings.TrimSpace(hint.Label) != "" {
			values = append(values, hint.Label)
		}
	}
	seen := make(map[string]struct{})
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	return result
}
