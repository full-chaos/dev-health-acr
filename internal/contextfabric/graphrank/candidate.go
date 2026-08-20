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
// tracer/requestID (CHAOS-3884, team-lead ruling 2026-08-17, guardrail 6):
// optional (nil-safe) -- when tracer is set, this function emits its OWN
// "identity_gate" ResolutionTraceEvent carrying the REAL confidence-gate
// inputs (FromKeyedIdentityLookup, eligible kind, aliasMatched,
// providerMatched, whether the gate fired, the resulting confidence) for
// every isAliasLookupScopedKind candidate -- the one place these are
// genuine local variables, never reconstructed downstream from an
// already-merged/corroborated SubjectCandidate. Scoped to
// isAliasLookupScopedKind (the SAME gate the alias/provider detection
// below already uses) so this never fires for the vast majority of
// candidates (ci_pipeline_run, pull_request, ...) the identity mechanism
// was never going to touch.
func NodeCandidate(principal storage.Principal, scope contextfabric.RequestedScope, term string, node CandidateNode, isInternal func(contextfabric.SubjectRef) bool, allowExactMatch bool, tracer ResolutionTracer, requestID string) (contextfabric.SubjectCandidate, bool) {
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
	// set -- AND isAliasIdentityEligibleKind, AND allowExactMatch (a
	// synthetic, non-caller-typed term -- CHAOS-3838's
	// questionProvenanceMarker -- must never win ANY promotion, alias-based
	// or identity-trust-based, just because it happens to equal some node's
	// stored data; the SAME discipline the matched/aliasMatched checks
	// above already carry explicitly). Never on the mechanism VALUE alone.
	//
	// Fix (team-lead ruling, 2026-08-17, live-reproduced projection-lag
	// bug): identityTrusted ALONE now gates the bump, no longer additionally
	// conjoined with (aliasMatched || providerMatched). The prior form --
	// `(aliasMatched || providerMatched) && identityTrusted` -- silently
	// RE-DERIVED the match against node.Attributes (the GRAPH's own,
	// possibly STALE "aliases"/"provider_aliases" property, last written at
	// whatever projection cycle ran before this ticket's alias logic
	// existed) even for a claimant reader.go had ALREADY existence-checked
	// and keyed-matched against FRESH ClickHouse data -- reintroducing
	// exactly the projection-lag dependency (CHAOS-3882 class) Option C's
	// keyed-lookup/existence-check split exists to eliminate. reader.go
	// sets FromKeyedIdentityLookup=true in EXACTLY one place
	// (falkorgraph/reader.go, inside the loop over graphrank.MatchIdentityRows'
	// own returned matches, AFTER a successful nodeByKindID existence
	// check) -- identityTrusted being true is ALREADY proof of a genuine
	// identity-class match against fresh source data; re-deriving it from
	// the graph's own (possibly stale) attributes only ever LOSES that
	// proof, never adds to it.
	//
	// allowExactMatch is now an EXPLICIT conjunct here rather than an
	// implicit one inherited transitively through aliasMatched/
	// providerMatched (both themselves already gated on it, inside the
	// `if allowExactMatch && !matched && ...` block above) -- dropping the
	// (aliasMatched||providerMatched) conjunct would otherwise have
	// silently dropped that inherited gate too, letting a synthetic
	// question-pass marker win via identityTrusted alone
	// (TestNodeCandidate_AllowExactMatchFalseAlsoDisablesAliasMatch pins
	// this). Structurally moot in production today (reader.go's AliasLookup
	// is only ever merged with allowExactMatch=true -- resolve.go never
	// calls it from the question pass), kept explicit anyway rather than
	// relying on that caller discipline never changing.
	//
	// aliasMatched/providerMatched are UNCHANGED everywhere else (mechanism
	// tagging below, MatchReasons, the counting scope HIGH-5 established)
	// -- this fix touches the confidence gate only.
	identityTrusted := allowExactMatch && node.FromKeyedIdentityLookup && isAliasIdentityEligibleKind(subject.Kind)
	if matched || identityTrusted {
		confidence = 1
	}
	if confidence == 0 {
		confidence = 0.5
	}
	if tracer != nil && isAliasLookupScopedKind(subject.Kind) {
		tracer.Trace(ResolutionTraceEvent{
			RequestID: requestID, Stage: "identity_gate", Subject: subject,
			FromKeyedIdentityLookup: node.FromKeyedIdentityLookup, EligibleKind: isAliasIdentityEligibleKind(subject.Kind),
			AliasMatched: aliasMatched, ProviderMatched: providerMatched,
			GateFired: matched || identityTrusted, FinalConfidence: confidence,
		})
	}
	// CHAOS-3893: node.Mechanism is trusted AS DECLARED for a lookup-sourced
	// node (AliasLookup's own doc comment, resolve.go) even when the LOCAL
	// aliasMatched/providerMatched checks below are false -- exactly the
	// same "graph attribute can be stale, the adapter's own read is not"
	// gap TestNodeCandidate_IdentityTrustedAloneBoostsConfidenceDespiteAStaleGraphAttribute
	// already covers for CONFIDENCE. Before this fix the reason prose had
	// no matching fallback: an Option-C adapter-declared match whose graph
	// attribute happened to be stale/absent fell all the way through to
	// the generic "Hybrid graph search..." text, even though the mechanism
	// tagging below (unconditionally seeded from node.Mechanism) already
	// knew exactly which key class matched. Checked AFTER matched/
	// aliasMatched/providerMatched so a genuine local match keeps its own
	// (identical) reason text; this only fills the gap those three leave
	// open.
	reason := "Hybrid graph search matched the subject label or indexed context."
	switch {
	case matched:
		reason = "Exact canonical subject label match."
	case aliasMatched:
		reason = "Repository/project alias matched."
	case providerMatched:
		reason = "Provider-qualified identifier matched."
	case node.Mechanism == contextfabric.MatchAlias:
		reason = "Repository/project alias matched."
	case node.Mechanism == contextfabric.MatchProviderKey:
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
