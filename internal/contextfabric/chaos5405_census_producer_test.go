package contextfabric

// CHAOS-5405 D-d -- the census PRODUCER, driven through the production entry
// point and read off the SERVED document.
//
// WHY A SECOND CENSUS FILE. chaos5405_census_record_test.go pins the record
// SHAPE: the fields, the nullable count, the payload round trip, the
// projection carry. Every one of those passes with nothing whatsoever
// populating the field. A contract type that no code path fills is, to a
// reader, indistinguishable from a contract type that does not exist -- the
// document has no `fact_scope_census` key either way. That is the same shape
// CHAOS-4085 shipped: a signal plumbed end to end and never installed.
//
// So this file drives `Engine.Investigate`, marshals the result the way
// pginvestigation persists it, and asserts on the JSON. Deleting the
// producer's call site fails these tests; deleting the producer itself fails
// them; demoting a measured zero into an unmeasured null fails them.
//
// VALUES, NOT PRESENCE. The distinction D-d exists for is between two
// documents that both say "no facts": `authorized_population_count: 0` with
// `population_measured: true` (we counted, and the answer is none) and
// `authorized_population_count: null` with `population_measured: false` (we
// never finished counting). An assertion that the key merely EXISTS passes on
// both and pins neither, so every assertion below reads the value.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// chaos5405ServedCensus runs a REAL investigation with the metrics policy
// enabled and an expander whose census counts the caller controls, then
// returns the census rows exactly as they appear in the persisted payload,
// plus the events the production telemetry sink received.
//
// The JSON round trip is not decoration. pginvestigation stores the whole
// result as one JSONB payload and reads it back with json.Unmarshal, so a
// field that does not survive marshalling is a field no consumer and no
// re-baseline can ever read -- and a `*int` is exactly the kind of field that
// can be correct in memory and wrong on the wire.
func chaos5405ServedCensus(t *testing.T, counts FactScopeExpansionCounts) ([]map[string]any, []FactScopeExpansionEvent) {
	t.Helper()
	enableMetricsProjectPolicy(t, 0)

	observed := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	metrics := &factProviderStub{
		capability: planCapability(FactMetrics, "metrics", SubjectRepository),
		result: FactProviderResult{
			State: SourceAvailable, Version: "metrics-v1", ObservedAt: &observed,
		},
	}
	registry, err := NewFactCapabilityRegistry([]FactProvider{metrics}, FactRegistryOptions{
		ScopeExpander: &recordingScopeExpander{counts: counts},
	})
	if err != nil {
		t.Fatalf("NewFactCapabilityRegistry: %v", err)
	}
	telemetry := &recordingTelemetry{}
	engine, request := scopeEngineWithRegistry(t, telemetry, []SubjectRef{scopeProject}, registry)

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("Investigate: %v", err)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("a result carrying a census must still be servable: %v", err)
	}

	payload, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal the served result: %v", err)
	}
	var document map[string]any
	if err := json.Unmarshal(payload, &document); err != nil {
		t.Fatalf("unmarshal the persisted payload: %v", err)
	}
	raw, present := document["fact_scope_census"]
	if !present {
		t.Fatalf("the served document carries no fact_scope_census key -- the record type exists and nothing fills it, which is the same document a reader gets when the field was never designed")
	}
	rows, ok := raw.([]any)
	if !ok {
		t.Fatalf("fact_scope_census = %T, want an array", raw)
	}
	records := make([]map[string]any, 0, len(rows))
	for i, row := range rows {
		record, ok := row.(map[string]any)
		if !ok {
			t.Fatalf("census row %d = %T, want an object", i, row)
		}
		records = append(records, record)
	}
	return records, telemetry.factScopeExpansions
}

