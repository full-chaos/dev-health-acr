package contextfabric

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The per-population read states, on the SERVED document.
//
// A grouped answer reads each kind for its members and for its groups, under
// the same source name, and the fold keeps the worse state. These pins say:
// every canonical-fact observation of each read reaches the served document as
// a fact_read_origin_state row naming its population; the rows are exactly the
// pre-fold trace line's observations; a failed group read still discloses its
// own; a read refused at reconcile serves none; and a turn with no group read
// carries no such row.

type originRow struct {
	source, state, origin string
	degrading             bool
}

func originRowsOf(coverage Coverage) []originRow {
	rows := make([]originRow, 0)
	for _, d := range coverage.Details {
		if d.Code != contractsv1.ContextFabricCoverageDetailFactReadOriginState {
			continue
		}
		rows = append(rows, originRow{d.Source, string(d.SourceState), string(d.OriginKind), d.Degrading})
	}
	sort.Slice(rows, func(i, j int) bool { return fmt.Sprint(rows[i]) < fmt.Sprint(rows[j]) })
	return rows
}

// TestEachReadsStateReachesTheServedDocumentByPopulation is the composed path:
// the SAME source in DIFFERENT states on the two reads, which is exactly what
// the fold erases.
func TestEachReadsStateReachesTheServedDocumentByPopulation(t *testing.T) {
	telemetry := &recordingTelemetry{}
	recorder := &groupReadRecorder{facts: func(request CanonicalFactRequest) CanonicalFactBundle {
		bundle := emptyFactBundle()
		for _, subject := range request.Subjects {
			if subject.Kind == SubjectTeam {
				bundle.Coverage.Sources = []SourceObservation{
					{Source: "canonical_fact:health", State: SourceNoData, Reason: "canonical fact capability returned no_data"},
					{Source: "canonical_fact:workload", State: SourceStale, Reason: "canonical fact capability returned stale"},
				}
				return bundle
			}
		}
		bundle.Facts = groupReadMemberFacts()
		bundle.Coverage.Sources = []SourceObservation{
			{Source: "canonical_fact:health", State: SourceAvailable},
			{Source: "canonical_fact:workload", State: SourceAvailable},
		}
		return bundle
	}}
	engine, request := groupReadEngineFixture(t, telemetry, recorder)
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	got := originRowsOf(result.Coverage)
	t.Logf("served origin rows: %v", got)
	for _, d := range result.Coverage.Details {
		if d.Code == contractsv1.ContextFabricCoverageDetailFactReadOriginState {
			t.Logf("served detail %s label=%q", d.DetailID, d.Label)
		}
	}
	want := []originRow{
		{"canonical_fact:health", string(SourceAvailable), string(SubjectProject), false},
		{"canonical_fact:health", string(SourceNoData), string(SubjectTeam), false},
		{"canonical_fact:workload", string(SourceAvailable), string(SubjectProject), false},
		{"canonical_fact:workload", string(SourceStale), string(SubjectTeam), false},
	}
	sort.Slice(want, func(i, j int) bool { return fmt.Sprint(want[i]) < fmt.Sprint(want[j]) })
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("served origin rows = %v, want %v -- each read's state for each kind, named by its population", got, want)
	}

	// THE JOIN: the served rows are the pre-fold line's observations, one for
	// one. A row the trace does not show, or a trace observation the document
	// does not carry, is a disagreement between the two places a reader looks.
	fromTrace := make([]originRow, 0, len(telemetry.groupReadCoverageStates))
	for _, event := range telemetry.groupReadCoverageStates {
		origin := string(SubjectProject)
		if event.Read == GroupReadArmGroup {
			origin = string(event.GroupKind)
		}
		fromTrace = append(fromTrace, originRow{event.Source, string(event.State), origin, false})
	}
	sort.Slice(fromTrace, func(i, j int) bool { return fmt.Sprint(fromTrace[i]) < fmt.Sprint(fromTrace[j]) })
	t.Logf("pre-fold trace observations: %v", fromTrace)
	if fmt.Sprint(fromTrace) != fmt.Sprint(got) {
		t.Errorf("served rows %v do not equal the pre-fold line's observations %v", got, fromTrace)
	}

	// The fold still folds: the served SOURCE for health reads the worse state,
	// and the origin rows are what say which population it was.
	for _, source := range result.Coverage.Sources {
		if source.Source == "canonical_fact:health" && source.State != SourceNoData {
			t.Errorf("folded health source state = %q, want %q", source.State, SourceNoData)
		}
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("served result fails the write-path contract: %v", err)
	}
}

