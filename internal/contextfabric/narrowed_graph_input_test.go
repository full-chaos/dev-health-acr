package contextfabric

import (
	"reflect"
	"slices"
	"strings"
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
// path. An evidence ref a dropped member, path or driver carried is removed
// unless a holder left in the narrowed input carries it: a kept member, kept
// path, kept driver, retained fact or resolution candidate. A ref nothing
// dropped carried stays.
func TestTheNarrowedGraphInputDropsWhatNamesARemovedMember(t *testing.T) {
	team := SubjectRef{Kind: SubjectTeam, CanonicalID: "team_a", Label: "A"}
	keptSubject := SubjectRef{Kind: SubjectProject, CanonicalID: "project_kept", Label: "kept"}
	goneSubject := SubjectRef{Kind: SubjectProject, CanonicalID: "project_gone", Label: "gone"}
	kept := []CohortMember{{Subject: keptSubject, EvidenceRefIDs: []string{"ref_kept_member", "ref_shared"}}}
	removed := []CohortMember{{Subject: goneSubject, EvidenceRefIDs: []string{"ref_gone_member", "ref_shared", "ref_fact"}}}
	graph := GraphContext{
		Resolution: SubjectResolution{Candidates: []SubjectCandidate{{Subject: team, EvidenceRefIDs: []string{"ref_candidate"}}}},
		Cohort:     &Cohort{Kind: SubjectProject, Members: kept},
		Paths: []RelationshipPath{
			narrowedGraphPath("path_kept_one", []SubjectRef{team, keptSubject}, keptSubject, "ref_path_kept"),
			narrowedGraphPath("path_gone_node", []SubjectRef{team, goneSubject}, goneSubject, "ref_path_gone"),
			narrowedGraphPath("path_gone_edge", []SubjectRef{team, keptSubject}, goneSubject, "ref_path_edge"),
			narrowedGraphPath("path_gone_candidate", []SubjectRef{team, goneSubject}, goneSubject, "ref_candidate"),
		},
		DriverCandidates: []DriverJudgment{
			{DriverID: "driver_gone", AffectedSubjects: []SubjectRef{goneSubject}, EvidenceRefIDs: []string{"ref_driver_gone"}},
			{DriverID: "driver_kept", AffectedSubjects: []SubjectRef{keptSubject}, PathIDs: []string{"path_kept_one", "path_gone_node"}, EvidenceRefIDs: []string{"ref_driver_kept"}},
		},
		EvidenceRefIDs: []string{"ref_kept_member", "ref_shared", "ref_gone_member", "ref_fact", "ref_path_kept", "ref_path_gone", "ref_path_edge", "ref_candidate", "ref_driver_gone", "ref_driver_kept", "ref_unrelated"},
	}
	facts := []CanonicalFact{{Subject: team, EvidenceRefIDs: []string{"ref_fact"}}}
	original := slices.Clone(graph.DriverCandidates[1].PathIDs)

	got := graphWithoutRemovedMembers(graph, removed, facts)

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
	wantRefs := []string{"ref_kept_member", "ref_shared", "ref_fact", "ref_path_kept", "ref_candidate", "ref_driver_kept", "ref_unrelated"}
	if !slices.Equal(got.EvidenceRefIDs, wantRefs) {
		t.Errorf("refs = %v, want %v", got.EvidenceRefIDs, wantRefs)
	}
	if !slices.Equal(graph.DriverCandidates[1].PathIDs, original) || len(graph.Paths) != 4 || len(graph.EvidenceRefIDs) != 11 {
		t.Errorf("the caller's graph was changed: %+v", graph)
	}
	if same := graphWithoutRemovedMembers(graph, nil, facts); len(same.Paths) != 4 || len(same.DriverCandidates) != 2 || len(same.EvidenceRefIDs) != 11 {
		t.Errorf("nothing removed changed the graph: %+v", same)
	}
}

// keptInputRefs walks exactly the holders narrowedInputRefHolders names, and
// that list names every ref-bearing field the synthesis input types reach: a
// holder added to GraphContext, its cohort, paths, drivers or candidates, or
// to the fact bundle, fails here until it is walked.
func TestEveryEvidenceRefHolderOfTheSynthesisInputIsWalked(t *testing.T) {
	found := []string{}
	var walk func(reflect.Type, string, map[reflect.Type]bool)
	walk = func(tp reflect.Type, path string, seen map[reflect.Type]bool) {
		for tp.Kind() == reflect.Pointer {
			tp = tp.Elem()
		}
		for tp.Kind() == reflect.Slice || tp.Kind() == reflect.Array || tp.Kind() == reflect.Map {
			tp = tp.Elem()
			path += "[]"
			for tp.Kind() == reflect.Pointer {
				tp = tp.Elem()
			}
		}
		if tp.Kind() != reflect.Struct || seen[tp] {
			return
		}
		seen[tp] = true
		defer delete(seen, tp)
		for i := 0; i < tp.NumField(); i++ {
			field := tp.Field(i)
			if field.Type == reflect.TypeOf([]string(nil)) && strings.Contains(field.Name, "Ref") {
				found = append(found, path+"."+field.Name)
				continue
			}
			walk(field.Type, path+"."+field.Name, seen)
		}
	}
	walk(reflect.TypeOf(GraphContext{}), "Graph", map[reflect.Type]bool{})
	walk(reflect.TypeOf(CanonicalFactBundle{}), "Facts", map[reflect.Type]bool{})
	found = slices.DeleteFunc(found, func(path string) bool { return path == "Graph.EvidenceRefIDs" })
	slices.Sort(found)
	walked := slices.Sorted(slices.Values(narrowedInputRefHolders))
	if !slices.Equal(found, walked) {
		t.Fatalf("ref holders in the input types = %v, walked = %v", found, walked)
	}

	holder := []string{"ref_holder"}
	for _, row := range []struct {
		name  string
		graph GraphContext
		facts []CanonicalFact
	}{
		{"candidate", GraphContext{Resolution: SubjectResolution{Candidates: []SubjectCandidate{{EvidenceRefIDs: holder}}}}, nil},
		{"member", GraphContext{Cohort: &Cohort{Members: []CohortMember{{EvidenceRefIDs: holder}}}}, nil},
		{"path", GraphContext{Paths: []RelationshipPath{{EvidenceRefIDs: holder}}}, nil},
		{"edge", GraphContext{Paths: []RelationshipPath{{Edges: []RelationshipEdge{{EvidenceRefIDs: holder}}}}}, nil},
		{"driver", GraphContext{DriverCandidates: []DriverJudgment{{EvidenceRefIDs: holder}}}, nil},
		{"fact", GraphContext{}, []CanonicalFact{{EvidenceRefIDs: holder}}},
	} {
		if !keptInputRefs(row.graph, row.facts)["ref_holder"] {
			t.Errorf("%s: keptInputRefs does not walk this holder", row.name)
		}
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