// TestChaos5405_AMeasuredZeroReachesTheServedDocumentAsZero is one half of
// D-d's distinction: the traversal COMPLETED and the caller-visible authorized
// population is genuinely none.
func TestChaos5405_AMeasuredZeroReachesTheServedDocumentAsZero(t *testing.T) {
	records, events := chaos5405ServedCensus(t, FactScopeExpansionCounts{
		CensusComplete: true, AuthorizedCount: 0,
	})
	if len(records) != 1 {
		t.Fatalf("census rows = %d, want 1", len(records))
	}
	record := records[0]

	if got := record["population_measured"]; got != true {
		t.Fatalf("population_measured = %v, want true -- the expander reported a completed census", got)
	}
	count, present := record["authorized_population_count"]
	if !present {
		t.Fatalf("authorized_population_count is absent from the served row -- this field carries no omitempty precisely so it can never vanish")
	}
	if count == nil {
		t.Fatalf("authorized_population_count = null on a COMPLETED census -- a measured zero has been demoted into 'we never counted', which is the exact ambiguity D-d exists to remove")
	}
	if got, ok := count.(float64); !ok || got != 0 {
		t.Fatalf("authorized_population_count = %v (%T), want the number 0", count, count)
	}

	// The row must also NAME what it measured, or two yardstick arms cannot
	// be proven to have run the same activated policy set (D-f gate 8).
	if got := record["policy"]; got != string(FactScopePolicyProjectWorkItemRepository) {
		t.Fatalf("policy = %v, want %q -- a census that does not name its policy is not a discriminator", got, FactScopePolicyProjectWorkItemRepository)
	}
	if got := record["requirement_kind"]; got != string(FactMetrics) {
		t.Fatalf("requirement_kind = %v, want %q", got, FactMetrics)
	}
	if got := record["outcome"]; got == nil || got == "" {
		t.Fatalf("outcome = %v -- an unnamed outcome makes the row unreadable", got)
	}

	// CROSS-AUTHORITY AGREEMENT. The served census and the telemetry stream
	// describe the same decisions, so their cardinality must match. This is
	// what makes "one record per decision, with no filter" an assertion
	// rather than a comment: a producer that silently dropped a row would
	// still satisfy every value check above.
	if len(records) != len(events) {
		t.Fatalf("census rows = %d, emitted decision events = %d -- the served census and the telemetry stream disagree about how many decisions were made", len(records), len(events))
	}
}

// TestChaos5405_AnUnmeasuredPopulationReachesTheServedDocumentAsNull is the
// other half, and the one a plain int could not express at all.
func TestChaos5405_AnUnmeasuredPopulationReachesTheServedDocumentAsNull(t *testing.T) {
	records, events := chaos5405ServedCensus(t, FactScopeExpansionCounts{
		CensusComplete: false, AuthorizedCount: 0,
	})
	if len(records) != 1 {
		t.Fatalf("census rows = %d, want 1", len(records))
	}
	record := records[0]

	if got := record["population_measured"]; got != false {
		t.Fatalf("population_measured = %v, want false -- and it must be PRESENT: a false that vanishes reads as an absent field, not as 'the census did not complete'", got)
	}
	count, present := record["authorized_population_count"]
	if !present {
		t.Fatalf("authorized_population_count is absent from the served row -- absent and null are different documents and only one of them is the ruled shape")
	}
	if count != nil {
		t.Fatalf("authorized_population_count = %v on an INCOMPLETE census -- an unmeasured population has been served as a measured number, which is a claim the traversal never earned", count)
	}
	if len(records) != len(events) {
		t.Fatalf("census rows = %d, emitted decision events = %d", len(records), len(events))
	}
}

// TestChaos5405_ControlAPathThatResolvedNoScopeServesNoCensus is the NEGATIVE
// CONTROL for this file. Optional-first is the whole reason the field carries
// omitempty: "this answer resolved no scope" and "scope was resolved and found
// nothing" must remain different documents. Without this, a producer that
// always wrote an empty array would pass every assertion above.
func TestChaos5405_ControlAPathThatResolvedNoScopeServesNoCensus(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		scope *FactReadScope
	}{
		{"nil scope", nil},
		{"scope with no decisions", &FactReadScope{}},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := scopeServableResult()
			applyFactScopeCensus(&result, tc.scope)
			if result.FactScopeCensus != nil {
				t.Fatalf("FactScopeCensus = %+v, want nil -- an empty array is a claim that decisions were made and none had a population", result.FactScopeCensus)
			}
			payload, err := json.Marshal(result)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var document map[string]any
			if err := json.Unmarshal(payload, &document); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if _, present := document["fact_scope_census"]; present {
				t.Fatalf("fact_scope_census is present on a result that resolved no scope -- optional-first means the key is absent, not empty")
			}
		})
	}
}

