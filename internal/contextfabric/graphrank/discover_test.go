package graphrank

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestAdmitEdgesAssignsClosedVocabularyRelationshipCategory is the H4
// regression test (CHAOS-3752 rebase onto main's CHAOS-3755, which landed
// the closed driver-category vocabulary): main's H4 fix changed
// zepgraph.relationMeaning to return "relationship" for every graph-derived
// driver candidate, because the more descriptive strings it originally used
// ("dependency"/"pressure"/"signal") are not members of the closed
// ContextFabricDriverCategory enum and would make driver.Validate() reject
// EVERY graph-discovered driver outright once Category started being
// validated as a closed enum.
//
// The CHAOS-3752 graphrank extraction (this package) was branched before
// H4 landed, so its own copy of relationMeaning silently reintroduced the
// pre-H4 strings when this branch rebased onto main. No test in graphrank,
// zepgraph, or falkorgraph previously asserted a graph-discovered driver's
// Category value at all -- this test closes that gap directly against the
// shared AdmitEdges entry point every backend adapter calls, so neither
// backend can regress this again silently.
func TestAdmitEdgesAssignsClosedVocabularyRelationshipCategory(t *testing.T) {
	from := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_from", Label: "From Work"}
	to := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_to", Label: "To Work"}
	options := contextfabric.InvestigationOptions{
		MaxSubjectCandidates: 10, MaxCohortMembers: 10, MaxRelationshipPaths: 10,
		MaxDrivers: 10, MaxEvidenceRefs: 50, MaxSerializedBytes: 262144,
	}
	// One relation name per relationMeaning branch -- BLOCKS (principal),
	// CAUSES (contributing), INDICATES (symptom) -- plus a name matching no
	// branch (context, driver=false, excluded from Drivers) to prove the
	// closed category applies uniformly, not only to the branches that
	// happen to produce a driver.
	relations := []string{"BLOCKS", "CAUSES", "INDICATES"}
	edges := make([]ResolvedEdge, 0, len(relations))
	for i, name := range relations {
		edges = append(edges, ResolvedEdge{
			UUID: "edge_" + name, Name: name, Fact: "test fact",
			From: from, To: to, Attributes: map[string]interface{}{"evidence_refs": []string{"evidence_" + name}},
			CreatedAt: "2026-01-01T00:00:00Z",
		})
		_ = i
	}

	result := AdmitEdges("org-1", edges, options, func(contextfabric.SubjectRef) bool { return false })
	if len(result.Drivers) != len(relations) {
		t.Fatalf("AdmitEdges() produced %d drivers, want %d: %#v", len(result.Drivers), len(relations), result.Drivers)
	}
	for _, driver := range result.Drivers {
		if driver.Category != "relationship" {
			t.Fatalf("driver.Category = %q, want \"relationship\" (the closed-vocabulary category every graph-discovered driver must use)", driver.Category)
		}
		if err := driver.Validate(); err != nil {
			t.Fatalf("driver.Validate() error = %v, want a graph-discovered driver to validate against the closed category enum: %#v", err, driver)
		}
	}
}

func discoverOptions() contextfabric.InvestigationOptions {
	return contextfabric.InvestigationOptions{
		MaxSubjectCandidates: 10, MaxCohortMembers: 10, MaxRelationshipPaths: 10,
		MaxDrivers: 10, MaxEvidenceRefs: 50, MaxSerializedBytes: 262144,
	}
}

func testResolvedEdge(uuid, name string, from, to contextfabric.SubjectRef, relevance float64, evidenceRefs ...string) ResolvedEdge {
	r := relevance
	return ResolvedEdge{
		UUID: uuid, Name: name, Fact: "test fact", From: from, To: to, Relevance: &r,
		Attributes: map[string]interface{}{"evidence_refs": evidenceRefs, "epistemic_status": "observed"},
		CreatedAt:  "2026-01-01T00:00:00Z",
	}
}

// sortResolvedEdges mirrors the adapter-owned pattern (zepgraph.DiscoverContext,
// falkorgraph.DiscoverContext) of sorting a CandidateEdge-shaped view before
// resolving endpoints, then applying that order to the already-resolved
// ResolvedEdge list -- since AdmitEdges itself does not re-sort (its own doc
// comment: "edges must already be in the backend's intended admission
// order").
func sortResolvedEdges(edges []ResolvedEdge) []ResolvedEdge {
	candidates := make([]CandidateEdge, 0, len(edges))
	for _, e := range edges {
		candidates = append(candidates, CandidateEdge{UUID: e.UUID, Relevance: e.Relevance, Score: e.Score})
	}
	order := SortEdgesByRelevance(candidates)
	byUUID := make(map[string]ResolvedEdge, len(edges))
	for _, e := range edges {
		byUUID[e.UUID] = e
	}
	ordered := make([]ResolvedEdge, 0, len(edges))
	for _, c := range order {
		ordered = append(ordered, byUUID[c.UUID])
	}
	return ordered
}

