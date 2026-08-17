package graphrank

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// aliasCandidateNode builds a CandidateNode carrying aliases/provider_aliases
// attributes, mirroring candidateNode's shape (candidate_test.go) but with
// the two additional identity attributes falkorgraph's subjectMergeAttrs
// writes (propAliases/propProviderAliases). relevance<0 means "no
// Relevance/Score at all" (ResultConfidence(nil,nil)==0, floored to 0.5) --
// the shape a keyed-identity-read-sourced node actually has (no ranked
// score, see FromKeyedIdentityLookup's own doc comment).
func aliasCandidateNode(kind contextfabric.SubjectKind, canonicalID, label string, relevance float64, aliases, providerAliases []string, fromKeyedLookup bool) CandidateNode {
	attrs := map[string]interface{}{
		"canonical_id":  canonicalID,
		"subject_kind":  string(kind),
		"label":         label,
		"evidence_refs": []string{"evidence_identity_1234"},
		"aliases":       aliases,
	}
	if providerAliases != nil {
		attrs["provider_aliases"] = providerAliases
	}
	node := CandidateNode{UUID: "node-" + canonicalID, Name: label, Attributes: attrs, FromKeyedIdentityLookup: fromKeyedLookup}
	if relevance >= 0 {
		node.Relevance = Normalized(relevance)
	}
	return node
}

// TestNodeCandidate_AliasMatchTaggedButNotTrustedWithoutKeyedLookup is
// BLOCKING-1's positive proof: an ordinary (non-keyed-lookup) node whose
// alias attribute contains the term is TAGGED MatchAlias (so it is
// discoverable/countable, HIGH-5) but its confidence is NOT bumped to 1 --
// FromKeyedIdentityLookup is false, so identityTrusted is false regardless
// of kind eligibility.
func TestNodeCandidate_AliasMatchTaggedButNotTrustedWithoutKeyedLookup(t *testing.T) {
	t.Parallel()
	principal := storage.Principal{OrgID: "org_1"}
	scope := contextfabric.RequestedScope{}
	node := aliasCandidateNode(contextfabric.SubjectRepository, "repository_1", "full-chaos/dev-health-acr", -1, []string{"dev-health-acr"}, nil, false)

	candidate, ok := NodeCandidate(principal, scope, "dev-health-acr", node, noInternalSubjects, true)
	if !ok {
		t.Fatal("NodeCandidate() rejected a legitimately authorized alias-matching node")
	}
	if !HasMechanism(candidate.MatchMechanisms, contextfabric.MatchAlias) {
		t.Fatalf("mechanisms = %v, want MatchAlias present (attribution must not depend on FromKeyedIdentityLookup)", candidate.MatchMechanisms)
	}
	if candidate.Confidence == 1 {
		t.Fatalf("confidence = 1, want the ordinary floor (0.5) -- FromKeyedIdentityLookup=false must never earn the identity trust bump")
	}
	if candidate.Confidence != 0.5 {
		t.Fatalf("confidence = %v, want 0.5 (ResultConfidence(nil,nil) floored)", candidate.Confidence)
	}
}

// TestNodeCandidate_AliasMatchTrustedWithKeyedLookupOnEligibleKind is the
// companion positive case: the SAME match, but FromKeyedIdentityLookup=true
// on an eligible kind (repository) -- confidence bumps to 1.
func TestNodeCandidate_AliasMatchTrustedWithKeyedLookupOnEligibleKind(t *testing.T) {
	t.Parallel()
	principal := storage.Principal{OrgID: "org_1"}
	scope := contextfabric.RequestedScope{}
	node := aliasCandidateNode(contextfabric.SubjectRepository, "repository_1", "full-chaos/dev-health-acr", -1, []string{"dev-health-acr"}, nil, true)

	candidate, ok := NodeCandidate(principal, scope, "dev-health-acr", node, noInternalSubjects, true)
	if !ok {
		t.Fatal("NodeCandidate() rejected a legitimately authorized alias-matching node")
	}
	if !HasMechanism(candidate.MatchMechanisms, contextfabric.MatchAlias) {
		t.Fatalf("mechanisms = %v, want MatchAlias present", candidate.MatchMechanisms)
	}
	if candidate.Confidence != 1 {
		t.Fatalf("confidence = %v, want 1 (FromKeyedIdentityLookup=true on an eligible kind)", candidate.Confidence)
	}
	if candidate.MatchReasons[0] != "Repository/project alias matched." {
		t.Fatalf("match reason = %q, want the alias-specific reason", candidate.MatchReasons[0])
	}
}

