package graphrank

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// noInternalSubjects is the isInternal predicate every test in this file
// uses unless it is specifically testing the isInternal exclusion path --
// mirrors falkorgraph's own isInternalSubject (always false: falkorgraph has
// no anchor/marker nodes) and is deliberately NOT zepgraph's
// isInternalBookkeepingSubject, which stays zepgraph-local per the design
// doc (§7: "Not moved -- a Zep-specific concept").
func noInternalSubjects(contextfabric.SubjectRef) bool { return false }

func candidateNode(kind contextfabric.SubjectKind, canonicalID, label string, relevance float64, repos interface{}) CandidateNode {
	attrs := map[string]interface{}{
		"canonical_id":  canonicalID,
		"subject_kind":  string(kind),
		"label":         label,
		"evidence_refs": []string{"evidence_identity_1234"},
	}
	if repos != nil {
		attrs["authorization_repositories"] = repos
	}
	r := relevance
	return CandidateNode{UUID: "node-" + canonicalID, Name: label, Relevance: Normalized(r), Attributes: attrs}
}

// TestNodeCandidateFiltersUnauthorizedNodesBeforeCandidates is the direct
// port of zepgraph's TestResolveSubjectsFiltersUnauthorizedNodesBeforeCandidates,
// translated from ResolveSubjects' end-to-end wiring to NodeCandidate/
// AuthorizedAttributes directly: a node whose authorization_repositories
// list does not admit the calling principal's repository scope must never
// become a candidate, even alongside an authorized one.
func TestNodeCandidateFiltersUnauthorizedNodesBeforeCandidates(t *testing.T) {
	t.Parallel()
	principal := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"full-chaos/dev-health-acr"}}
	scope := contextfabric.RequestedScope{}
	authorized := candidateNode(contextfabric.SubjectProject, "project_ask_dev", "Ask Dev", 1, []string{"full-chaos/dev-health-acr"})
	foreign := candidateNode(contextfabric.SubjectProject, "project_foreign", "Ask Dev Foreign", 0.99, []string{"other/private"})

	if _, ok := NodeCandidate(principal, scope, "Ask Dev", foreign, noInternalSubjects, true, nil, ""); ok {
		t.Fatal("NodeCandidate() admitted a node outside the principal's repository scope")
	}
	candidate, ok := NodeCandidate(principal, scope, "Ask Dev", authorized, noInternalSubjects, true, nil, "")
	if !ok || candidate.Subject.CanonicalID != "project_ask_dev" {
		t.Fatalf("NodeCandidate() for the authorized node = %#v, ok=%v", candidate, ok)
	}
}

// TestNodeCandidateExcludesInternalBookkeepingSubjects is the direct port of
// zepgraph's TestResolveSubjectsExcludesInternalBookkeepingSubjectsFromCandidates:
// a node the caller's isInternal predicate flags as adapter-internal
// bookkeeping must never become a candidate, regardless of authorization or
// relevance.
func TestNodeCandidateExcludesInternalBookkeepingSubjects(t *testing.T) {
	t.Parallel()
	principal := storage.Principal{OrgID: "org_1"}
	scope := contextfabric.RequestedScope{}
	root := candidateNode(contextfabric.SubjectOrganization, "organization-root", "Organization", 0.99, "*")
	isReserved := func(subject contextfabric.SubjectRef) bool {
		return subject.CanonicalID == "organization-root"
	}
	if _, ok := NodeCandidate(principal, scope, "organization", root, isReserved, true, nil, ""); ok {
		t.Fatal("NodeCandidate() admitted a node the isInternal predicate flagged as reserved bookkeeping")
	}
	// Control: the same node is admitted when isInternal never flags it,
	// proving the exclusion above is really isInternal's doing and not some
	// other property of the fixture (e.g. its kind or authorization scope).
	if _, ok := NodeCandidate(principal, scope, "organization", root, noInternalSubjects, true, nil, ""); !ok {
		t.Fatal("NodeCandidate() control case: same node must be admitted when isInternal never flags it")
	}
}

