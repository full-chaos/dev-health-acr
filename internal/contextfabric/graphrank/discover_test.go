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
	// BLOCKS is the one relationMeaning branch CHAOS-3779 kept a producer
	// for. Before CHAOS-3779 this test also exercised CAUSES (contributing)
	// and INDICATES (symptom), but relationMeaning's doc comment now
	// records why those branches -- and five others -- were pruned as dead
	// code (drift item D12, AC-3779-9): they recognized a relation name no
	// projection ever produced. A recognizer entry with no producer is a
	// defect, not a placeholder, so the test roster shrank with the
	// production switch rather than keeping an untestable placeholder
	// branch alive.
	edges := []ResolvedEdge{{
		UUID: "edge_BLOCKS", Name: "BLOCKS", Fact: "test fact",
		From: from, To: to, Attributes: map[string]interface{}{"evidence_refs": []string{"evidence_BLOCKS"}},
		CreatedAt: "2026-01-01T00:00:00Z",
	}}

	result := AdmitEdges("org-1", edges, options, func(contextfabric.SubjectRef) bool { return false })
	if len(result.Drivers) != 1 {
		t.Fatalf("AdmitEdges() produced %d drivers, want 1: %#v", len(result.Drivers), result.Drivers)
	}
	driver := result.Drivers[0]
	if driver.Category != "relationship" {
		t.Fatalf("driver.Category = %q, want \"relationship\" (the closed-vocabulary category every graph-discovered driver must use)", driver.Category)
	}
	if driver.Standing != contextfabric.DriverPrincipal {
		t.Fatalf("driver.Standing = %q, want %q", driver.Standing, contextfabric.DriverPrincipal)
	}
	if err := driver.Validate(); err != nil {
		t.Fatalf("driver.Validate() error = %v, want a graph-discovered driver to validate against the closed category enum: %#v", err, driver)
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
		UUID: uuid, Name: name, Fact: "test fact", From: from, To: to, Relevance: Normalized(r),
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
// TestAdmitEdgesAnswersBlocksAndPartOfInOneHopWithEvidence binds AC-3779-5
// (retrieval half: "one test proves retrieval returns the edge with its
// evidence reference") and AC-3779-6 (one-hop) for CHAOS-3779's two
// implemented types.
//
// BLOCKS: before CHAOS-3779 the type field was a free string and the
// recognizer's own vocabulary was undocumented/untested (TRD §19.13
// Correction 1: the row was already flowing accidentally). Answering "what
// blocks work item B" had no test proving a single graph edge -- as
// opposed to a separate FactBlockers canonical-fact round trip, or a
// generic RELATED_TO edge a reader has to reinterpret -- carried the
// answer with its own evidence. This test proves it now does, in one hop,
// with a recognized driver standing.
//
// PART_OF: before CHAOS-3779 there was no producer for work_items.
// parent_id at all (see queryWorkItemHierarchy). "What is the parent of
// work item C" had no graph path whatsoever -- the only route was a
// bespoke ClickHouse query outside Context Fabric entirely. This test
// proves a single PART_OF edge now answers it in one hop, still carrying
// its own evidence reference even though (unlike BLOCKS) it is not a
// recognized driver -- see relationMeaning's doc comment for why PART_OF
// stays a plain structural path relationship.
func TestAdmitEdgesAnswersBlocksAndPartOfInOneHopWithEvidence(t *testing.T) {
	t.Parallel()

	workA := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_a", Label: "Work A"}
	workB := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_b", Label: "Work B"}
	blocksResult := AdmitEdges("org_1", []ResolvedEdge{
		testResolvedEdge("edge-blocks", "BLOCKS", workA, workB, 1, "evidence_blocks_1"),
	}, discoverOptions(), func(contextfabric.SubjectRef) bool { return false })
	if len(blocksResult.Paths) != 1 {
		t.Fatalf("BLOCKS: len(Paths) = %d, want 1: %#v", len(blocksResult.Paths), blocksResult.Paths)
	}
	blocksPath := blocksResult.Paths[0]
	if len(blocksPath.Nodes) != 2 || len(blocksPath.Edges) != 1 {
		t.Fatalf("BLOCKS: path is not one hop: %d nodes, %d edges: %#v", len(blocksPath.Nodes), len(blocksPath.Edges), blocksPath)
	}
	if blocksPath.Edges[0].Type != contextfabric.RelationshipType("BLOCKS") {
		t.Fatalf("BLOCKS: edge type = %q, want BLOCKS", blocksPath.Edges[0].Type)
	}
	if blocksPath.Edges[0].From != workA || blocksPath.Edges[0].To != workB {
		t.Fatalf("BLOCKS: edge does not connect work_a directly to work_b: %#v", blocksPath.Edges[0])
	}
	if len(blocksPath.Edges[0].EvidenceRefIDs) == 0 || blocksPath.Edges[0].EvidenceRefIDs[0] != "evidence_blocks_1" {
		t.Fatalf("BLOCKS: edge evidence = %#v, want [evidence_blocks_1]", blocksPath.Edges[0].EvidenceRefIDs)
	}
	if len(blocksResult.Drivers) != 1 || blocksResult.Drivers[0].Standing != contextfabric.DriverPrincipal {
		t.Fatalf("BLOCKS: drivers = %#v, want exactly one principal-standing driver", blocksResult.Drivers)
	}

	workC := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_c", Label: "Work C"}
	workParent := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_parent", Label: "Work Parent"}
	partOfResult := AdmitEdges("org_1", []ResolvedEdge{
		testResolvedEdge("edge-part-of", "PART_OF", workC, workParent, 1, "evidence_part_of_1"),
	}, discoverOptions(), func(contextfabric.SubjectRef) bool { return false })
	if len(partOfResult.Paths) != 1 {
		t.Fatalf("PART_OF: len(Paths) = %d, want 1: %#v", len(partOfResult.Paths), partOfResult.Paths)
	}
	partOfPath := partOfResult.Paths[0]
	if len(partOfPath.Nodes) != 2 || len(partOfPath.Edges) != 1 {
		t.Fatalf("PART_OF: path is not one hop: %d nodes, %d edges: %#v", len(partOfPath.Nodes), len(partOfPath.Edges), partOfPath)
	}
	if partOfPath.Edges[0].Type != contextfabric.RelationshipType("PART_OF") {
		t.Fatalf("PART_OF: edge type = %q, want PART_OF", partOfPath.Edges[0].Type)
	}
	if partOfPath.Edges[0].From != workC || partOfPath.Edges[0].To != workParent {
		t.Fatalf("PART_OF: edge does not connect work_c directly to work_parent: %#v", partOfPath.Edges[0])
	}
	if len(partOfPath.Edges[0].EvidenceRefIDs) == 0 || partOfPath.Edges[0].EvidenceRefIDs[0] != "evidence_part_of_1" {
		t.Fatalf("PART_OF: edge evidence = %#v, want [evidence_part_of_1]", partOfPath.Edges[0].EvidenceRefIDs)
	}
}

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
// the cohort kind's range -- a node whose reported subject_kind coincides
// with the cohort kind, while its canonical_id still carries a reserved
// bookkeeping identifier, must still be excluded.
func TestDiscoveredCohortExcludesInternalBookkeepingSubjects(t *testing.T) {
	t.Parallel()
	principal := storage.Principal{OrgID: "org_1"}
	// subject_kind reports "team" -- matching the frame's declared member
	// kind below -- while canonical_id still carries the reserved
	// organization-root identifier.
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
		// CHAOS-4736: the cohort kind is DECLARED by the frame, not guessed
		// from the judgment text above. These tests are about authorization
		// counting, not kind selection, so the frame simply states the kind
		// the judgment used to imply and every assertion below is unchanged.
		Frame: teamCohortFrame(),
	}
	discovery.Request.Options.MaxCohortMembers = 10
	cohort, _, _, _, _ := DiscoveredCohort(principal, discovery, []CandidateNode{impostorRoot, genuineTeam}, isReserved)
	if cohort == nil {
		t.Fatal("cohort = nil, want the genuine team still discovered")
	}
	for _, member := range cohort.Members {
		if member.Subject.CanonicalID == "organization-root" {
			t.Fatalf("cohort = %#v, an internal bookkeeping identifier must never surface as a cohort member", cohort)
		}
	}
}

