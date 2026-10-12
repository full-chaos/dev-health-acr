package contextfabric

import (
	"slices"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func narrowedGraphPath(id string, nodes []SubjectRef, edgeTo SubjectRef, ref string) RelationshipPath {
	return RelationshipPath{
		PathID: id, Nodes: nodes, WhyRelevant: "fixture", EvidenceRefIDs: []string{ref},
		Edges: []RelationshipEdge{{Type: contractsv1.ContextFabricRelationshipRelatedTo, From: nodes[0], To: edgeTo, EvidenceRefIDs: []string{ref}}},
	}
}

// A narrowed pass's graph input carries nothing that names a removed member:
// a path with the member as a node or an edge end, and a driver candidate that
// affects it, are dropped; a kept driver loses its citation of a dropped
// path; an evidence ref only dropped things carried is removed, and a ref a
// kept member, kept path, kept driver or retained fact still carries stays.
func TestTheNarrowedGraphInputDropsWhatNamesARemovedMember(t *testing.T) {
	team := SubjectRef{Kind: SubjectTeam, CanonicalID: "team_a", Label: "A"}
	keptSubject := SubjectRef{Kind: SubjectProject, CanonicalID: "project_kept", Label: "kept"}
	goneSubject := SubjectRef{Kind: SubjectProject, CanonicalID: "project_gone", Label: "gone"}
	kept := []CohortMember{{Subject: keptSubject, EvidenceRefIDs: []string{"ref_kept_member", "ref_shared"}}}
	removed := []CohortMember{{Subject: goneSubject, EvidenceRefIDs: []string{"ref_gone_member", "ref_shared", "ref_fact"}}}
	graph := GraphContext{
		Paths: []RelationshipPath{
			narrowedGraphPath("path_kept_one", []SubjectRef{team, keptSubject}, keptSubject, "ref_path_kept"),
			narrowedGraphPath("path_gone_node", []SubjectRef{team, goneSubject}, goneSubject, "ref_path_gone"),
			narrowedGraphPath("path_gone_edge", []SubjectRef{team, keptSubject}, goneSubject, "ref_path_edge"),
		},
		DriverCandidates: []DriverJudgment{
			{DriverID: "driver_gone", AffectedSubjects: []SubjectRef{goneSubject}, EvidenceRefIDs: []string{"ref_driver_gone"}},
			{DriverID: "driver_kept", AffectedSubjects: []SubjectRef{keptSubject}, PathIDs: []string{"path_kept_one", "path_gone_node"}, EvidenceRefIDs: []string{"ref_driver_kept"}},
		},
		EvidenceRefIDs: []string{"ref_kept_member", "ref_shared", "ref_gone_member", "ref_fact", "ref_path_kept", "ref_path_gone", "ref_path_edge", "ref_driver_gone", "ref_driver_kept", "ref_unrelated"},
	}
	facts := []CanonicalFact{{Subject: team, EvidenceRefIDs: []string{"ref_fact"}}}
	original := slices.Clone(graph.DriverCandidates[1].PathIDs)

	got := graphWithoutRemovedMembers(graph, removed, kept, facts)

	paths := []string{}
	for _, path := range got.Paths {
		paths = append(paths, path.PathID)
	}
	if !slices.Equal(paths, []string{"path_kept_one"}) {
		t.Errorf("paths = %v, want only the path that names no removed member", paths)
	}
	if len(got.DriverCandidates) != 1 || got.DriverCandidates[0].DriverID != "driver_kept" || !slices.Equal(got.DriverCandidates[0].PathIDs, []string{"path_kept_one"}) {
		t.Errorf("drivers = %+v, want driver_kept citing path_kept_one only", got.DriverCandidates)
	}
	wantRefs := []string{"ref_kept_member", "ref_shared", "ref_fact", "ref_path_kept", "ref_driver_kept", "ref_unrelated"}
	if !slices.Equal(got.EvidenceRefIDs, wantRefs) {
		t.Errorf("refs = %v, want %v", got.EvidenceRefIDs, wantRefs)
	}
	if !slices.Equal(graph.DriverCandidates[1].PathIDs, original) || len(graph.Paths) != 3 || len(graph.EvidenceRefIDs) != 10 {
		t.Errorf("the caller's graph was changed: %+v", graph)
	}
	if same := graphWithoutRemovedMembers(graph, nil, kept, facts); len(same.Paths) != 3 || len(same.DriverCandidates) != 2 || len(same.EvidenceRefIDs) != 10 {
		t.Errorf("nothing removed changed the graph: %+v", same)
	}
}

// The census correction restates every field that carries the served count --
// Served, the raw text, the label and the degraded reason equal to the old raw
// -- on a copy, for the cohort's own kind only.
func TestTheCensusCorrectionRestatesEveryFieldThatCarriesTheCount(t *testing.T) {
	declared, listed := 40, 13
	stale := KindCensusTruncatedReason(SubjectProject, declared, listed)
	detail := CoverageDetail{
		DetailID: "cov-graph-01", Source: "context-fabric:graph", Code: contractsv1.ContextFabricCoverageDetailKindCensusTruncated,
		Degrading: true, Kind: SubjectProject, Declared: &declared, Served: &listed, Raw: stale,
	}
	detail.Label = contractsv1.ComposeCoverageDetailLabel(detail)
	staleLabel := detail.Label
	other := CoverageDetail{DetailID: "cov-graph-02", Source: "context-fabric:graph", Code: contractsv1.ContextFabricCoverageDetailGraphExactNameCandidatesTruncated, Degrading: true, Raw: "exact_name_candidates_truncated"}
	coverage := Coverage{Details: []CoverageDetail{detail, other}, DegradedReasons: []string{stale, "exact_name_candidates_truncated"}}
	cohort := budgetStageCohort(6)

	got := correctKindCensusTruncatedCoverage(coverage, cohort)

	want := KindCensusTruncatedReason(SubjectProject, declared, 6)
	census := got.Details[0]
	if census.Served == nil || *census.Served != 6 || census.Raw != want || census.Label != contractsv1.ComposeCoverageDetailLabel(census) || census.Label == staleLabel {
		t.Errorf("census = served %v raw %q label %q, want 6, %q and the label composed over 6", census.Served, census.Raw, census.Label, want)
	}
	if !slices.Equal(got.DegradedReasons, []string{want, "exact_name_candidates_truncated"}) || got.Details[1].Raw != other.Raw {
		t.Errorf("reasons = %v, details[1] = %+v, want only the census reason restated", got.DegradedReasons, got.Details[1])
	}
	if *coverage.Details[0].Served != listed || coverage.Details[0].Raw != stale || coverage.DegradedReasons[0] != stale {
		t.Errorf("the caller's coverage was changed: %+v", coverage)
	}
	teams := budgetStageCohort(2)
	teams.Kind = SubjectTeam
	if other := correctKindCensusTruncatedCoverage(coverage, teams); other.Details[0].Raw != stale || other.DegradedReasons[0] != stale {
		t.Errorf("a cohort of another kind restated the census: %+v", other)
	}
	if none := correctKindCensusTruncatedCoverage(coverage, nil); *none.Details[0].Served != 0 || none.Details[0].Raw != KindCensusTruncatedReason(SubjectProject, declared, 0) {
		t.Errorf("no cohort: %+v, want served 0", none.Details[0])
	}
}
