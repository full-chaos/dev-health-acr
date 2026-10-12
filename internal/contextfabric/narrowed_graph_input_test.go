package contextfabric

import (
	"encoding/json"
	"maps"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// A narrowed pass's graph input carries nothing that names a removed member:
// a path with the member as a node or as either edge end is dropped, and so is
// a driver candidate that affects the member or cites a dropped path (the
// shape graphrank's edge admission builds: the edge's From affected, its To
// named in the title). An evidence ref a dropped member, path, edge or driver
// carried is removed unless a holder left in the narrowed input carries it: a
// kept member, kept path, kept driver, retained fact or resolution candidate.
// A ref nothing dropped carried stays.
func TestTheNarrowedGraphInputDropsWhatNamesARemovedMember(t *testing.T) {
	team := SubjectRef{Kind: SubjectTeam, CanonicalID: "team_a", Label: "A"}
	keptSubject := SubjectRef{Kind: SubjectProject, CanonicalID: "project_kept", Label: "kept"}
	goneSubject := SubjectRef{Kind: SubjectProject, CanonicalID: "project_gone", Label: "gone"}
	blocker := SubjectRef{Kind: SubjectWorkItem, CanonicalID: "work_item_1", Label: "PLAT-1"}
	kept := []CohortMember{{Subject: keptSubject, EvidenceRefIDs: []string{"ref_kept_member", "ref_shared"}}}
	removed := []CohortMember{{Subject: goneSubject, EvidenceRefIDs: []string{"ref_gone_member", "ref_shared", "ref_fact"}}}
	path := func(id string, nodes []SubjectRef, from, to SubjectRef, pathRef, edgeRef string) RelationshipPath {
		return RelationshipPath{
			PathID: id, Nodes: nodes, WhyRelevant: "fixture", EvidenceRefIDs: []string{pathRef},
			Edges: []RelationshipEdge{{Type: contractsv1.ContextFabricRelationshipRelatedTo, From: from, To: to, EvidenceRefIDs: []string{edgeRef}}},
		}
	}
	graph := GraphContext{
		Resolution: SubjectResolution{Candidates: []SubjectCandidate{{Subject: team, EvidenceRefIDs: []string{"ref_candidate"}}}},
		Cohort:     &Cohort{Kind: SubjectProject, Members: kept},
		Paths: []RelationshipPath{
			path("path_kept_one", []SubjectRef{team, keptSubject}, team, keptSubject, "ref_path_kept", "ref_edge_kept"),
			path("path_gone_node_only", []SubjectRef{team, goneSubject}, team, keptSubject, "ref_path_node", "ref_edge_node"),
			path("path_gone_edge_to", []SubjectRef{team, keptSubject}, team, goneSubject, "ref_path_to", "ref_edge_to"),
			path("path_gone_edge_from", []SubjectRef{team, keptSubject}, goneSubject, keptSubject, "ref_path_from", "ref_edge_from"),
			path("path_blocks_gone", []SubjectRef{blocker, goneSubject}, blocker, goneSubject, "ref_candidate", "ref_edge_blocks"),
		},
		DriverCandidates: []DriverJudgment{
			{DriverID: "driver_affects_gone", AffectedSubjects: []SubjectRef{goneSubject}, EvidenceRefIDs: []string{"ref_driver_gone"}},
			{DriverID: "driver_blocks_gone", Title: "Blocks: gone", Summary: "PLAT-1 blocks gone.", AffectedSubjects: []SubjectRef{blocker}, PathIDs: []string{"path_blocks_gone"}, EvidenceRefIDs: []string{"ref_driver_blocks"}},
			{DriverID: "driver_kept", AffectedSubjects: []SubjectRef{keptSubject}, PathIDs: []string{"path_kept_one"}, EvidenceRefIDs: []string{"ref_driver_kept"}},
		},
		EvidenceRefIDs: []string{
			"ref_kept_member", "ref_shared", "ref_gone_member", "ref_fact", "ref_path_kept", "ref_edge_kept",
			"ref_path_node", "ref_edge_node", "ref_path_to", "ref_edge_to", "ref_path_from", "ref_edge_from",
			"ref_candidate", "ref_edge_blocks", "ref_driver_gone", "ref_driver_blocks", "ref_driver_kept", "ref_unrelated",
		},
	}
	facts := []CanonicalFact{{Subject: team, EvidenceRefIDs: []string{"ref_fact"}}}

	got := graphWithoutRemovedMembers(graph, removed, facts)

	paths := []string{}
	for _, path := range got.Paths {
		paths = append(paths, path.PathID)
	}
	if !slices.Equal(paths, []string{"path_kept_one"}) {
		t.Errorf("paths = %v, want only the path that names no removed member (node, edge to, edge from)", paths)
	}
	drivers := []string{}
	for _, driver := range got.DriverCandidates {
		drivers = append(drivers, driver.DriverID)
	}
	if !slices.Equal(drivers, []string{"driver_kept"}) {
		t.Errorf("drivers = %v, want driver_kept only: a driver affecting the removed member or citing a dropped path goes", drivers)
	}
	wantRefs := []string{"ref_kept_member", "ref_shared", "ref_fact", "ref_path_kept", "ref_edge_kept", "ref_candidate", "ref_driver_kept", "ref_unrelated"}
	if !slices.Equal(got.EvidenceRefIDs, wantRefs) {
		t.Errorf("refs = %v, want %v", got.EvidenceRefIDs, wantRefs)
	}
	if len(graph.Paths) != 5 || len(graph.DriverCandidates) != 3 || len(graph.EvidenceRefIDs) != 18 {
		t.Errorf("the caller's graph was changed: %+v", graph)
	}
	if same := graphWithoutRemovedMembers(graph, nil, facts); len(same.Paths) != 5 || len(same.DriverCandidates) != 3 || len(same.EvidenceRefIDs) != 18 {
		t.Errorf("nothing removed changed the graph: %+v", same)
	}
}

// The holder census covers the WHOLE synthesis input: every top-level
// SynthesisInput field is a walked root or a named exclusion, and every field
// at any depth that can carry an evidence ref (a string list, string map or
// string named for a ref or evidence, raw JSON, an interface) is a holder
// keptInputRefs walks or a named exclusion with its reason. A field added to
// the input fails here until it is accounted for, and each walked holder is
// shown to be walked.
func TestEveryEvidenceRefHolderOfTheSynthesisInputIsWalked(t *testing.T) {
	inputType := reflect.TypeOf(SynthesisInput{})
	roots := []string{}
	for i := 0; i < inputType.NumField(); i++ {
		roots = append(roots, inputType.Field(i).Name)
	}
	slices.Sort(roots)
	if named := slices.Sorted(maps.Keys(synthesisInputRefRoots)); !slices.Equal(roots, named) {
		t.Fatalf("SynthesisInput fields = %v, accounted for = %v", roots, named)
	}

	refName := regexp.MustCompile(`(?i)ref|evidence`)
	rawJSON := reflect.TypeOf(json.RawMessage(nil))
	stringish := func(tp reflect.Type) bool {
		switch tp.Kind() {
		case reflect.String:
			return true
		case reflect.Slice, reflect.Array, reflect.Map:
			return tp.Elem().Kind() == reflect.String || ((tp.Elem().Kind() == reflect.Slice || tp.Elem().Kind() == reflect.Array) && tp.Elem().Elem().Kind() == reflect.String)
		}
		return false
	}
	found := []string{}
	var walk func(reflect.Type, string, map[reflect.Type]bool)
	walk = func(tp reflect.Type, path string, seen map[reflect.Type]bool) {
		for {
			switch tp.Kind() {
			case reflect.Pointer:
				tp = tp.Elem()
				continue
			case reflect.Slice, reflect.Array, reflect.Map:
				if tp == rawJSON || stringish(tp) {
					return
				}
				tp = tp.Elem()
				path += "[]"
				continue
			}
			break
		}
		if tp.Kind() != reflect.Struct || seen[tp] {
			return
		}
		seen[tp] = true
		defer delete(seen, tp)
		for i := 0; i < tp.NumField(); i++ {
			field := tp.Field(i)
			fieldPath := strings.TrimPrefix(path+"."+field.Name, ".")
			fieldType := field.Type
			for fieldType.Kind() == reflect.Pointer {
				fieldType = fieldType.Elem()
			}
			switch {
			case fieldType == rawJSON || fieldType.Kind() == reflect.Interface:
				found = append(found, fieldPath)
			case stringish(fieldType) && refName.MatchString(field.Name):
				found = append(found, fieldPath)
			default:
				walk(field.Type, fieldPath, seen)
			}
		}
	}
	walk(inputType, "", map[reflect.Type]bool{})
	slices.Sort(found)
	accounted := append(slices.Clone(narrowedInputRefHolders), slices.Collect(maps.Keys(narrowedInputRefExclusions))...)
	slices.Sort(accounted)
	if !slices.Equal(found, accounted) {
		t.Fatalf("ref-carrying fields of SynthesisInput = %v\nwalked or excluded = %v", found, accounted)
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