// TestChaos5405_EveryServedCensusRowIsCarriedByTheProjection closes the loop
// the shape pin opened: the projection carry is pinned there against a
// hand-built record, and here against the rows the PRODUCER actually made, so
// a producer whose output the projection silently drops fails.
func TestChaos5405_EveryServedCensusRowIsCarriedByTheProjection(t *testing.T) {
	records, _ := chaos5405ServedCensus(t, FactScopeExpansionCounts{
		CensusComplete: true, AuthorizedCount: 3,
	})
	if len(records) != 1 {
		t.Fatalf("census rows = %d, want 1", len(records))
	}
	if got, ok := records[0]["authorized_population_count"].(float64); !ok || got != 3 {
		t.Fatalf("authorized_population_count = %v, want 3 -- a non-zero measured population is what proves the value is carried rather than defaulted", records[0]["authorized_population_count"])
	}

	// The same rows, once more through the projection a consumer reads.
	result := scopeServableResult()
	applyFactScopeCensus(&result, &FactReadScope{Events: []FactScopeExpansionEvent{{
		RequirementKind: FactMetrics,
		OriginKind:      SubjectProject,
		Policy:          FactScopePolicyProjectWorkItemRepository,
		Outcome:         FactScopeExpanded,
		CensusComplete:  true,
		AuthorizedCount: 3,
	}}})
	if len(result.FactScopeCensus) != 1 {
		t.Fatalf("producer wrote %d rows, want 1", len(result.FactScopeCensus))
	}
	if result.FactScopeCensus[0].AuthorizedPopulationCount == nil {
		t.Fatal("AuthorizedPopulationCount is nil on a completed census")
	}
	if got := *result.FactScopeCensus[0].AuthorizedPopulationCount; got != 3 {
		t.Fatalf("AuthorizedPopulationCount = %d, want 3", got)
	}
	var _ contractsv1.ContextFabricFactScopeCensusRecord = result.FactScopeCensus[0]
}

// TestChaos5405_TwoDecisionsGetTwoRowsAndDoNotShareAPopulationPointer pins the
// per-decision granularity AND the aliasing hazard the pointer introduces.
//
// A loop that takes the address of its iteration variable once would give
// every row the LAST decision's population -- uniformly wrong, with no error
// and no failing shape check anywhere. Two decisions with DIFFERENT
// populations is the only arrangement that can see it.
func TestChaos5405_TwoDecisionsGetTwoRowsAndDoNotShareAPopulationPointer(t *testing.T) {
	t.Parallel()
	result := scopeServableResult()
	applyFactScopeCensus(&result, &FactReadScope{Events: []FactScopeExpansionEvent{
		{RequirementKind: FactMetrics, OriginKind: SubjectProject, CensusComplete: true, AuthorizedCount: 7},
		{RequirementKind: FactStatus, OriginKind: SubjectTeam, CensusComplete: true, AuthorizedCount: 11},
	}})
	if len(result.FactScopeCensus) != 2 {
		t.Fatalf("census rows = %d, want 2 -- one per decision", len(result.FactScopeCensus))
	}
	first, second := result.FactScopeCensus[0], result.FactScopeCensus[1]
	if first.AuthorizedPopulationCount == nil || second.AuthorizedPopulationCount == nil {
		t.Fatal("a completed census must carry its count on every row")
	}
	if first.AuthorizedPopulationCount == second.AuthorizedPopulationCount {
		t.Fatal("both rows share one *int -- every population would report the last decision's number")
	}
	if got := *first.AuthorizedPopulationCount; got != 7 {
		t.Fatalf("first row population = %d, want 7", got)
	}
	if got := *second.AuthorizedPopulationCount; got != 11 {
		t.Fatalf("second row population = %d, want 11", got)
	}
	if first.RequirementKind == second.RequirementKind {
		t.Fatal("both rows carry the same requirement kind -- the rows are not per-decision")
	}
}