// TestDiscoveredCohortReportsAuthzDroppedCount (CHAOS-3888) proves the
// cohort_members_authz_dropped signal: a node outside the principal's
// repository scope must be excluded from cohort membership exactly as
// before, AND now also counted in DiscoveredCohort's second return value --
// while an authorized sibling in the same node set must not inflate it.
func TestDiscoveredCohortReportsAuthzDroppedCount(t *testing.T) {
	t.Parallel()
	principal := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"full-chaos/dev-health-acr"}}
	authorized := candidateNode(contextfabric.SubjectTeam, "team_platform", "Platform", 0.9, []string{"full-chaos/dev-health-acr"})
	foreign := candidateNode(contextfabric.SubjectTeam, "team_foreign", "Foreign", 0.9, []string{"other/private"})
	discovery := contextfabric.GraphDiscoveryRequest{
		Request: testRequest(),
		Interpretation: contextfabric.InterpretedQuestion{
			Shape: contextfabric.ShapeDiscoveredCohort, RequestedJudgment: "teams_under_pressure",
			TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactHealth}},
		},
		Resolution: contextfabric.SubjectResolution{Candidates: []contextfabric.SubjectCandidate{}, Committed: []contextfabric.SubjectRef{}},
		// CHAOS-4736: the cohort kind is DECLARED by the frame, not guessed
		// from the judgment text above. These tests are about authorization
		// counting, not kind selection, so the frame simply states the kind
		// the judgment used to imply and every assertion below is unchanged.
		Frame: teamCohortFrame(),
	}
	discovery.Request.Options.MaxCohortMembers = 10
	cohort, authzDropped, kindScopedAuthzDropped, _, _ := DiscoveredCohort(principal, discovery, []CandidateNode{authorized, foreign}, noInternalSubjects)
	if cohort == nil || len(cohort.Members) != 1 || cohort.Members[0].Subject.CanonicalID != "team_platform" {
		t.Fatalf("cohort = %#v, want only the authorized team discovered", cohort)
	}
	if authzDropped != 1 {
		t.Fatalf("authzDropped = %d, want exactly 1 (the authorized member must not inflate it)", authzDropped)
	}
	if kindScopedAuthzDropped != 1 {
		t.Fatalf("kindScopedAuthzDropped = %d, want exactly 1 (both nodes are teams, so it must equal authzDropped here)", kindScopedAuthzDropped)
	}
}