// TestNodeCandidate_IdentityTrustedAloneBoostsConfidenceDespiteAStaleGraphAttribute
// is the live-reproduced projection-lag bug's OWN regression pin
// (team-lead ruling, 2026-08-17, guardrail 3(a)): the graph node's OWN
// "aliases" attribute does NOT contain this term -- exactly the shape a
// node projected before this ticket's alias-computation logic existed
// carries, or one whose next projection cycle simply hasn't run yet --
// while node.Mechanism/FromKeyedIdentityLookup are set exactly as
// reader.go's AliasLookup closure sets them: a genuine, existence-checked
// match against FRESH ClickHouse data. This must still boost to confidence
// 1 -- identityTrusted alone is the proof, not a second re-derivation
// against the (here, deliberately stale) graph attribute.
//
// This test is written to FAIL on the pre-fix gate
// (`(aliasMatched||providerMatched) && identityTrusted`, which requires
// aliasMatched -- and aliasMatched is false here BY CONSTRUCTION, since
// AliasAttributes(node.Attributes) does not contain "dev-health-acr") and
// PASS on the fixed gate (`matched || identityTrusted`). Verified by hand
// against both: reverting candidate.go's fix locally reproduces
// `confidence = 0.5, want 1` on this exact test.
func TestNodeCandidate_IdentityTrustedAloneBoostsConfidenceDespiteAStaleGraphAttribute(t *testing.T) {
	t.Parallel()
	principal := storage.Principal{OrgID: "org_1"}
	scope := contextfabric.RequestedScope{}
	// aliases: nil -- the graph's OWN stored attribute is stale/absent for
	// this term, unlike TestNodeCandidate_AliasMatchTrustedWithKeyedLookupOnEligibleKind
	// immediately above, which keeps it fresh/consistent.
	node := aliasCandidateNode(contextfabric.SubjectRepository, "repository_1", "full-chaos/dev-health-acr", -1, nil, nil, true)
	// reader.go sets node.Mechanism = match.Mechanism (from
	// graphrank.MatchIdentityRows, matched against FRESH ClickHouse data)
	// BEFORE FromKeyedIdentityLookup=true -- reproduced explicitly here
	// since aliasCandidateNode itself does not set it.
	node.Mechanism = contextfabric.MatchAlias

	candidate, ok := NodeCandidate(principal, scope, "dev-health-acr", node, noInternalSubjects, true)
	if !ok {
		t.Fatal("NodeCandidate() rejected a legitimately authorized node")
	}
	if candidate.Confidence != 1 {
		t.Fatalf("confidence = %v, want 1 -- identityTrusted alone (FromKeyedIdentityLookup=true, eligible kind) must boost confidence even when the graph's own \"aliases\" attribute does not (yet) contain this term (the live-reproduced projection-lag bug)", candidate.Confidence)
	}
	if !HasMechanism(candidate.MatchMechanisms, contextfabric.MatchAlias) {
		t.Fatalf("mechanisms = %v, want MatchAlias present (trusted-as-declared from node.Mechanism, unaffected by this fix)", candidate.MatchMechanisms)
	}
}