// TestAdmitEdgesTruncatesEvidenceRefsToBudget is the direct port of
// zepgraph's TestDiscoverContextTruncatesEvidenceRefsToBudget: proves
// Options.MaxEvidenceRefs is enforced on the aggregated evidence list AND
// on every path's/driver's own EvidenceRefIDs (Codex finding G5), not just
// the flat aggregate.
func TestAdmitEdgesTruncatesEvidenceRefsToBudget(t *testing.T) {
	t.Parallel()
	project := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	first := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_1", Label: "Work One"}
	second := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_2", Label: "Work Two"}
	edges := []ResolvedEdge{
		testResolvedEdge("edge-1", "BLOCKS", project, first, 0.9, "evidence_one_1234"),
		testResolvedEdge("edge-2", "BLOCKS", project, second, 0.9, "evidence_two_1234"),
	}
	options := discoverOptions()
	options.MaxEvidenceRefs = 1
	result := AdmitEdges("org_1", edges, options, noInternalSubjects)
	if len(result.EvidenceRefIDs) != 1 {
		t.Fatalf("evidence ref IDs = %#v, want truncated to Options.MaxEvidenceRefs=1", result.EvidenceRefIDs)
	}
	allEvidence := make(map[string]struct{})
	for _, path := range result.Paths {
		for _, id := range path.EvidenceRefIDs {
			allEvidence[id] = struct{}{}
		}
	}
	for _, driver := range result.Drivers {
		for _, id := range driver.EvidenceRefIDs {
			allEvidence[id] = struct{}{}
		}
	}
	for _, id := range result.EvidenceRefIDs {
		allEvidence[id] = struct{}{}
	}
	if len(allEvidence) > 1 {
		t.Fatalf("distinct evidence across paths+drivers+aggregate = %#v, want at most Options.MaxEvidenceRefs=1", allEvidence)
	}
}

// TestAdmitEdgesAdmitsHigherRelevanceEdgeRegardlessOfInputOrder is the
// direct port of zepgraph's TestDiscoverContextAdmitsHigherRelevanceEdgeRegardlessOfBackendOrder
// (Codex round-2 finding N2): sorting before admission must let a materially
// more relevant edge win a scarce evidence budget over a low-relevance edge
// that merely appeared first in backend order.
func TestAdmitEdgesAdmitsHigherRelevanceEdgeRegardlessOfInputOrder(t *testing.T) {
	t.Parallel()
	project := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	low := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_low", Label: "Work Low"}
	high := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_high", Label: "Work High"}
	// Low-relevance edge placed FIRST in input order; high-relevance second.
	edges := []ResolvedEdge{
		testResolvedEdge("edge-low", "BLOCKS", project, low, 0.2, "evidence_low_12345"),
		testResolvedEdge("edge-high", "BLOCKS", project, high, 0.9, "evidence_high_12345"),
	}
	options := discoverOptions()
	options.MaxEvidenceRefs = 1
	result := AdmitEdges("org_1", sortResolvedEdges(edges), options, noInternalSubjects)
	if len(result.Paths) != 1 || result.Paths[0].Nodes[1].CanonicalID != "work_high" {
		t.Fatalf("paths = %#v, want the higher-relevance edge admitted under a scarce evidence budget, regardless of input order", result.Paths)
	}
}

// TestAdmitEdgesDoesNotConsumeEvidenceBudgetForARejectedPath is the direct
// port of zepgraph's TestDiscoverContextDoesNotConsumeEvidenceBudgetForARejectedPath
// (Codex round-2 finding N3): the shared evidence set must only be mutated
// once a path is actually accepted -- an edge whose resulting path fails
// RelationshipPath.Validate() must not have permanently consumed its share
// of a scarce evidence budget.
func TestAdmitEdgesDoesNotConsumeEvidenceBudgetForARejectedPath(t *testing.T) {
	t.Parallel()
	project := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	invalidTarget := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_invalid", Label: "Work Invalid"}
	validTarget := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_valid", Label: "Work Valid"}
	edges := []ResolvedEdge{
		// "short" is under the v1 contract's minimum evidence ref ID length,
		// so the resulting path fails Validate() and must never be admitted.
		testResolvedEdge("edge-invalid", "BLOCKS", project, invalidTarget, 0.9, "short"),
		testResolvedEdge("edge-valid", "BLOCKS", project, validTarget, 0.5, "evidence_valid_12345"),
	}
	options := discoverOptions()
	options.MaxEvidenceRefs = 1
	result := AdmitEdges("org_1", edges, options, noInternalSubjects)
	if len(result.Paths) != 1 || result.Paths[0].Nodes[1].CanonicalID != "work_valid" {
		t.Fatalf("paths = %#v, want the valid edge admitted -- the invalid edge must not have consumed the evidence budget before its path was rejected", result.Paths)
	}
	if len(result.EvidenceRefIDs) != 1 || result.EvidenceRefIDs[0] != "evidence_valid_12345" {
		t.Fatalf("evidence ref IDs = %#v, want only the admitted edge's evidence", result.EvidenceRefIDs)
	}
}