// TestAFailedGroupReadDisclosesItsOwnStateByPopulation is the failed path: the
// group read errored, its bundle is not composed, and its observation is still
// named on the served document.
func TestAFailedGroupReadDisclosesItsOwnStateByPopulation(t *testing.T) {
	recorder := groupReadServing()
	recorder.facts = func(request CanonicalFactRequest) CanonicalFactBundle {
		bundle := emptyFactBundle()
		bundle.Facts = groupReadMemberFacts()
		bundle.Coverage.Sources = []SourceObservation{{Source: "canonical_fact:health", State: SourceAvailable}}
		return bundle
	}
	twoMembers := []CohortMember{
		{Subject: SubjectRef{Kind: SubjectProject, CanonicalID: "project_a", Label: "project_a"}, Rank: 1, InclusionReasons: []string{"matched"}},
		{Subject: SubjectRef{Kind: SubjectProject, CanonicalID: "project_b", Label: "project_b"}, Rank: 2, InclusionReasons: []string{"matched"}},
	}
	engine, request := groupReadEngineFixtureFull(t, &recordingTelemetry{}, &partialFailingReader{inner: recorder}, twoMembers, nil, SubjectProject, nil, nil)
	result, err := engine.Investigate(canonicalRequestContext(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	got := originRowsOf(result.Coverage)
	t.Logf("served origin rows after a failed group read: %v", got)
	want := fmt.Sprint([]originRow{
		{"canonical_fact:health", string(SourceAvailable), string(SubjectProject), false},
		{"canonical_fact:health", string(SourceUnavailable), string(SubjectTeam), false},
	})
	if fmt.Sprint(got) != want {
		t.Fatalf("served origin rows = %v, want %s", got, want)
	}
}

// TestAGroupReadRefusedAtReconcileServesNoOriginRows is the metadata-conflict
// path: nothing of the group read was composed, so the document must not
// describe that read.
func TestAGroupReadRefusedAtReconcileServesNoOriginRows(t *testing.T) {
	recorder := &groupReadRecorder{facts: func(request CanonicalFactRequest) CanonicalFactBundle {
		bundle := emptyFactBundle()
		for _, subject := range request.Subjects {
			if subject.Kind == SubjectTeam {
				bundle.Versions = map[FactKind]string{FactHealth: "health-v4"}
				bundle.Coverage.Sources = []SourceObservation{{Source: "canonical_fact:health", State: SourceAvailable}}
				return bundle
			}
		}
		bundle.Facts = groupReadMemberFacts()
		bundle.Versions = map[FactKind]string{FactHealth: "health-v3"}
		bundle.Coverage.Sources = []SourceObservation{{Source: "canonical_fact:health", State: SourceAvailable}}
		return bundle
	}}
	logs := captureEngineLogger(t)
	engine, request := groupReadEngineFixture(t, logs.telemetry, recorder)
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	line := cohortGroupReadLine(t, logs)
	t.Logf("group read refusal=%v; served origin rows: %v", line["group_read_refusal"], originRowsOf(result.Coverage))
	if line["group_read_refusal"] != string(GroupReadRefusalMetadataConflict) {
		t.Fatalf("refusal = %v -- the fixture must reach the metadata_conflict exit", line["group_read_refusal"])
	}
	if rows := originRowsOf(result.Coverage); len(rows) != 0 {
		t.Errorf("a group read refused at reconcile served %d origin row(s): %v", len(rows), rows)
	}
}

// TestATurnWithNoGroupReadServesNoOriginRows is the control: an ungrouped
// answer reads once, and its coverage carries no origin row.
func TestATurnWithNoGroupReadServesNoOriginRows(t *testing.T) {
	coverage := MergeCoverage("org_1", Coverage{Sources: []SourceObservation{{Source: "canonical_fact:health", State: SourceAvailable}}})
	if rows := originRowsOf(coverage); len(rows) != 0 {
		t.Fatalf("a single read's coverage carries origin rows: %v", rows)
	}
	recorder := &groupReadRecorder{facts: func(CanonicalFactRequest) CanonicalFactBundle {
		bundle := emptyFactBundle()
		// No group-naming column anywhere, so no group axis is built and no
		// group read is issued.
		bundle.Facts = []CanonicalFact{ungroupableFact("project_a"), ungroupableFact("project_b")}
		bundle.Coverage.Sources = []SourceObservation{{Source: "canonical_fact:metrics", State: SourceAvailable}}
		return bundle
	}}
	engine, request := groupReadEngineFixture(t, &recordingTelemetry{}, recorder)
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if len(recorder.requests) != 1 {
		t.Fatalf("CONTROL BROKEN: %d fact-service calls, want 1 -- this turn must not issue a group read", len(recorder.requests))
	}
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	t.Logf("ungrouped served origin rows: %v", originRowsOf(result.Coverage))
	if rows := originRowsOf(result.Coverage); len(rows) != 0 {
		t.Errorf("a turn with no group read served %d origin row(s): %v", len(rows), rows)
	}
}

// TestTheNonDegradingDedupeKeyKeepsRowsThatSayDifferentThings is the class
// sweep for the widened dedupe key, over every producer of non-degrading
// details: two rows that differ ONLY in one identity field must both survive,
// and two rows identical in every field must still collapse.
func TestTheNonDegradingDedupeKeyKeepsRowsThatSayDifferentThings(t *testing.T) {
	registryRow := func(state SourceState, origin SubjectKind, raw string) CoverageDetail {
		d := CoverageDetail{DetailID: "cov-fact-01", Source: "canonical_fact:health", Code: contractsv1.ContextFabricCoverageDetailFactProviderReported,
			FactKind: FactHealth, SourceState: state, Raw: raw}
		if origin != "" {
			d.Code = contractsv1.ContextFabricCoverageDetailFactScopeUnexpanded
			d.OriginKind, d.ScopeOutcome, d.Policy, d.Basis = origin, "attempted_empty", "none", "direct"
		}
		d.Label = contractsv1.ComposeCoverageDetailLabel(d)
		return d
	}
	graphRow := func() CoverageDetail {
		count := 2
		d := CoverageDetail{DetailID: "cov-graph-01", Source: "context-fabric:graph-validity-windows", Code: contractsv1.ContextFabricCoverageDetailGraphValidityUnbounded,
			Count: &count, Raw: "validity_unbounded:2"}
		d.Label = contractsv1.ComposeCoverageDetailLabel(d)
		return d
	}
	originRowFor := func(state SourceState, origin SubjectKind) CoverageDetail {
		return readOriginStateCoverage(Coverage{Sources: []SourceObservation{{Source: "canonical_fact:health", State: state}}}, Coverage{}, origin, "").Details[0]
	}
	cells := []struct {
		producer, shape string
		rows            []CoverageDetail
		want            int
	}{
		{"fact registry (provider reported)", "identical rows", []CoverageDetail{registryRow(SourceNoData, "", "r"), registryRow(SourceNoData, "", "r")}, 1},
		{"fact registry (provider reported)", "same raw, different state", []CoverageDetail{registryRow(SourceNoData, "", "r"), registryRow(SourceNotApplicable, "", "r")}, 2},
		{"fact registry (scope unexpanded)", "identical rows", []CoverageDetail{registryRow(SourceNoData, SubjectTeam, "r"), registryRow(SourceNoData, SubjectTeam, "r")}, 1},
		{"fact registry (scope unexpanded)", "same raw, different origin kind", []CoverageDetail{registryRow(SourceNoData, SubjectTeam, "r"), registryRow(SourceNoData, SubjectProject, "r")}, 2},
		{"graph reader (validity unbounded)", "identical rows", []CoverageDetail{graphRow(), graphRow()}, 1},
		{"origin state", "identical rows", []CoverageDetail{originRowFor(SourceAvailable, SubjectProject), originRowFor(SourceAvailable, SubjectProject)}, 1},
		{"origin state", "same kind, member vs group", []CoverageDetail{originRowFor(SourceAvailable, SubjectProject), originRowFor(SourceAvailable, SubjectTeam)}, 2},
		{"origin state", "same kind and origin, different state", []CoverageDetail{originRowFor(SourceAvailable, SubjectTeam), originRowFor(SourceNoData, SubjectTeam)}, 2},
	}
	for _, c := range cells {
		got := len(mergeCoverageDetails(c.rows, nil, "org_1"))
		t.Logf("CELL producer=%s shape=%s -> %d row(s)", c.producer, c.shape, got)
		if got != c.want {
			t.Errorf("%s / %s: %d row(s) after merge, want %d", c.producer, c.shape, got, c.want)
		}
	}
}

// TestTheOriginRowsFitTheCoverageBoundAtTheVocabularyMaximum is the ceiling
// pin: the largest grouped coverage the vocabularies allow -- every fact kind
// on both reads, each observation carrying its own non-available detail, plus
// every graph detail -- still fits the contract's coverage entry bound. A
// vocabulary that grows past it fails HERE, not on a served turn.
func TestTheOriginRowsFitTheCoverageBoundAtTheVocabularyMaximum(t *testing.T) {
	var member, group CanonicalFactBundle
	member.Coverage, group.Coverage = Coverage{}, Coverage{}
	for _, kind := range contractsv1.ContextFabricFactKindVocabulary() {
		appendFactCoverage(&member, FactKind(kind), SourceNoData, nil, "", "member read returned no_data", coverageDetailSpec{})
		appendFactCoverage(&group, FactKind(kind), SourceStale, nil, "", "group read returned stale", coverageDetailSpec{})
	}
	graph := Coverage{}
	for i, code := range []contractsv1.ContextFabricCoverageDetailCode{
		contractsv1.ContextFabricCoverageDetailGraphEndpointLookupFailed,
		contractsv1.ContextFabricCoverageDetailGraphExactNameCandidatesTruncated,
		contractsv1.ContextFabricCoverageDetailGraphCohortDeniedByAuthorization,
		contractsv1.ContextFabricCoverageDetailGraphUnknownRelationshipType,
		contractsv1.ContextFabricCoverageDetailGraphValidityUnbounded,
	} {
		count := i + 1
		d := CoverageDetail{DetailID: fmt.Sprintf("cov-graph-%02d", i+1), Source: "context-fabric:graph", Code: code, Raw: fmt.Sprintf("graph:%s", code)}
		if code != contractsv1.ContextFabricCoverageDetailGraphExactNameCandidatesTruncated {
			d.Count = &count
		}
		d.Degrading = code != contractsv1.ContextFabricCoverageDetailGraphValidityUnbounded
		d.Label = contractsv1.ComposeCoverageDetailLabel(d)
		graph.Details = append(graph.Details, d)
		if d.Degrading {
			graph.DegradedReasons = append(graph.DegradedReasons, d.Raw)
		}
	}
	origins := readOriginStateCoverage(member.Coverage, group.Coverage, SubjectProject, SubjectTeam)
	served := MergeCoverage("org_1", member.Coverage, group.Coverage, graph, origins)

	byCode := map[contractsv1.ContextFabricCoverageDetailCode]int{}
	for _, d := range served.Details {
		byCode[d.Code]++
	}
	codes := make([]string, 0, len(byCode))
	for code, n := range byCode {
		codes = append(codes, fmt.Sprintf("%s=%d", code, n))
	}
	sort.Strings(codes)
	const bound = 100 // contextFabricWriteBounds.coverageEntries; the served write below is the authority
	t.Logf("vocabulary-maximal grouped coverage: %d fact kinds, %d details served (%s), bound %d",
		contractsv1.ContextFabricFactKindCount, len(served.Details), strings.Join(codes, " "), bound)
	if want := 2 * contractsv1.ContextFabricFactKindCount; byCode[contractsv1.ContextFabricCoverageDetailFactReadOriginState] != want {
		t.Fatalf("origin rows = %d, want %d (every kind on both reads)", byCode[contractsv1.ContextFabricCoverageDetailFactReadOriginState], want)
	}
	if len(served.Details) > bound {
		t.Fatalf("%d details exceed the coverage entry bound %d -- a grouped turn at the vocabulary maximum would fail its write", len(served.Details), bound)
	}
	if err := served.Validate(); err != nil {
		t.Fatalf("the vocabulary-maximal served coverage fails the write-path contract: %v", err)
	}
}

// A DISCLOSURE MUST NOT RENAME THE CAUSE OF A LOSS.
//
// evaluateReadRequirement carries the coverage layer's own code as the
// requirement row's cause, keyed by fact kind and last-writer-wins. A
// never-degrading code describes a read that happened; it does not say what
// cost the read its evidence. fact_read_origin_state made the difference
// load-bearing: it is emitted per origin for EVERY kind, so before the guard
// it was the last detail written for every kind and a row narrowed by a
// provider failure named the disclosure instead.
//
// SWEPT OVER THE WHOLE CLASS, enumerated from the contract rather than
// listed here, so a never-degrading code added later is covered the day it is
// declared. Each cell puts the degrading detail FIRST and the disclosure
// SECOND, which is the order that fails without the guard.
func TestANonDegradingDisclosureNeverBecomesARequirementRowsCause(t *testing.T) {
	const kind = FactKind("health")
	requirement := contractsv1.ContextFabricPlanRequirement{
		Requirement: "state/member/project",
		Obligation:  "state",
		FactKinds:   []contractsv1.ContextFabricFactKind{kind},
	}
	coverageFor := func(second contractsv1.ContextFabricCoverageDetailCode) Coverage {
		return Coverage{
			Sources: []SourceObservation{{Source: canonicalFactSourcePrefix + string(kind), State: SourceUnavailable}},
			Details: []CoverageDetail{
				{
					DetailID: "cov-01", Source: canonicalFactSourcePrefix + string(kind),
					Code: contractsv1.ContextFabricCoverageDetailFactProviderReported, FactKind: kind,
				},
				{
					DetailID: "cov-02", Source: canonicalFactSourcePrefix + string(kind),
					Code: second, FactKind: kind,
				},
			},
		}
	}

	swept := 0
	for _, code := range contractsv1.ContextFabricCoverageDetailCodeVocabulary() {
		if contractsv1.ContextFabricCoverageDetailCodeMayDegrade(code) {
			continue
		}
		swept++
		evidence := evaluateReadRequirement(requirement, coverageFor(code))
		if evidence.Cause != contractsv1.ContextFabricCoverageDetailFactProviderReported {
			t.Errorf("second detail %q: cause = %q, want %q -- a code that can never degrade must not name the cause of a loss",
				code, evidence.Cause, contractsv1.ContextFabricCoverageDetailFactProviderReported)
		}
	}
	if swept < 3 {
		t.Fatalf("swept %d never-degrading codes, want every member of the class (at least fact_pruned, graph_validity_unbounded, fact_read_origin_state)", swept)
	}

	// THE DISCRIMINATING CONTROL: a code that CAN degrade still wins the
	// last-writer-wins map, so the sweep above is proving the guard and not
	// merely that the first detail is always kept.
	control := evaluateReadRequirement(requirement, coverageFor(contractsv1.ContextFabricCoverageDetailFactNarrowed))
	if control.Cause != contractsv1.ContextFabricCoverageDetailFactNarrowed {
		t.Fatalf("control: cause = %q, want %q -- a degrading code must still be carried", control.Cause, contractsv1.ContextFabricCoverageDetailFactNarrowed)
	}
}