// TestNodeCandidate_KeyedLookupNeverTrustsNonEligibleKind is BLOCKING-1's
// other half: a team candidate (scoped for COUNTING, never eligible for the
// confidence bump) found via the keyed lookup is STILL tagged MatchAlias
// but keeps its ordinary confidence -- the eligibility gate, not the
// keyed-lookup marker alone, decides trust.
func TestNodeCandidate_KeyedLookupNeverTrustsNonEligibleKind(t *testing.T) {
	t.Parallel()
	principal := storage.Principal{OrgID: "org_1"}
	scope := contextfabric.RequestedScope{}
	node := aliasCandidateNode(contextfabric.SubjectTeam, "team_1", "Chaos Team", -1, []string{"chaos"}, nil, true)

	candidate, ok := NodeCandidate(principal, scope, "chaos", node, noInternalSubjects, true)
	if !ok {
		t.Fatal("NodeCandidate() rejected a legitimately authorized alias-matching team node")
	}
	if !HasMechanism(candidate.MatchMechanisms, contextfabric.MatchAlias) {
		t.Fatalf("mechanisms = %v, want MatchAlias present (team is in the COUNTING scope)", candidate.MatchMechanisms)
	}
	if candidate.Confidence == 1 {
		t.Fatal("confidence = 1, want the ordinary floor -- team is not in isAliasIdentityEligibleKind regardless of FromKeyedIdentityLookup")
	}
}

// TestNodeCandidate_NonScopedKindNeverAttemptsAliasMatch is defense in
// depth: a kind outside isAliasLookupScopedKind entirely (e.g.
// ci_pipeline_run) must not even ATTEMPT the alias comparison, even if its
// attributes happen to carry an "aliases" list that would otherwise match
// -- this is what keeps the large, non-enumerable observation-kind
// populations (CHAOS-3810's own accepted residual scope) untouched by this
// ticket.
func TestNodeCandidate_NonScopedKindNeverAttemptsAliasMatch(t *testing.T) {
	t.Parallel()
	principal := storage.Principal{OrgID: "org_1"}
	scope := contextfabric.RequestedScope{}
	node := aliasCandidateNode(contractsCIRunKind(), "ci_run_1", "nightly build", -1, []string{"chaos"}, nil, true)

	candidate, ok := NodeCandidate(principal, scope, "chaos", node, noInternalSubjects, true)
	if !ok {
		t.Fatal("NodeCandidate() rejected a legitimately authorized node")
	}
	if HasMechanism(candidate.MatchMechanisms, contextfabric.MatchAlias) {
		t.Fatalf("mechanisms = %v, want MatchAlias absent -- ci_pipeline_run is outside isAliasLookupScopedKind", candidate.MatchMechanisms)
	}
	if candidate.Confidence == 1 {
		t.Fatal("confidence = 1, want the ordinary floor -- an out-of-scope kind must never earn the identity bump")
	}
}

// TestNodeCandidate_ProviderAliasMatchTaggedDistinctlyFromAlias proves
// MatchProviderKey is a SEPARATE mechanism from MatchAlias, chosen only
// when the bare-alias list did not already match.
func TestNodeCandidate_ProviderAliasMatchTaggedDistinctlyFromAlias(t *testing.T) {
	t.Parallel()
	principal := storage.Principal{OrgID: "org_1"}
	scope := contextfabric.RequestedScope{}
	node := aliasCandidateNode(contextfabric.SubjectRepository, "repository_1", "full-chaos/dev-health-acr", -1, []string{"dev-health-acr"}, []string{"github:full-chaos/dev-health-acr"}, true)

	candidate, ok := NodeCandidate(principal, scope, "github:full-chaos/dev-health-acr", node, noInternalSubjects, true)
	if !ok {
		t.Fatal("NodeCandidate() rejected a legitimately authorized node")
	}
	if !HasMechanism(candidate.MatchMechanisms, contextfabric.MatchProviderKey) {
		t.Fatalf("mechanisms = %v, want MatchProviderKey present", candidate.MatchMechanisms)
	}
	if HasMechanism(candidate.MatchMechanisms, contextfabric.MatchAlias) {
		t.Fatalf("mechanisms = %v, want MatchAlias ABSENT -- the term matched the provider-alias list, not the bare-alias list", candidate.MatchMechanisms)
	}
	if candidate.Confidence != 1 {
		t.Fatalf("confidence = %v, want 1", candidate.Confidence)
	}
	if candidate.MatchReasons[0] != "Provider-qualified identifier matched." {
		t.Fatalf("match reason = %q, want the provider-key-specific reason", candidate.MatchReasons[0])
	}
}