// TestNodeCandidateAcceptsHybridMatchWhenTermDoesNotEqualLabel is the direct
// port of zepgraph's TestResolveSubjectsResolvesSubjectMatchedByAliasOrPreviousName,
// translated to graphrank's actual concern: zepgraph's alias/previous-name
// embedding into indexed search text is entirely backend-specific write-path
// behavior with no graphrank equivalent (graphrank never sees alias text,
// only whatever CandidateNode a backend's Search callback already
// returned) -- what graphrank DOES own is correctly scoring the result once
// a backend's search matches on something other than the exact canonical
// label: NodeCandidate must still admit it via the hybrid-confidence path
// (not the exact-match fast path, which requires term==Name or term==Label)
// and must resolve the correct canonical subject.
func TestNodeCandidateAcceptsHybridMatchWhenTermDoesNotEqualLabel(t *testing.T) {
	t.Parallel()
	principal := storage.Principal{OrgID: "org_1"}
	scope := contextfabric.RequestedScope{}
	// The canonical label is "Ask Dev"; the search term "Dev Agent" matches
	// neither node.Name nor subject.Label, so the exact-match fast path
	// cannot fire -- this exercises the hybrid-confidence path exclusively.
	node := candidateNode(contextfabric.SubjectProject, "project_ask_dev", "Ask Dev", 0.9, "*")
	candidate, ok := NodeCandidate(principal, scope, "Dev Agent", node, noInternalSubjects, true, nil, "")
	if !ok || candidate.Subject.CanonicalID != "project_ask_dev" {
		t.Fatalf("NodeCandidate() for a hybrid (non-exact) match = %#v, ok=%v", candidate, ok)
	}
	if candidate.Confidence != ResultConfidence(node.Relevance, node.Score) {
		t.Fatalf("NodeCandidate() confidence = %v, want the raw ResultConfidence for a non-exact hybrid match", candidate.Confidence)
	}
	if candidate.MatchReasons[0] != "Hybrid graph search matched the subject label or indexed context." {
		t.Fatalf("NodeCandidate() match reason = %q, want the hybrid (non-exact) reason", candidate.MatchReasons[0])
	}
}

// TestNodeCandidate_AllowExactMatchFalseNeverPromotesEvenOnLiteralEquality is
// the codex round-2 P1 regression proof: a subject legitimately labeled the
// EXACT SAME literal string as term must still NOT get the exact-match
// promotion (confidence 1.0, MatchExact) when allowExactMatch is false --
// this is the structural guarantee CHAOS-3838's question-provenance marker
// depends on (a real subject could, in principle, be labeled
// "[full question]"; that must never let a question-level vector-only find
// masquerade as an exact caller-typed match). Confidence and mechanism must
// come from node.Relevance/node.Mechanism alone. MUTATION CHECK: passing
// true instead of false here reproduces confidence==1 / MatchExact present,
// which TestNodeCandidateAcceptsHybridMatchWhenTermDoesNotEqualLabel's
// sibling exact-match tests already prove is the true-path behavior.
func TestNodeCandidate_AllowExactMatchFalseNeverPromotesEvenOnLiteralEquality(t *testing.T) {
	t.Parallel()
	principal := storage.Principal{OrgID: "org_1"}
	scope := contextfabric.RequestedScope{}
	const marker = "[full question]"
	node := candidateNode(contextfabric.SubjectProject, "project_literal", marker, 0.9, "*")
	node.Mechanism = contextfabric.MatchVector

	candidate, ok := NodeCandidate(principal, scope, marker, node, noInternalSubjects, false, nil, "")
	if !ok {
		t.Fatal("NodeCandidate() rejected a legitimately authorized, valid node")
	}
	if candidate.Confidence == 1 {
		t.Fatalf("NodeCandidate(allowExactMatch=false) confidence = 1, want it derived ONLY from node.Relevance/node.Score despite term literally equaling the node's own label/name")
	}
	if candidate.Confidence != ResultConfidence(node.Relevance, node.Score) {
		t.Fatalf("NodeCandidate(allowExactMatch=false) confidence = %v, want the raw ResultConfidence %v", candidate.Confidence, ResultConfidence(node.Relevance, node.Score))
	}
	if HasMechanism(candidate.MatchMechanisms, contextfabric.MatchExact) {
		t.Fatalf("NodeCandidate(allowExactMatch=false) mechanisms = %v, want MatchExact absent despite literal term/label equality", candidate.MatchMechanisms)
	}
	if !HasMechanism(candidate.MatchMechanisms, contextfabric.MatchVector) || DistinctMechanismCount(candidate.MatchMechanisms) != 1 {
		t.Fatalf("NodeCandidate(allowExactMatch=false) mechanisms = %v, want ONLY the adapter-declared mechanism (MatchVector)", candidate.MatchMechanisms)
	}

	// Control: the SAME literal-equal fixture WITH allowExactMatch=true
	// exact-matches normally -- proving the guard above is allowExactMatch's
	// doing, not some other property of this fixture (e.g. label length).
	control, ok := NodeCandidate(principal, scope, marker, node, noInternalSubjects, true, nil, "")
	if !ok || control.Confidence != 1 || !HasMechanism(control.MatchMechanisms, contextfabric.MatchExact) {
		t.Fatalf("control (allowExactMatch=true) = %#v, ok=%v, want confidence=1 and MatchExact present for a genuine literal match", control, ok)
	}
}
