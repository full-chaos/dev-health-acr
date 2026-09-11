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
	// NEVER INDEX WHAT THE THING UNDER TEST PRODUCED WITHOUT CHECKING IT
	// FIRST. This helper used to take `.Details[0]` directly, and a change
	// that makes the producer emit nothing then PANICS the whole test binary
	// rather than failing this one test -- the remaining tests never run, so
	// a mutation battery reads the truncated run as a harness error and
	// scores no kill at all for a defect these pins do catch. Four arms were
	// lost that way. A helper asserts, and the failure stays local.
	originRowFor := func(state SourceState, origin SubjectKind) CoverageDetail {
		t.Helper()
		rows := readOriginStateCoverage(Coverage{Sources: []SourceObservation{{Source: "canonical_fact:health", State: state}}}, Coverage{}, origin, "").Details
		if len(rows) != 1 {
			t.Fatalf("the producer served %d rows for one canonical observation rooted on %q in state %q, want 1", len(rows), origin, state)
		}
		return rows[0]
	}
	sharedSourceRow := func(kind FactKind) CoverageDetail {
		d := CoverageDetail{DetailID: "cov-origin-01", Source: "canonical_fact:health",
			Code:     contractsv1.ContextFabricCoverageDetailFactReadOriginState,
			FactKind: kind, SourceState: SourceAvailable, OriginKind: SubjectTeam}
		d.Label = contractsv1.ComposeCoverageDetailLabel(d)
		return d
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
		// FACT KIND ALONE. The three cells above vary state and origin; none
		// varies ONLY the kind, because an origin row's Source encodes its
		// kind and the producer can never mint two. The dedupe key is a
		// merge-layer rule over whatever reaches it, so the pair is built by
		// hand: two rows identical in source, code, raw, state and origin,
		// differing in fact_kind alone. Without fact_kind in the key they
		// collapse, and a battery arm that drops it survives.
		{"origin state", "same source and origin, different fact kind", []CoverageDetail{sharedSourceRow(FactHealth), sharedSourceRow(FactWorkload)}, 2},
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

// WHAT THE PRODUCER REFUSES TO SERVE.
//
// readOriginStateCoverage mints rows for the served document, and a row it
// should not mint is worse than a missing one: an unrooted read would name an
// empty population, a graph observation would be disclosed as if it were a
// fact kind, and a row the contract refuses would fail the WHOLE
// investigation over a disclosure. Each clause here is a separate guard in
// that function, executed on its own.
func TestTheProducerServesOnlyRowsItCanStandBehind(t *testing.T) {
	canonical := Coverage{Sources: []SourceObservation{
		{Source: "canonical_fact:health", State: SourceAvailable},
		{Source: "canonical_fact:workload", State: SourceNoData},
	}}

	t.Run("a read with no root kind serves nothing", func(t *testing.T) {
		// Both origins empty: the member read has no plan member kind and no
		// cohort kind, the group read no requested group kind. Rows would
		// carry origin_kind="", which the contract refuses -- so the guard
		// returns before minting any.
		got := readOriginStateCoverage(canonical, canonical, "", "")
		if len(got.Details) != 0 {
			t.Fatalf("rows = %d, want 0 -- an unrooted read must disclose nothing, not a row with an empty population", len(got.Details))
		}
		// And one-sided: only the rooted read discloses.
		half := readOriginStateCoverage(canonical, canonical, SubjectProject, "")
		if len(half.Details) != 2 {
			t.Fatalf("rooted-member-only rows = %d, want 2 (the member read's two observations, and none from the unrooted group read)", len(half.Details))
		}
		for _, d := range half.Details {
			if d.OriginKind != SubjectProject {
				t.Errorf("row %s carries origin %q, want %q -- the unrooted read leaked a row", d.DetailID, d.OriginKind, SubjectProject)
			}
		}
	})

	t.Run("an observation that names no fact kind is skipped", func(t *testing.T) {
		mixed := Coverage{Sources: []SourceObservation{
			{Source: "canonical_fact:health", State: SourceAvailable},
			{Source: "context-fabric:graph", State: SourceUnavailable},
			{Source: "context-fabric:graph-validity-windows", State: SourceAvailable},
			{Source: "canonical_fact:", State: SourceAvailable},
		}}
		got := readOriginStateCoverage(mixed, Coverage{}, SubjectProject, "")
		if len(got.Details) != 1 {
			t.Fatalf("rows = %d, want 1 -- only the canonical-fact observation is a fact read", len(got.Details))
		}
		if got.Details[0].FactKind != FactHealth {
			t.Errorf("row fact kind = %q, want %q -- a graph observation was disclosed as a fact kind", got.Details[0].FactKind, FactHealth)
		}
	})

	t.Run("a row the contract would refuse is skipped, not served", func(t *testing.T) {
		// An observation with no state. The code requires source_state, so
		// the minted row fails Validate and must be dropped -- serving it
		// would make the investigation's own write fail.
		refused := Coverage{Sources: []SourceObservation{
			{Source: "canonical_fact:health", State: ""},
			{Source: "canonical_fact:workload", State: SourceAvailable},
		}}
		got := readOriginStateCoverage(refused, Coverage{}, SubjectProject, "")
		if len(got.Details) != 1 {
			t.Fatalf("rows = %d, want 1 -- the stateless observation must be skipped", len(got.Details))
		}
		if got.Details[0].FactKind != FactWorkload {
			t.Errorf("surviving row is %q, want the workload row", got.Details[0].FactKind)
		}
		for _, d := range got.Details {
			if err := d.Validate(); err != nil {
				t.Errorf("row %s reached the document and fails the contract: %v", d.DetailID, err)
			}
		}
	})

	t.Run("every row carries its own detail id", func(t *testing.T) {
		got := readOriginStateCoverage(canonical, canonical, SubjectProject, SubjectTeam)
		if len(got.Details) != 4 {
			t.Fatalf("rows = %d, want 4 (two observations on each of two reads)", len(got.Details))
		}
		seen := map[string]int{}
		for _, d := range got.Details {
			seen[d.DetailID]++
		}
		if len(seen) != len(got.Details) {
			t.Fatalf("detail ids %v over %d rows -- ids collide, so a disclosure or a phrasing write lands on the wrong row", seen, len(got.Details))
		}
	})
}

// THE MEMBER READ'S ORIGIN IS THE PLAN'S MEMBER KIND WHEN THE PLAN HAS ONE.
//
// The plan's member kind and the cohort's kind are different facts and they
// disagree on exactly the turns this row exists for -- a cohort discovered as
// teams under a plan whose members are projects. Taking the cohort's kind
// there would label the member read with the group's population.
func TestTheMemberReadsOriginIsThePlansMemberKindWhenItHasOne(t *testing.T) {
	cohortOfTeams := &Cohort{Kind: SubjectTeam}
	cases := []struct {
		name   string
		plan   AnswerPlan
		cohort *Cohort
		want   SubjectKind
	}{
		{"plan names a member kind that differs from the cohort's", AnswerPlan{MemberKind: SubjectProject}, cohortOfTeams, SubjectProject},
		{"plan names one and the cohort agrees", AnswerPlan{MemberKind: SubjectTeam}, cohortOfTeams, SubjectTeam},
		{"plan names none, so the cohort's own kind stands in", AnswerPlan{}, cohortOfTeams, SubjectTeam},
		{"plan names none and there is no cohort", AnswerPlan{}, nil, ""},
		{"neither names one", AnswerPlan{}, &Cohort{}, ""},
	}
	for _, c := range cases {
		got := originMemberKind(c.plan, c.cohort)
		t.Logf("CELL %s -> %q", c.name, got)
		if got != c.want {
			t.Errorf("%s: origin = %q, want %q", c.name, got, c.want)
		}
	}
}

// THE SERVED ORDER IS DETERMINISTIC FOR ROWS THE WIDENED KEY KEEPS APART.
//
// Widening the dedupe key means rows that used to collapse now all survive,
// and `sort.Slice` is NOT stable: without a tiebreak past Raw, their served
// order is whatever order they happened to arrive in. Two turns with the same
// evidence would then serve two different documents. The fixture varies ONLY
// the fact kind, so the fact-kind clause is the one deciding the order.
func TestTheServedOrderOfOriginRowsDoesNotDependOnArrivalOrder(t *testing.T) {
	row := func(kind FactKind) CoverageDetail {
		d := CoverageDetail{DetailID: "cov-origin-01", Source: "canonical_fact:shared",
			Code:     contractsv1.ContextFabricCoverageDetailFactReadOriginState,
			FactKind: kind, SourceState: SourceAvailable, OriginKind: SubjectTeam}
		d.Label = contractsv1.ComposeCoverageDetailLabel(d)
		return d
	}
	kinds := []FactKind{FactWorkload, FactHealth, FactMetrics, FactReadiness}
	order := func(in []FactKind) []string {
		details := make([]CoverageDetail, 0, len(in))
		for _, k := range in {
			details = append(details, row(k))
		}
		merged := mergeCoverageDetails(details, nil, "org_1")
		out := make([]string, 0, len(merged))
		for _, d := range merged {
			out = append(out, string(d.FactKind))
		}
		return out
	}
	first := order(kinds)
	if len(first) != len(kinds) {
		t.Fatalf("merged %d rows from %d that differ in fact kind -- the key collapsed rows it must keep apart: %v", len(first), len(kinds), first)
	}
	t.Logf("served order: %v", first)
	// Every rotation of the same evidence must serve the same order.
	for shift := 1; shift < len(kinds); shift++ {
		permuted := append(append([]FactKind(nil), kinds[shift:]...), kinds[:shift]...)
		got := order(permuted)
		if fmt.Sprint(got) != fmt.Sprint(first) {
			t.Errorf("arrival order %v served %v, but %v served %v -- the served document depends on the order rows arrived in", permuted, got, kinds, first)
		}
	}
}