// TestAdmitEdgesTieBreaksDeterministicallyOnEqualRelevance is the direct
// port of zepgraph's TestDiscoverContextEdgeAdmissionTieBreaksDeterministicallyOnEqualRelevance:
// two edges with EQUAL relevance must still admit deterministically (smaller
// UUID wins), not depend on map/backend iteration order.
func TestAdmitEdgesTieBreaksDeterministicallyOnEqualRelevance(t *testing.T) {
	t.Parallel()
	project := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	first := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_first", Label: "Work First"}
	second := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_second", Label: "Work Second"}
	// "edge-aaa" < "edge-bbb" lexicographically; equal relevance on both.
	edges := []ResolvedEdge{
		testResolvedEdge("edge-bbb", "BLOCKS", project, second, 0.5, "evidence_second_12345"),
		testResolvedEdge("edge-aaa", "BLOCKS", project, first, 0.5, "evidence_first_123456"),
	}
	options := discoverOptions()
	options.MaxEvidenceRefs = 1
	result := AdmitEdges("org_1", sortResolvedEdges(edges), options, noInternalSubjects)
	if len(result.Paths) != 1 || result.Paths[0].Nodes[1].CanonicalID != "work_first" {
		t.Fatalf("paths = %#v, want the lexicographically-smaller edge UUID (edge-aaa) to win the equal-relevance tie deterministically", result.Paths)
	}
}

// TestAdmitEdgesExcludesInternalBookkeepingRelationships is the direct port
// of zepgraph's TestDiscoverContextExcludesInternalBookkeepingRelationships:
// an edge touching a subject the isInternal predicate flags as
// adapter-internal bookkeeping must never surface as a public relationship
// path, even if it carries evidence.
func TestAdmitEdgesExcludesInternalBookkeepingRelationships(t *testing.T) {
	t.Parallel()
	root := contextfabric.SubjectRef{Kind: contextfabric.SubjectOrganization, CanonicalID: "organization-root", Label: "Organization"}
	subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	edges := []ResolvedEdge{testResolvedEdge("edge-bookkeeping", "HAS_SUBJECT", root, subject, 0, "evidence_leaked_1234")}
	isReserved := func(s contextfabric.SubjectRef) bool { return s.CanonicalID == "organization-root" }
	result := AdmitEdges("org_1", edges, discoverOptions(), isReserved)
	if len(result.Paths) != 0 || len(result.EvidenceRefIDs) != 0 {
		t.Fatalf("result = %#v, want internal bookkeeping relationship excluded", result)
	}
}

// TestDiscoveredCohortExcludesInternalBookkeepingSubjects is the direct port
// of zepgraph's same-named test (Codex finding G8(b)): discoveredCohort's
// membership loop must call isInternal itself, not rely on an accident of
// interpretedCohortKind's kind range -- a node whose reported subject_kind
// coincides with the interpreted cohort kind, while its canonical_id still
// carries a reserved bookkeeping identifier, must still be excluded.
func TestDiscoveredCohortExcludesInternalBookkeepingSubjects(t *testing.T) {
	t.Parallel()
	principal := storage.Principal{OrgID: "org_1"}
	// subject_kind reports "team" -- matching interpretedCohortKind's output
	// for a "teams" judgment below -- while canonical_id still carries the
	// reserved organization-root identifier.
	impostorRoot := candidateNode(contextfabric.SubjectTeam, "organization-root", "Organization", 0.9, "*")
	genuineTeam := candidateNode(contextfabric.SubjectTeam, "team_platform", "Platform", 0.9, "*")
	isReserved := func(s contextfabric.SubjectRef) bool { return s.CanonicalID == "organization-root" }
	discovery := contextfabric.GraphDiscoveryRequest{
		Request: testRequest(),
		Interpretation: contextfabric.InterpretedQuestion{
			Shape: contextfabric.ShapeDiscoveredCohort, RequestedJudgment: "teams_under_pressure",
			TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactHealth}},
		},
		Resolution: contextfabric.SubjectResolution{Candidates: []contextfabric.SubjectCandidate{}, Committed: []contextfabric.SubjectRef{}},
	}
	discovery.Request.Options.MaxCohortMembers = 10
	cohort := DiscoveredCohort(principal, discovery, []CandidateNode{impostorRoot, genuineTeam}, isReserved)
	if cohort == nil {
		t.Fatal("cohort = nil, want the genuine team still discovered")
	}
	for _, member := range cohort.Members {
		if member.Subject.CanonicalID == "organization-root" {
			t.Fatalf("cohort = %#v, an internal bookkeeping identifier must never surface as a cohort member", cohort)
		}
	}
}