// TestChaos5405_TheWorstCaseCensusFitsTheSmallestBudgetACallerMayRequest is
// the response-shape measurement, written as an ASSERTION rather than as a
// number in a PR body.
//
// Any change that grows the served response has to be measured against the
// per-request budgets, and the census is the first field this ticket adds to
// the served document. The measurement is only worth having if it cannot go
// stale, so nothing here is a literal: the row ceiling is derived from the
// LIVE eligibility table (the resolver emits at most one decision per
// requirement/origin pair in it) and the policy and basis strings are the
// real ones, so adding a policy or lengthening a name moves this test rather
// than moving a sentence nobody re-measures.
//
// The bound compared against is the SMALLEST budget a caller may request
// (ContextFabricSerializedBytesMin), not the 256 KiB default, because the
// minimum is where a fixed-size addition actually bites -- against the
// default the whole census is under 3%. The headroom is that minimum less the
// smallest valid answer (ContextFabricMinimumAnswerBytes): a census that ate
// more than that would let a legitimate answer be refused for being too large
// on account of its own diagnostics, which is the #413 shape.
//
// MEASURED at this tip: 21 rows, 6304 bytes keyed, against 7169 bytes of
// headroom -- it fits, with about 12% of the headroom to spare. That margin
// is thin enough to be worth a test rather than a note.
func TestChaos5405_TheWorstCaseCensusFitsTheSmallestBudgetACallerMayRequest(t *testing.T) {
	t.Parallel()

	// The resolver walks each requirement kind's origin kinds and emits at
	// most one event per eligible pair, so the live table's size IS the row
	// ceiling. Derived, never typed.
	population := 200
	rows := []contractsv1.ContextFabricFactScopeCensusRecord{}
	for kind, byOrigin := range factScopePolicies {
		for origin, rule := range byOrigin {
			rows = append(rows, contractsv1.ContextFabricFactScopeCensusRecord{
				RequirementKind: string(kind),
				OriginKind:      string(origin),
				Policy:          string(rule.Policy),
				Basis:           string(rule.Basis),
				// The longest axis and outcome tokens in their own closed
				// vocabularies, so the row is the widest one that can be
				// served rather than a typical one.
				Axis:                      string(contractsv1.ContextFabricTemporalObservedTime),
				Outcome:                   string(FactScopeTargetKindMismatch),
				TargetLimit:               maxFactScopeTargets,
				PopulationMeasured:        true,
				AuthorizedPopulationCount: &population,
				AdmittedCount:             maxFactScopeTargets,
				Truncated:                 true,
			})
		}
	}
	if len(rows) == 0 {
		t.Fatal("the eligibility table is empty -- this measurement would be vacuous")
	}

	document, err := json.Marshal(map[string]any{"fact_scope_census": rows})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// Less the surrounding object's own two braces: what is measured is the
	// key and its value as they sit on a result, not a document of one field.
	censusBytes := len(document) - 2

	headroom := contractsv1.ContextFabricSerializedBytesMin - contractsv1.ContextFabricMinimumAnswerBytes
	if censusBytes >= headroom {
		t.Fatalf("the worst-case census is %d bytes over %d rows, against %d bytes of headroom (%d minimum budget less the %d-byte smallest valid answer) -- a caller requesting the smallest permitted budget could be refused an answer because of the diagnostics attached to it",
			censusBytes, len(rows), headroom,
			contractsv1.ContextFabricSerializedBytesMin, contractsv1.ContextFabricMinimumAnswerBytes)
	}
	t.Logf("worst-case census: rows=%d bytes=%d headroom=%d spare=%d", len(rows), censusBytes, headroom, headroom-censusBytes)
}

