package devhealthfacts_test

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-3781 round-1 F1: providers are not uniform in temporal precision,
// so each must report the grain it actually answered at.
//
// Before this, the engine assumed day grain for every answered source. A
// pull request merged at 14:00Z was therefore serialized under a day
// grain, reading as though the answer only knew about midnight -- a
// precision claim the data contradicts, in the opposite direction from the
// over-claiming this issue is mostly about, but wrong either way.

// TestProviderGrainMatchesItsActualPrecision pins each provider's declared
// grain. A daily rollup can only speak for a day; a provider deriving from
// an immutable event timestamp answers at the exact instant.
func TestProviderGrainMatchesItsActualPrecision(t *testing.T) {
	t.Parallel()
	asOf := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

	// Kinds whose answer comes from a *_daily rollup bucket.
	dailyKinds := map[contextfabric.FactKind]bool{
		contextfabric.FactMetrics:                 true,
		contextfabric.FactInvestment:              true,
		contextfabric.FactHealth:                  true,
		contextfabric.FactReadiness:               true,
		contextfabric.FactOperationalDeficiencies: true,
	}

	for _, tc := range timeAxisCases() {
		if !tc.answersHistory {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			client := &fakeClient{}
			provider := findProvider(t, devhealthfacts.NewProviders(client), tc.kind)
			result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"},
				historicalQuery(tc, contextfabric.TimeContext{Axis: contextfabric.TemporalValidTime, AsOf: &asOf}))
			if err != nil {
				t.Fatalf("ReadFacts() error = %v", err)
			}
			want := contextfabric.GrainInstant
			if dailyKinds[tc.kind] {
				want = contextfabric.GrainDay
			}
			if result.Grain != want {
				t.Fatalf("Grain = %q, want %q -- a provider must report the precision it can actually answer at", result.Grain, want)
			}
		})
	}
}

// TestTierCProvidersReportNoGrain: a provider that declined to answer
// contributes no precision, so it must not coarsen (or refine) the
// composed answer.
func TestTierCProvidersReportNoGrain(t *testing.T) {
	t.Parallel()
	asOf := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range timeAxisCases() {
		if tc.answersHistory {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			client := &fakeClient{}
			provider := findProvider(t, devhealthfacts.NewProviders(client), tc.kind)
			result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"},
				historicalQuery(tc, contextfabric.TimeContext{Axis: contextfabric.TemporalValidTime, AsOf: &asOf}))
			if err != nil {
				t.Fatalf("ReadFacts() error = %v", err)
			}
			if result.Grain != "" {
				t.Fatalf("Grain = %q, want empty: a provider that could not answer reports no precision", result.Grain)
			}
		})
	}
}

// TestHistoricalIncidentsOmitSeverity is round-1 F2: severity is revised in
// place with no recorded history, so it cannot be reported for a past
// time. Reporting the current value under a historical label is the exact
// defect this issue removes, and a reason on the overall answer does not
// undo a wrong VALUE on a specific fact.
func TestHistoricalIncidentsOmitSeverity(t *testing.T) {
	t.Parallel()
	asOf := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	subject := incidentSubject("incident-1")

	client := &fakeClient{tables: []fakeTable{{
		match: "FROM operational_incidents",
		rows:  [][]any{{"incident-1", "open", ""}},
	}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactIncidents)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time:     contextfabric.TimeContext{Axis: contextfabric.TemporalValidTime, AsOf: &asOf},
		Kind:     contextfabric.FactIncidents,
		Subjects: []contextfabric.SubjectRef{subject},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("Facts = %#v, want exactly one incident fact", result.Facts)
	}
	if _, present := result.Facts[0].Fields["severity"]; present {
		t.Fatal("a historical incident fact carried a severity; severity has no recorded history and must be omitted, not guessed")
	}
	// The derivable half still answers.
	if _, present := result.Facts[0].Fields["status"]; !present {
		t.Fatal("status is derivable from started_at/resolved_at and must still be reported")
	}
	// And the omission is named, so a reader knows what went missing.
	if result.Reason == "" {
		t.Fatal("the severity omission must be stated, not silent")
	}
}