// TestDiscoveredCohortKindScopedAuthzDroppedExcludesOtherKinds is CHAOS-4577
// (codex round-1 P2): the exact-name arm's pool mixes repository/project/team
// nodes in one fetch (chaos4348ExactNameCandidates' exactNameKinds), so the
// unscoped authzDropped counts a denied node of ANY kind. A caller asking
// "how many candidates for the cohort I actually asked about were denied"
// must see only kind-matching denials -- an unrelated denied repository node
// must not inflate kindScopedAuthzDropped for a teams question.
func TestDiscoveredCohortKindScopedAuthzDroppedExcludesOtherKinds(t *testing.T) {
	t.Parallel()
	principal := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"full-chaos/dev-health-acr"}}
	deniedRepo := candidateNode(contextfabric.SubjectRepository, "repo_private", "Private Repo", 0.9, []string{"other/private"})
	authorizedTeam := candidateNode(contextfabric.SubjectTeam, "team_platform", "Platform", 0.9, []string{"full-chaos/dev-health-acr"})
	discovery := contextfabric.GraphDiscoveryRequest{
		Request: testRequest(),
		Interpretation: contextfabric.InterpretedQuestion{
			Shape: contextfabric.ShapeDiscoveredCohort, RequestedJudgment: "teams_under_pressure",
			TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactHealth}},
		},
		Resolution: contextfabric.SubjectResolution{Candidates: []contextfabric.SubjectCandidate{}, Committed: []contextfabric.SubjectRef{}},
		// CHAOS-4736: the cohort kind is DECLARED by the frame, not guessed
		// from the judgment text above. These tests are about authorization
		// counting, not kind selection, so the frame simply states the kind
		// the judgment used to imply and every assertion below is unchanged.
		Frame: teamCohortFrame(),
	}
	discovery.Request.Options.MaxCohortMembers = 10
	cohort, authzDropped, kindScopedAuthzDropped, _, _ := DiscoveredCohort(principal, discovery, []CandidateNode{deniedRepo, authorizedTeam}, noInternalSubjects)
	if cohort == nil || len(cohort.Members) != 1 || cohort.Members[0].Subject.CanonicalID != "team_platform" {
		t.Fatalf("cohort = %#v, want the authorized team discovered despite the denied repository", cohort)
	}
	if authzDropped != 1 {
		t.Fatalf("authzDropped = %d, want exactly 1 (the denied repository)", authzDropped)
	}
	if kindScopedAuthzDropped != 0 {
		t.Fatalf("kindScopedAuthzDropped = %d, want exactly 0 -- the only denial was a repository, not a team, so it must not count toward a teams-cohort denial signal", kindScopedAuthzDropped)
	}
}