// TestChaos5405_TheCensusIsNotChargedAgainstTheItemBudget pins the DECISION
// recorded on ContextFabricResultItemCounts, because a comment is not an
// assertion.
//
// The item budget's own contract is that a new result collection is a decision
// to charge or not to charge it -- "not something a reflective walk silently
// starts billing on the day the field is added." The census is deliberately
// NOT charged: it is a statement about the answer's completeness rather than
// evidence, and charging it would let the diagnostics that explain a
// truncation evict the rows they are explaining.
//
// Without this test the decision is only prose. A future change that starts
// counting the census -- by adding a field, or by replacing the explicit
// counter with a reflective walk -- would pass every other guard in the
// package while quietly shrinking answers.
func TestChaos5405_TheCensusIsNotChargedAgainstTheItemBudget(t *testing.T) {
	t.Parallel()

	result := scopeServableResult()
	before := contractsv1.CountContextFabricResultItems(result)

	applyFactScopeCensus(&result, &FactReadScope{Events: []FactScopeExpansionEvent{
		{RequirementKind: FactStatus, OriginKind: SubjectProject, CensusComplete: true, AuthorizedCount: 9},
		{RequirementKind: FactWork, OriginKind: SubjectTeam, CensusComplete: false},
	}})
	if len(result.FactScopeCensus) != 2 {
		t.Fatalf("fixture wrote %d census rows, want 2 -- this test would otherwise measure the absence of a census", len(result.FactScopeCensus))
	}
	after := contractsv1.CountContextFabricResultItems(result)

	if after != before {
		t.Fatalf("item counts moved with the census attached: before=%+v after=%+v -- the census is not evidence and must not be charged against the caller's item allowance", before, after)
	}
	if after.Budgeted() != before.Budgeted() {
		t.Fatalf("Budgeted() = %d with a census, %d without", after.Budgeted(), before.Budgeted())
	}

	// CONTROL, in the other direction: a collection that IS charged moves the
	// count. Without it, "nothing moved" would also pass on a counter that
	// counts nothing at all.
	charged := result
	charged.Drivers = append(append([]contractsv1.ContextFabricDriverJudgment(nil), result.Drivers...),
		contractsv1.ContextFabricDriverJudgment{})
	if got, want := contractsv1.CountContextFabricResultItems(charged).Budgeted(), before.Budgeted()+1; got != want {
		t.Fatalf("control failed: Budgeted() = %d after adding one driver, want %d -- this test cannot tell a charged collection from an uncharged one", got, want)
	}
}

// TestChaos5405_TheServedRowCarriesTheAdmittedCountItMeasured closes a gap the
// mutation battery found in THIS FILE's own pins.
//
// Every census assertion above ran on a fixture whose admitted count was zero,
// so zeroing `admitted_count` in the producer changed nothing any test could
// see: the arm that replaces `AdmittedCount: event.AdmittedCount` with a
// literal 0 SURVIVED. An expected value equal to the field's zero value pins
// nothing, which is why this drives a traversal that actually admits a target
// and asserts the served number is the one that was measured.
func TestChaos5405_TheServedRowCarriesTheAdmittedCountItMeasured(t *testing.T) {
	enableMetricsProjectPolicy(t, 0)

	observed := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	metrics := &factProviderStub{
		capability: planCapability(FactMetrics, "metrics", SubjectRepository),
		result: FactProviderResult{
			State: SourceAvailable, Version: "metrics-v1", ObservedAt: &observed,
		},
	}
	registry, err := NewFactCapabilityRegistry([]FactProvider{metrics}, FactRegistryOptions{
		// A target that IS admitted, so admitted_count is NON-ZERO and a
		// dropped assignment becomes visible.
		ScopeExpander: &recordingScopeExpander{
			targets: []SubjectRef{scopeRepo},
			counts:  FactScopeExpansionCounts{CensusComplete: true, AuthorizedCount: 1},
		},
	})
	if err != nil {
		t.Fatalf("NewFactCapabilityRegistry: %v", err)
	}
	telemetry := &recordingTelemetry{}
	engine, request := scopeEngineWithRegistry(t, telemetry, []SubjectRef{scopeProject}, registry)
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("Investigate: %v", err)
	}

	if len(telemetry.factScopeExpansions) != 1 {
		t.Fatalf("expansion events = %d, want 1", len(telemetry.factScopeExpansions))
	}
	measured := telemetry.factScopeExpansions[0].AdmittedCount
	if measured == 0 {
		t.Fatalf("the fixture admitted nothing, so this test would assert a zero against a zero and pin nothing")
	}

	payload, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var document map[string]any
	if err := json.Unmarshal(payload, &document); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	rows, ok := document["fact_scope_census"].([]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("fact_scope_census = %v, want one row", document["fact_scope_census"])
	}
	record, ok := rows[0].(map[string]any)
	if !ok {
		t.Fatalf("census row = %T, want an object", rows[0])
	}
	served, ok := record["admitted_count"].(float64)
	if !ok {
		t.Fatalf("admitted_count = %v (%T), want a number", record["admitted_count"], record["admitted_count"])
	}
	if int(served) != measured {
		t.Fatalf("served admitted_count = %d, the traversal measured %d -- the served census must report what was admitted, not a constant", int(served), measured)
	}
}