// TestCurrentIncidentsStillCarrySeverity is the over-blocking guard for
// F2: severity is perfectly good data for a current-axis question, and
// dropping it there would be a regression.
func TestCurrentIncidentsStillCarrySeverity(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{
		match: "FROM operational_incidents",
		rows:  [][]any{{"incident-1", "open", "critical"}},
	}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactIncidents)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time:     contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind:     contextfabric.FactIncidents,
		Subjects: []contextfabric.SubjectRef{incidentSubject("incident-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("Facts = %#v, want one fact", result.Facts)
	}
	if _, present := result.Facts[0].Fields["severity"]; !present {
		t.Fatal("a current-axis incident fact lost its severity")
	}
}

// TestF2_UnrepresentableUnsignedValuesAreOmittedNotWrapped is CHAOS-3781
// round-3 F2, red→green.
//
// backfill_log.duration_ms and investment_metrics_daily.churn_loc are the
// only UInt64 columns these providers read; every other numeric column is
// UInt32 and fits int64 by construction. Wrapping a UInt64 with toInt64
// in SQL turned any value above MaxInt64 NEGATIVE, and FactValue.Validate
// accepts negatives, so it would have reached a public answer as a
// silently wrong number.
//
// The schema parity guard cannot catch this: it proves a scan does not
// FAIL, not that a value is right. So the range is checked explicitly and
// an out-of-range row is OMITTED rather than reported wrong.
func TestF2_UnrepresentableUnsignedValuesAreOmittedNotWrapped(t *testing.T) {
	t.Parallel()
	// One tick above MaxInt64: the smallest value that wraps negative.
	const overflow = uint64(math.MaxInt64) + 1

	for _, testCase := range []struct {
		name    string
		kind    contextfabric.FactKind
		subject contextfabric.SubjectRef
		match   string
		row     []any
	}{
		{
			name: "backfill_log.duration_ms", kind: contextfabric.FactSourceHealth,
			subject: organizationSubject("org-1"), match: "FROM backfill_log",
			row: []any{"github", "success", int64(412), overflow, "", "2026-08-12 03:00:00"},
		},
		{
			name: "investment_metrics_daily.churn_loc", kind: contextfabric.FactInvestment,
			subject: teamSubject("CHAOS"), match: "FROM investment_metrics_daily",
			row: []any{"CHAOS", "product", "growth", "2026-02-22", int64(30), int64(12), int64(4), overflow, float64(18.5)},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			client := &fakeClient{tables: []fakeTable{{match: testCase.match, rows: [][]any{testCase.row}}}}
			provider := findProvider(t, devhealthfacts.NewProviders(client), testCase.kind)
			result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
				Time:     contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
				Kind:     testCase.kind,
				Subjects: []contextfabric.SubjectRef{testCase.subject},
			})
			if err != nil {
				t.Fatalf("ReadFacts() error = %v", err)
			}
			// The wrong-value outcome: a fact carrying a negative count.
			for _, fact := range result.Facts {
				for field, value := range fact.Fields {
					if value.Integer != nil && *value.Integer < 0 {
						t.Fatalf("field %q reported %d -- an unsigned source value wrapped negative instead of being omitted", field, *value.Integer)
					}
				}
			}
			if result.Reason == "" {
				t.Fatal("an omitted out-of-range value must be stated, not silent")
			}
		})
	}
}

// TestF2_RepresentableUnsignedValuesStillReport is the over-blocking
// guard: ordinary values must survive the range check untouched.
func TestF2_RepresentableUnsignedValuesStillReport(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{
		match: "FROM backfill_log",
		rows:  [][]any{{"github", "success", int64(412), uint64(9800), "", "2026-08-12 03:00:00"}},
	}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactSourceHealth)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time:     contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind:     contextfabric.FactSourceHealth,
		Subjects: []contextfabric.SubjectRef{organizationSubject("org-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("Facts = %#v, want the ordinary row to survive", result.Facts)
	}
	value, ok := result.Facts[0].Fields["duration_ms"]
	if !ok || value.Integer == nil || *value.Integer != 9800 {
		t.Fatalf("duration_ms = %#v, want 9800", value)
	}
}