// TestNodeCandidate_ExactLabelTakesPriorityOverAlias pins the precedence
// hierarchy (design doc Finding 1): when a term equals BOTH this
// candidate's own canonical label AND (coincidentally) an alias entry, the
// label match wins outright -- MatchExact only, never MatchAlias -- mutual
// exclusivity is load-bearing for exactIndex's own unconditional priority
// over identityIndex (MEDIUM-7's corrected rationale).
func TestNodeCandidate_ExactLabelTakesPriorityOverAlias(t *testing.T) {
	t.Parallel()
	principal := storage.Principal{OrgID: "org_1"}
	scope := contextfabric.RequestedScope{}
	// label IS "chaos-ops" (the project shape from the live-graph
	// dev-health-ops/chaos-ops collision) AND "chaos-ops" is ALSO (somewhat
	// artificially) listed in this SAME node's own aliases -- the point is
	// that the label check must win before the alias check is even tried.
	node := aliasCandidateNode(contextfabric.SubjectProject, "project_1", "chaos-ops", -1, []string{"chaos-ops"}, nil, true)

	candidate, ok := NodeCandidate(principal, scope, "chaos-ops", node, noInternalSubjects, true)
	if !ok {
		t.Fatal("NodeCandidate() rejected a legitimately authorized node")
	}
	if !HasMechanism(candidate.MatchMechanisms, contextfabric.MatchExact) {
		t.Fatalf("mechanisms = %v, want MatchExact present", candidate.MatchMechanisms)
	}
	if HasMechanism(candidate.MatchMechanisms, contextfabric.MatchAlias) {
		t.Fatalf("mechanisms = %v, want MatchAlias ABSENT -- exact label match must be mutually exclusive with the alias check", candidate.MatchMechanisms)
	}
	if candidate.MatchReasons[0] != "Exact canonical subject label match." {
		t.Fatalf("match reason = %q, want the exact-label reason, not the alias reason", candidate.MatchReasons[0])
	}
}

// TestNodeCandidate_AllowExactMatchFalseAlsoDisablesAliasMatch mirrors the
// pre-existing exact-match allowExactMatch=false guard
// (TestNodeCandidate_AllowExactMatchFalseNeverPromotesEvenOnLiteralEquality)
// for the alias path: CHAOS-3838's synthetic question-provenance marker
// must never win an alias-match promotion either.
func TestNodeCandidate_AllowExactMatchFalseAlsoDisablesAliasMatch(t *testing.T) {
	t.Parallel()
	principal := storage.Principal{OrgID: "org_1"}
	scope := contextfabric.RequestedScope{}
	const marker = "[full question]"
	node := aliasCandidateNode(contextfabric.SubjectRepository, "repository_1", "full-chaos/dev-health-acr", -1, []string{marker}, nil, true)

	candidate, ok := NodeCandidate(principal, scope, marker, node, noInternalSubjects, false)
	if !ok {
		t.Fatal("NodeCandidate() rejected a legitimately authorized node")
	}
	if HasMechanism(candidate.MatchMechanisms, contextfabric.MatchAlias) {
		t.Fatalf("mechanisms = %v, want MatchAlias absent when allowExactMatch=false", candidate.MatchMechanisms)
	}
	if candidate.Confidence == 1 {
		t.Fatal("confidence = 1, want it unaffected by the literal alias/term equality when allowExactMatch=false")
	}
}

// contractsCIRunKind isolates the ci_pipeline_run subject-kind literal to
// one place in this test file, matching how the trial provenance test
// fixtures (generative_trial_live_test.go) reference the same kind.
func contractsCIRunKind() contextfabric.SubjectKind {
	return contextfabric.SubjectKind("ci_pipeline_run")
}