// TestAdmitEdgesReportsSelfLoopDropsSeparatelyFromInternalEndpointDrops
// (CHAOS-3888) proves DroppedSelfLoopCount: a self-loop edge and an
// internal-bookkeeping-endpoint edge are both excluded by the SAME combined
// condition as before (unchanged behavior), but only the self-loop is
// counted -- an internal-endpoint exclusion (routine and already expected on
// every call, per DroppedSelfLoopCount's own doc comment) must not inflate
// it.
func TestAdmitEdgesReportsSelfLoopDropsSeparatelyFromInternalEndpointDrops(t *testing.T) {
	t.Parallel()
	subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	root := contextfabric.SubjectRef{Kind: contextfabric.SubjectOrganization, CanonicalID: "organization-root", Label: "Organization"}
	other := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_other", Label: "Other"}
	isReserved := func(s contextfabric.SubjectRef) bool { return s.CanonicalID == "organization-root" }
	edges := []ResolvedEdge{
		testResolvedEdge("edge-self-loop", "RELATES_TO", subject, subject, 0, "evidence_self_loop_1234"),
		testResolvedEdge("edge-bookkeeping", "HAS_SUBJECT", root, other, 0, "evidence_bookkeeping_1234"),
	}
	result := AdmitEdges("org_1", edges, discoverOptions(), isReserved)
	if len(result.Paths) != 0 {
		t.Fatalf("result.Paths = %#v, want both edges excluded (unchanged behavior)", result.Paths)
	}
	if result.DroppedSelfLoopCount != 1 {
		t.Fatalf("DroppedSelfLoopCount = %d, want exactly 1 (the internal-bookkeeping drop must not inflate it)", result.DroppedSelfLoopCount)
	}
}

// teamCohortFrame is the minimal validated frame declaring a discovered TEAM
// cohort -- the kind the deleted matcher used to infer from a
// "teams_under_pressure" judgment.
func teamCohortFrame() *contextfabric.QuestionFrame {
	return &contextfabric.QuestionFrame{
		Goals: []contextfabric.InvestigationGoal{contextfabric.GoalAssessState},
		SubjectExpression: contextfabric.SubjectExpression{
			Kind:       contextfabric.SubjectExpressionDiscoveredKind,
			Discovered: &contextfabric.DiscoveredSetExpression{MemberKind: contextfabric.SubjectTeam},
		},
		Temporal: contextfabric.TemporalIntentCurrent,
		Version:  contextfabric.QuestionFrameVersion,
	}
}
