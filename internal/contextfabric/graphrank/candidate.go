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
	// CHAOS-3778 / AC-3778-6: record the retrieval mechanism the adapter
	// declared. An exact label/name match is recorded as MatchExact IN
	// ADDITION to (not instead of) the adapter's own mechanism, because both
	// statements are true and both are useful to a reader: the full-text index
	// is what surfaced this node, and the term is what matched its label
	// exactly. Recording both cannot demote the candidate -- an exact match
	// carries confidence 1, and CorroboratedConfidence returns 1 unchanged for
	// any base of 1 precisely so that being found a second way never costs an
	// exact match its certainty.
	mechanisms := MergeMechanisms([]contextfabric.MatchMechanism{node.Mechanism})
	if matched {
		mechanisms = MergeMechanisms(mechanisms, []contextfabric.MatchMechanism{contextfabric.MatchExact})
	}
	return contextfabric.SubjectCandidate{
		ReceiptID: DeterministicUUID("context-fabric-subject-receipt", node.UUID, strings.ToLower(term)),
		Subject:   subject, State: contextfabric.ResolutionProposed,
		MatchedTerms: []string{term}, MatchReasons: []string{reason}, Confidence: confidence,
		EvidenceRefIDs: EvidenceRefs(node.Attributes), MatchMechanisms: mechanisms,
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
