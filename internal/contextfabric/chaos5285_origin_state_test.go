package contextfabric

import (
	"context"
	"encoding/json"
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
	// ONE ROW PER KIND, naming the read the folded source does NOT publish.
	// The member read is `available` for both kinds and the group read is
	// worse for both, so the fold carries the group's state and the rows
	// carry the member's.
	want := []originRow{
		{"canonical_fact:health", string(SourceAvailable), string(SubjectProject), false},
		{"canonical_fact:workload", string(SourceAvailable), string(SubjectProject), false},
	}
	sort.Slice(want, func(i, j int) bool { return fmt.Sprint(want[i]) < fmt.Sprint(want[j]) })
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("served origin rows = %v, want %v -- one row per kind, naming the read whose state the fold does not publish", got, want)
	}

	// THE JOIN, AND IT IS THE WHOLE POINT: served rows + folded source states
	// RECONSTRUCT both reads, exactly as the pre-fold trace line recorded
	// them. Nothing the trace saw is missing from the document, and nothing
	// on the document is absent from the trace.
	folded := map[string]SourceState{}
	for _, source := range result.Coverage.Sources {
		folded[source.Source] = source.State
	}
	named := map[string]originRow{}
	for _, row := range got {
		named[row.source+"|"+row.origin] = row
	}
	for _, event := range telemetry.groupReadCoverageStates {
		origin := string(SubjectProject)
		if event.Read == GroupReadArmGroup {
			origin = string(event.GroupKind)
		}
		reconstructed := string(folded[event.Source])
		if row, ok := named[event.Source+"|"+origin]; ok {
			reconstructed = row.state
		}
		t.Logf("reconstruct %s/%s: trace=%s document=%s", origin, event.Source, event.State, reconstructed)
		if reconstructed != string(event.State) {
			t.Errorf("%s/%s reconstructs to %q from the document, but the read was %q -- a reader cannot recover this read's state",
				origin, event.Source, reconstructed, event.State)
		}
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
	// THE FAILED READ IS THE ONE THE DOCUMENT LACKS. Its bundle is never
	// composed, so nothing of it reaches the served source -- which stays
	// the member read's own `available`. The row is therefore the GROUP's,
	// and it is the only place the failure is readable. (On the COMPOSED
	// path the same rule names the other read, because there the fold
	// publishes the worse state; the rule is "name the read the served
	// source does not", not "name the group".)
	want := fmt.Sprint([]originRow{
		{"canonical_fact:health", string(SourceUnavailable), string(SubjectTeam), false},
	})
	if fmt.Sprint(got) != want {
		t.Fatalf("served origin rows = %v, want %s", got, want)
	}
	var servedHealth SourceState
	for _, source := range result.Coverage.Sources {
		if source.Source == "canonical_fact:health" {
			servedHealth = source.State
		}
	}
	t.Logf("served health source = %q, group row = %q -> both reads recoverable", servedHealth, got[0].state)
	if servedHealth != SourceAvailable {
		t.Fatalf("served health source = %q, want %q -- the member read's own state, since the failed bundle was not composed", servedHealth, SourceAvailable)
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
		member := Coverage{Sources: []SourceObservation{{Source: "canonical_fact:health", State: state}}}
		group := Coverage{Sources: []SourceObservation{{Source: "canonical_fact:health", State: SourceUnconfigured}}}
		rows := readOriginStateCoverage(group, member, group, origin, SubjectRepository).Details
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
	// AND THE COMBINED CAP'S SECOND OBSERVATION, on every kind of the group
	// read. This is the shape r1 P1-2 found: the cap appends a `truncated`
	// observation for a kind the provider already reported, so a producer
	// that emitted per OBSERVATION served two rows per kind per read -- 88
	// origin details instead of 44 -- and a LEGAL full-vocabulary grouped
	// request came to 110 coverage details against a bound of 100. It was
	// not a near miss on the ceiling; `Investigate` refused to serve an
	// answer it should have served. The maximal fixture carries the doubling
	// so the bound is measured against the worst shape the pipeline can
	// actually produce, not the tidiest one.
	for _, kind := range contractsv1.ContextFabricFactKindVocabulary() {
		appendFactCoverage(&group, FactKind(kind), SourceTruncated, nil, "",
			fmt.Sprintf("%s (omitted %d)", groupFactsCapReason, 1), coverageDetailSpec{})
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
	// The SERVED source states are the fold of the two reads -- exactly what
	// the document will carry -- so the rows are taken against the real
	// thing rather than against a derived guess.
	folded := MergeCoverage("org_1", member.Coverage, group.Coverage)
	origins := readOriginStateCoverage(folded, member.Coverage, group.Coverage, SubjectProject, SubjectTeam)
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
	// KINDS, NOT READS x KINDS. This is the number the bound is safe
	// against: at most one row per kind, whatever the two reads did and
	// however many observations each took. A row per read per kind measured
	// 115 here and `Investigate` refused a legal request.
	origin := byCode[contractsv1.ContextFabricCoverageDetailFactReadOriginState]
	if origin > contractsv1.ContextFabricFactKindCount {
		t.Fatalf("origin rows = %d, want at most %d (one per kind)", origin, contractsv1.ContextFabricFactKindCount)
	}
	// Non-vacuous: the two reads differ on every kind here, so every kind
	// must carry its row.
	if origin != contractsv1.ContextFabricFactKindCount {
		t.Fatalf("origin rows = %d, want %d -- the reads differ on every kind, so every kind owes a row", origin, contractsv1.ContextFabricFactKindCount)
	}
	// The group read is `truncated` after the cap, worse than the member's
	// `no_data`, so the SERVED source is the group's and the rows name the
	// member read.
	for _, d := range served.Details {
		if d.Code != contractsv1.ContextFabricCoverageDetailFactReadOriginState {
			continue
		}
		if d.OriginKind != SubjectProject || d.SourceState != SourceNoData {
			t.Fatalf("row for %s is %s/%s, want %s/%s -- the row names the read the folded source does not publish",
				d.FactKind, d.OriginKind, d.SourceState, SubjectProject, SourceNoData)
		}
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

// THE INPUT DOMAIN OF THE DISCLOSURE: every shape a (member, group) pair of
// read states can take, and what the document says about each.
//
// The rule under test has one sentence: serve one row per kind, naming the
// read whose state the SERVED SOURCE does not publish. Every cell below is
// executed through the producer and the merge, and each asserts the property
// that makes the rule worth having -- from the document alone, a reader
// recovers BOTH reads' states. A cell that serves no row must be a cell where
// the source already answers for both.
func TestTheOriginDisclosureInputDomain(t *testing.T) {
	const kind = "health"
	const source = "canonical_fact:" + kind
	cov := func(states ...SourceState) Coverage {
		c := Coverage{}
		for _, state := range states {
			c.Sources = append(c.Sources, SourceObservation{Source: source, State: state, Reason: "fixture"})
		}
		return c
	}
	worst := func(a, b SourceState) SourceState {
		if sourceStateSeverity(b) > sourceStateSeverity(a) {
			return b
		}
		return a
	}

	cases := []struct {
		name          string
		member, group Coverage
		served        Coverage
		memberKind    SubjectKind
		groupKind     SubjectKind
		wantRows      int
		wantOrigin    SubjectKind
		wantState     SourceState
		recoverMember SourceState
		recoverGroup  SourceState
	}{
		{
			name: "both reads equal", member: cov(SourceAvailable), group: cov(SourceAvailable),
			served: cov(SourceAvailable), memberKind: SubjectProject, groupKind: SubjectTeam,
			wantRows: 0, recoverMember: SourceAvailable, recoverGroup: SourceAvailable,
		},
		{
			name: "member worse", member: cov(SourceUnavailable), group: cov(SourceAvailable),
			served: cov(SourceUnavailable), memberKind: SubjectProject, groupKind: SubjectTeam,
			wantRows: 1, wantOrigin: SubjectTeam, wantState: SourceAvailable,
			recoverMember: SourceUnavailable, recoverGroup: SourceAvailable,
		},
		{
			name: "group worse", member: cov(SourceAvailable), group: cov(SourceNoData),
			served: cov(SourceNoData), memberKind: SubjectProject, groupKind: SubjectTeam,
			wantRows: 1, wantOrigin: SubjectProject, wantState: SourceAvailable,
			recoverMember: SourceAvailable, recoverGroup: SourceNoData,
		},
		{
			name: "group read absent for this kind", member: cov(SourceAvailable), group: Coverage{},
			served: cov(SourceAvailable), memberKind: SubjectProject, groupKind: SubjectTeam,
			wantRows: 0, recoverMember: SourceAvailable, recoverGroup: "",
		},
		{
			name: "member read absent for this kind", member: Coverage{}, group: cov(SourceStale),
			served: cov(SourceStale), memberKind: SubjectProject, groupKind: SubjectTeam,
			wantRows: 0, recoverMember: "", recoverGroup: SourceStale,
		},
		{
			// The combined fact cap's shape: the group read is observed twice
			// for one kind, `available` then `truncated`. Its own state is the
			// worse of the two, and the row must not be the stale first one.
			name: "cap truncated the group read", member: cov(SourceAvailable), group: cov(SourceAvailable, SourceTruncated),
			served: cov(SourceTruncated), memberKind: SubjectProject, groupKind: SubjectTeam,
			wantRows: 1, wantOrigin: SubjectProject, wantState: SourceAvailable,
			recoverMember: SourceAvailable, recoverGroup: SourceTruncated,
		},
		{
			// The contract requires an origin kind; an unrooted read cannot
			// be named, so the row is refused rather than served empty.
			name: "the differing read is unrooted", member: cov(SourceAvailable), group: cov(SourceNoData),
			served: cov(SourceNoData), memberKind: "", groupKind: SubjectTeam,
			wantRows: 0, recoverMember: "", recoverGroup: SourceNoData,
		},
	}

	for _, c := range cases {
		got := readOriginStateCoverage(c.served, c.member, c.group, c.memberKind, c.groupKind)
		rows := got.Details
		var shape string
		if len(rows) == 1 {
			shape = fmt.Sprintf("%s=%s", rows[0].OriginKind, rows[0].SourceState)
		}
		t.Logf("CELL %-34s -> %d row(s) %s", c.name, len(rows), shape)
		if len(rows) != c.wantRows {
			t.Errorf("%s: %d row(s), want %d (%v)", c.name, len(rows), c.wantRows, rows)
			continue
		}
		if c.wantRows == 1 {
			if rows[0].OriginKind != c.wantOrigin || rows[0].SourceState != c.wantState {
				t.Errorf("%s: row = %s/%s, want %s/%s", c.name, rows[0].OriginKind, rows[0].SourceState, c.wantOrigin, c.wantState)
			}
			if err := rows[0].Validate(); err != nil {
				t.Errorf("%s: served row fails the contract: %v", c.name, err)
			}
		}
		// THE PROPERTY: reconstruct both reads from the document alone.
		servedState := c.served.Sources[0].State
		recover := func(origin SubjectKind) SourceState {
			for _, row := range rows {
				if row.OriginKind == origin {
					return row.SourceState
				}
			}
			return servedState
		}
		if c.recoverMember != "" && c.memberKind != "" {
			if got := recover(c.memberKind); got != c.recoverMember {
				t.Errorf("%s: the member read reconstructs to %q, but it was %q", c.name, got, c.recoverMember)
			}
		}
		if c.recoverGroup != "" && c.groupKind != "" && c.memberKind != c.groupKind {
			if got := recover(c.groupKind); got != c.recoverGroup {
				t.Errorf("%s: the group read reconstructs to %q, but it was %q", c.name, got, c.recoverGroup)
			}
		}
		// And the served state is itself one of the two reads, which is what
		// makes the single row sufficient.
		if c.recoverMember != "" && c.recoverGroup != "" {
			if servedState != worst(c.recoverMember, c.recoverGroup) {
				t.Errorf("%s: served source %q is not the worse of the two reads (%q, %q) -- the fixture does not model the fold",
					c.name, servedState, c.recoverMember, c.recoverGroup)
			}
		}
	}
}

// WHAT THE PRODUCER REFUSES TO SERVE, and says it refused.
//
// A row it should not mint is worse than a missing one: an unrooted read
// would name an empty population, a graph observation would be disclosed as
// if it were a fact kind, and a row the contract refuses would fail the WHOLE
// investigation over a disclosure. Each clause is a separate guard, executed
// on its own, and each drop is asserted on the EMITTED line -- silence made
// these guards unobservable and, for the same reason, unpinnable: a battery
// weakened each in turn and nothing any test could see changed.
//
// NOT t.Parallel(): it installs the process-global default logger.
func TestEveryRowTheProducerDropsSaysWhyItWasDropped(t *testing.T) {
	type line struct {
		Msg         string `json:"msg"`
		Level       string `json:"level"`
		Reason      string `json:"reason"`
		Source      string `json:"source"`
		OriginKind  string `json:"origin_kind"`
		FactKind    string `json:"fact_kind"`
		SourceState string `json:"source_state"`
		Error       string `json:"error"`
	}
	read := func(t *testing.T, buf *syncBuffer) []line {
		t.Helper()
		var out []line
		for _, raw := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
			if raw == "" {
				continue
			}
			var l line
			if err := json.Unmarshal([]byte(raw), &l); err != nil {
				t.Fatalf("log line is not JSON: %v (%q)", err, raw)
			}
			if strings.HasPrefix(l.Msg, "context fabric read origin state") {
				out = append(out, l)
			}
		}
		return out
	}
	reasons := func(lines []line, reason string) []line {
		var out []line
		for _, l := range lines {
			if l.Reason == reason {
				out = append(out, l)
			}
		}
		return out
	}
	differing := func(memberState, groupState SourceState) (served, member, group Coverage) {
		member = Coverage{Sources: []SourceObservation{{Source: "canonical_fact:health", State: memberState}}}
		group = Coverage{Sources: []SourceObservation{{Source: "canonical_fact:health", State: groupState}}}
		served = group
		return served, member, group
	}

	t.Run("an unrooted differing read is named, not served empty", func(t *testing.T) {
		buf := captureDefaultJSONLogger(t)
		served, member, group := differing(SourceAvailable, SourceNoData)
		// The member read differs from the served source and has no root
		// kind, so it cannot be named and no row is served.
		got := readOriginStateCoverage(served, member, group, "", SubjectTeam)
		if len(got.Details) != 0 {
			t.Fatalf("rows = %d, want 0 -- an unrooted read must not be served with an empty population", len(got.Details))
		}
		lines := reasons(read(t, buf), "unrooted_read")
		if len(lines) != 1 {
			t.Fatalf("unrooted_read lines = %d, want 1: %+v", len(lines), read(t, buf))
		}
		if lines[0].Level != "WARN" {
			t.Errorf("level = %q, want WARN -- a read that cannot be named is a wiring gap", lines[0].Level)
		}
		if lines[0].FactKind != "health" || lines[0].SourceState != string(SourceAvailable) {
			t.Errorf("line = kind %q state %q, want health/available -- it must say what was lost", lines[0].FactKind, lines[0].SourceState)
		}
	})

	t.Run("a non-fact observation is dropped at Debug as not_a_fact_read", func(t *testing.T) {
		buf := captureDefaultJSONLogger(t)
		member := Coverage{Sources: []SourceObservation{
			{Source: "canonical_fact:health", State: SourceAvailable},
			{Source: "context-fabric:graph-validity-windows", State: SourceAvailable},
		}}
		group := Coverage{Sources: []SourceObservation{{Source: "canonical_fact:health", State: SourceNoData}}}
		if got := readOriginStateCoverage(group, member, group, SubjectProject, SubjectTeam); len(got.Details) != 1 {
			t.Fatalf("rows = %d, want 1 (health only)", len(got.Details))
		}
		skipped := reasons(read(t, buf), "not_a_fact_read")
		if len(skipped) != 1 {
			t.Fatalf("not_a_fact_read lines = %d, want 1", len(skipped))
		}
		if skipped[0].Level != "DEBUG" {
			t.Errorf("level = %q, want DEBUG -- a graph observation in fact coverage is routine", skipped[0].Level)
		}
		if skipped[0].Source != "context-fabric:graph-validity-windows" {
			t.Errorf("source = %q, want the graph source", skipped[0].Source)
		}
		// AND IT IS DROPPED HERE, not carried on to be refused downstream.
		if other := reasons(read(t, buf), "contract_refused"); len(other) != 0 {
			t.Fatalf("a graph observation produced %d contract_refused line(s): %+v", len(other), other)
		}
	})

	t.Run("a row the contract refuses is dropped at Warn with the error", func(t *testing.T) {
		buf := captureDefaultJSONLogger(t)
		// An empty state on the read that differs: the code requires
		// source_state, so the minted row fails Validate.
		member := Coverage{Sources: []SourceObservation{{Source: "canonical_fact:health", State: ""}}}
		group := Coverage{Sources: []SourceObservation{{Source: "canonical_fact:health", State: SourceNoData}}}
		if got := readOriginStateCoverage(group, member, group, SubjectProject, SubjectTeam); len(got.Details) != 0 {
			t.Fatalf("rows = %d, want 0 -- the refused row must not be served", len(got.Details))
		}
		refusals := reasons(read(t, buf), "contract_refused")
		if len(refusals) != 1 {
			t.Fatalf("contract_refused lines = %d, want 1: %+v", len(refusals), read(t, buf))
		}
		if refusals[0].Level != "WARN" {
			t.Errorf("level = %q, want WARN -- the producers mint only valid rows, so a refusal is a defect", refusals[0].Level)
		}
		if !strings.Contains(refusals[0].Error, "requires source_state") {
			t.Errorf("error = %q, want the contract's own reason", refusals[0].Error)
		}
	})

	t.Run("a kind with no served source serves nothing and says so", func(t *testing.T) {
		buf := captureDefaultJSONLogger(t)
		_, member, group := differing(SourceAvailable, SourceNoData)
		got := readOriginStateCoverage(Coverage{}, member, group, SubjectProject, SubjectTeam)
		if len(got.Details) != 0 {
			t.Fatalf("rows = %d, want 0 -- with no served source a single row cannot say which read is which", len(got.Details))
		}
		if lines := reasons(read(t, buf), "no_served_source"); len(lines) != 1 {
			t.Fatalf("no_served_source lines = %d, want 1: %+v", len(lines), read(t, buf))
		}
	})

	t.Run("a turn that drops nothing says nothing", func(t *testing.T) {
		// The discriminating control: non-execution is distinguishable from
		// an evaluated zero.
		buf := captureDefaultJSONLogger(t)
		served, member, group := differing(SourceAvailable, SourceNoData)
		if got := readOriginStateCoverage(served, member, group, SubjectProject, SubjectTeam); len(got.Details) != 1 {
			t.Fatalf("rows = %d, want 1", len(got.Details))
		}
		if lines := read(t, buf); len(lines) != 0 {
			t.Fatalf("a clean grouped read emitted %d skip line(s): %+v", len(lines), lines)
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
	for shift := 1; shift < len(kinds); shift++ {
		permuted := append(append([]FactKind(nil), kinds[shift:]...), kinds[:shift]...)
		got := order(permuted)
		if fmt.Sprint(got) != fmt.Sprint(first) {
			t.Errorf("arrival order %v served %v, but %v served %v -- the served document depends on the order rows arrived in", permuted, got, kinds, first)
		}
	}
}
