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
//
// allowExactMatch gates whether term is even ELIGIBLE to trigger the
// exact-label-match promotion below (codex round-2 P1). It exists because
// term is not always caller-typed search input: CHAOS-3838's question-level
// pass calls this with a synthetic, bounded PROVENANCE marker (see
// resolve.go's questionProvenanceMarker) standing in for the raw question,
// purely so MatchedTerms has something legible to display -- that marker
// must never be allowed to WIN an exact-match comparison against a real
// subject's label just because some subject happens to be named the same
// literal string as the marker. A caller passing false gets confidence and
// mechanism derived ONLY from node.Relevance/node.Score/node.Mechanism,
// never from any string comparison against term, regardless of what term
// happens to equal. Every pre-existing caller (the exact-hint path, the
// per-term hybrid-search path, and traversal reached from either) passes
// true, preserving byte-identical behavior for genuine caller-typed terms.
func NodeCandidate(principal storage.Principal, scope contextfabric.RequestedScope, term string, node CandidateNode, isInternal func(contextfabric.SubjectRef) bool, allowExactMatch bool) (contextfabric.SubjectCandidate, bool) {
	if !AuthorizedAttributes(principal, scope, node.Attributes) {
		return contextfabric.SubjectCandidate{}, false
	}
	subject, ok := NodeSubject(node)
	if !ok || isInternal(subject) {
		return contextfabric.SubjectCandidate{}, false
	}
	confidence := ResultConfidence(node.Relevance, node.Score)
	trimmedTerm := strings.TrimSpace(term)
	matched := allowExactMatch && (strings.EqualFold(trimmedTerm, node.Name) || strings.EqualFold(trimmedTerm, subject.Label))

	// aliasMatched/providerMatched (CHAOS-3884): the SAME allowExactMatch
	// discipline as the label check above (a synthetic, non-caller-typed
	// term -- CHAOS-3838's questionProvenanceMarker -- must never win this
	// either), plus one more gate: isAliasLookupScopedKind. Detection and
	// MECHANISM TAGGING run for every scoped kind (repository, project,
	// team, work_item) regardless of eligibility -- HIGH-5's fix -- so a
	// team/work_item candidate found this way is still discoverable and
	// COUNTABLE toward collision detection even though it can never itself
	// win the identity fast path (see the confidence gate below, and
	// resolution.go's identityCollision). Mutually exclusive with the
	// label check (only tried when !matched): an exact label match keeps
	// unconditional priority, unchanged from CHAOS-3810.
	aliasMatched := false
	providerMatched := false
	if allowExactMatch && !matched && isAliasLookupScopedKind(subject.Kind) {
		for _, alias := range AliasAttributes(node.Attributes) {
			if strings.EqualFold(trimmedTerm, alias) {
				aliasMatched = true
				break
			}
		}
		if !aliasMatched {
			for _, alias := range ProviderAliasAttributes(node.Attributes) {
				if strings.EqualFold(trimmedTerm, alias) {
					providerMatched = true
					break
				}
			}
		}
	}
	// identityTrusted (CHAOS-3884 spot-check item 2, "provenance made
	// structural"): the confidence=1 identity bump is gated on
	// node.FromKeyedIdentityLookup -- an explicit, adapter-declared
	// structural marker only a genuinely complete keyed identity read may
	// set -- AND isAliasIdentityEligibleKind, never on the mechanism VALUE
	// or on allowExactMatch alone. An ordinary Search()-sourced node that
	// happens to alias-match (aliasMatched/providerMatched true above) is
	// still tagged and counted, but keeps its ordinary, non-bumped
	// confidence: FromKeyedIdentityLookup is false for it by construction,
	// so it can never manufacture the completeness guarantee this bump
	// asserts on its own.
	identityTrusted := node.FromKeyedIdentityLookup && isAliasIdentityEligibleKind(subject.Kind)
	if matched || ((aliasMatched || providerMatched) && identityTrusted) {
		confidence = 1
	}
	if confidence == 0 {
		confidence = 0.5
	}
	reason := "Hybrid graph search matched the subject label or indexed context."
	switch {
	case matched:
		reason = "Exact canonical subject label match."
	case aliasMatched:
		reason = "Repository/project alias matched."
	case providerMatched:
		reason = "Provider-qualified identifier matched."
	}
	// CHAOS-3778 / AC-3778-6: record the retrieval mechanism the adapter
	// declared. An exact label/name match is recorded as MatchExact IN
	// ADDITION to (not instead of) the adapter's own mechanism, because both
	// statements are true and both are useful to a reader: the full-text index
	// is what surfaced this node, and the term is what matched its label
	// exactly. Recording both cannot demote the candidate -- an exact match
	// carries confidence 1, and CorroboratedConfidence returns 1 unchanged for
	// any base of 1 precisely so that being found a second way never costs an
	// exact match its certainty. CHAOS-3884: MatchAlias/MatchProviderKey
	// follow the identical "in addition to" discipline.
	mechanisms := MergeMechanisms([]contextfabric.MatchMechanism{node.Mechanism})
	switch {
	case matched:
		mechanisms = MergeMechanisms(mechanisms, []contextfabric.MatchMechanism{contextfabric.MatchExact})
	case aliasMatched:
		mechanisms = MergeMechanisms(mechanisms, []contextfabric.MatchMechanism{contextfabric.MatchAlias})
	case providerMatched:
		mechanisms = MergeMechanisms(mechanisms, []contextfabric.MatchMechanism{contextfabric.MatchProviderKey})
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
