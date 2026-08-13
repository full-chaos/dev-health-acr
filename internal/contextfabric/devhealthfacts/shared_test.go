package devhealthfacts_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// timeAxisCase names one provider/subject combination covering every
// provider file in this package, so the CHAOS-3781 tier behavior is proven
// uniformly rather than for a single representative provider.
//
// answersHistory records which tier the provider belongs to (see
// timebound.go): true for Tier A (as-of native rollups) and Tier B
// (derivable from immutable interval columns), false for Tier C, whose
// fact is a mutable attribute with no recorded history.
type timeAxisCase struct {
	name           string
	kind           contextfabric.FactKind
	subject        contextfabric.SubjectRef
	answersHistory bool
}

func timeAxisCases() []timeAxisCase {
	return []timeAxisCase{
		// Tier C -- no recorded history.
		{"identity", contextfabric.FactIdentity, repoSubject("repo-1"), false},
		{"membership", contextfabric.FactMembership, repoSubject("repo-1"), false},
		{"status", contextfabric.FactStatus, workItemSubject("WIDGET-101"), false},
		{"work", contextfabric.FactWork, workItemSubject("WIDGET-101"), false},
		{"blockers", contextfabric.FactBlockers, workItemSubject("WIDGET-101"), false},
		{"required_children", contextfabric.FactRequiredChildren, workItemSubject("WIDGET-101"), false},
		// Tier B -- derivable from immutable interval columns.
		{"actual_completion", contextfabric.FactActualCompletion, workItemSubject("WIDGET-101"), true},
		{"pull_requests", contextfabric.FactPullRequests, pullRequestSubject("repo-1", "1042"), true},
		{"reviews", contextfabric.FactReviews, reviewSubject("review-1"), true},
		{"continuous_integration", contextfabric.FactContinuousIntegration, ciRunSubject("run-1"), true},
		{"deployments", contextfabric.FactDeployments, deploymentSubject("deploy-1"), true},
		{"incidents", contextfabric.FactIncidents, incidentSubject("incident-1"), true},
		// Tier A -- as-of native rollups.
		{"metrics", contextfabric.FactMetrics, repoSubject("repo-1"), true},
		{"health", contextfabric.FactHealth, repoSubject("repo-1"), true},
		{"workload", contextfabric.FactWorkload, teamSubject("CHAOS"), true},
		{"investment", contextfabric.FactInvestment, teamSubject("CHAOS"), true},
		{"readiness", contextfabric.FactReadiness, teamSubject("CHAOS"), true},
		{"operational_deficiencies", contextfabric.FactOperationalDeficiencies, teamSubject("CHAOS"), true},
		{"source_health", contextfabric.FactSourceHealth, organizationSubject("org-1"), true},
	}
}

func historicalQuery(tc timeAxisCase, timeContext contextfabric.TimeContext) contextfabric.FactQuery {
	return contextfabric.FactQuery{
		Time: timeContext, Kind: tc.kind, Subjects: []contextfabric.SubjectRef{tc.subject},
	}
}

// bindingNames lists the parameter names of the last captured query, so a
// test can prove the time bound reached ClickHouse as a bound PARAMETER
// rather than as interpolated text.
func (c *fakeClient) bindingNames() []string {
	if len(c.queries) == 0 {
		return nil
	}
	last := c.queries[len(c.queries)-1]
	names := make([]string, 0, len(last.bindings))
	for _, binding := range last.bindings {
		names = append(names, binding.Name)
	}
	return names
}

// CHAOS-3781 replaces the H6 blanket refusal. Where that test asserted
// every provider refuses every non-current axis, these assert the tier
// split timebound.go defines: a provider that can answer honestly does,
// and one that cannot degrades honestly instead of guessing.

// TestTierCProvidersRefuseHistoricalFacts is the surviving half of H6. A
// fact with no recorded history -- a work item's status vocabulary, a
// title, a dependency row carrying only last_synced -- still must not be
// answered for a past time, and must not reach ClickHouse to decide that.
//
// The state is now not_applicable rather than unconfigured: the source is
// present and healthy, it simply cannot speak for that time, and §7.6
// keeps those two states distinct.
func TestTierCProvidersRefuseHistoricalFacts(t *testing.T) {
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
				t.Fatalf("ReadFacts() error = %v, want nil (a degradation is a result, not an error)", err)
			}
			if result.State != contextfabric.SourceNotApplicable {
				t.Fatalf("result.State = %q, want %q", result.State, contextfabric.SourceNotApplicable)
			}
			if len(result.Facts) != 0 {
				t.Fatalf("result.Facts = %#v, want empty", result.Facts)
			}
			if strings.TrimSpace(result.Reason) == "" {
				t.Fatal("a degraded result must carry a reason (AC-3781-5)")
			}
			if len(client.queries) != 0 {
				t.Fatalf("client.queries = %#v, want no ClickHouse query for a fact with no history", client.queries)
			}
		})
	}
}

// TestTierABProvidersAnswerValidTime is the other half, and the one that
// makes CHAOS-3781 worth doing: a provider that CAN answer for a past time
// must actually run its query, bounded, rather than degrade.
func TestTierABProvidersAnswerValidTime(t *testing.T) {
	t.Parallel()
	asOf := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
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
			// The point is that the provider ANSWERED rather than
			// declining. It may legitimately come back no_data -- this
			// fake returns zero rows, which post-F8 is reported as
			// out-of-retention rather than as a clean available/empty --
			// but it must never report the not_applicable degradation,
			// which means "I cannot speak for that time at all".
			if result.State == contextfabric.SourceNotApplicable {
				t.Fatalf("result.State = %q, but this provider can answer for a past time", result.State)
			}
			if len(client.queries) == 0 {
				t.Fatal("no ClickHouse query was issued; a Tier A/B provider must answer a valid-time question, not degrade")
			}
			// The instant must travel as a bound parameter. If it were
			// interpolated into the statement instead, this binding would
			// be absent -- and an operator-supplied timestamp would be
			// reaching the query text.
			var bound bool
			for _, name := range client.bindingNames() {
				if name == "time_end" {
					bound = true
				}
			}
			if !bound {
				t.Fatalf("bindings = %v, want the requested instant bound as a parameter", client.bindingNames())
			}
			if strings.Contains(client.queries[len(client.queries)-1].statement, "2026-03-01") {
				t.Fatal("the requested instant was interpolated into the statement text; it must only ever be a bound parameter")
			}
		})
	}
}

// TestEveryProviderDegradesOnObservedTime pins the ruling in
// observedTimeUnsupportedReason: no canonical source retains observation
// history, so NO provider -- not even a Tier A rollup that answers
// valid_time fine -- can say what was KNOWN at a past instant.
func TestEveryProviderDegradesOnObservedTime(t *testing.T) {
	t.Parallel()
	asOf := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range timeAxisCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			client := &fakeClient{}
			provider := findProvider(t, devhealthfacts.NewProviders(client), tc.kind)
			result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"},
				historicalQuery(tc, contextfabric.TimeContext{Axis: contextfabric.TemporalObservedTime, AsOf: &asOf}))
			if err != nil {
				t.Fatalf("ReadFacts() error = %v", err)
			}
			if result.State != contextfabric.SourceNotApplicable {
				t.Fatalf("result.State = %q, want %q on the observed-time axis", result.State, contextfabric.SourceNotApplicable)
			}
			if len(client.queries) != 0 {
				t.Fatalf("client.queries = %#v, want no query: computed_at and last_synced are rewrite stamps, so any observed-time filter would be wrong, not merely narrow", client.queries)
			}
		})
	}
}

// TestProvidersFailClosedOnAMalformedHistoricalContext guards the
// fail-closed direction: a historical axis missing the bound its own shape
// requires must degrade, never silently fall through to a query with no
// bound at all -- which would answer with current data under a historical
// label.
func TestProvidersFailClosedOnAMalformedHistoricalContext(t *testing.T) {
	t.Parallel()
	malformed := []struct {
		name        string
		timeContext contextfabric.TimeContext
	}{
		{"valid_time without as_of", contextfabric.TimeContext{Axis: contextfabric.TemporalValidTime}},
		{"range without bounds", contextfabric.TimeContext{Axis: contextfabric.TemporalRange}},
		{"empty axis", contextfabric.TimeContext{}},
	}
	for _, tc := range timeAxisCases() {
		for _, malformedCase := range malformed {
			t.Run(tc.name+"/"+malformedCase.name, func(t *testing.T) {
				t.Parallel()
				client := &fakeClient{}
				provider := findProvider(t, devhealthfacts.NewProviders(client), tc.kind)
				result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"},
					historicalQuery(tc, malformedCase.timeContext))
				if err != nil {
					t.Fatalf("ReadFacts() error = %v", err)
				}
				if result.State != contextfabric.SourceNotApplicable {
					t.Fatalf("result.State = %q, want %q", result.State, contextfabric.SourceNotApplicable)
				}
				if len(client.queries) != 0 {
					t.Fatalf("client.queries = %#v, want no unbounded query for a malformed time context", client.queries)
				}
			})
		}
	}
}

// TestProvidersAllowCurrentTimeAxis is the over-blocking regression guard
// for H6: axis=current must still reach ClickHouse normally.
func TestProvidersAllowCurrentTimeAxis(t *testing.T) {
	t.Parallel()
	for _, tc := range timeAxisCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			client := &fakeClient{}
			provider := findProvider(t, devhealthfacts.NewProviders(client), tc.kind)
			result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
				Time:     contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
				Kind:     tc.kind,
				Subjects: []contextfabric.SubjectRef{tc.subject},
			})
			if err != nil {
				t.Fatalf("ReadFacts() error = %v", err)
			}
			if result.State != contextfabric.SourceAvailable {
				t.Fatalf("result.State = %q, want %q", result.State, contextfabric.SourceAvailable)
			}
			if len(client.queries) == 0 {
				t.Fatalf("client.queries is empty, want a current-time query to reach ClickHouse")
			}
		})
	}
}

// TestReadFailureNeverLeaksRawClickHouseError is the M6 regression test:
// readFailure's Reason must be a fixed, non-parameterized string, never the
// raw driver error -- that error text flows straight into the public
// context_fabric_investigation_result.v1 response's
// coverage.sources[].reason (fact_registry.go's classifyFactReadError).
func TestReadFailureNeverLeaksRawClickHouseError(t *testing.T) {
	t.Parallel()
	const marker = "internal-secret-payload-should-never-leak"
	for _, tc := range timeAxisCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			client := &fakeClient{tables: []fakeTable{{match: "SELECT", err: errors.New(marker)}}}
			provider := findProvider(t, devhealthfacts.NewProviders(client), tc.kind)
			_, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
				Time:     contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
				Kind:     tc.kind,
				Subjects: []contextfabric.SubjectRef{tc.subject},
			})
			if err == nil {
				t.Fatalf("ReadFacts() error = nil, want a query failure")
			}
			if strings.Contains(err.Error(), marker) {
				t.Fatalf("err = %v, must not contain the raw driver error", err)
			}
			var failure *contextfabric.FactReadFailure
			if errors.As(err, &failure) && strings.Contains(failure.Reason, marker) {
				t.Fatalf("failure.Reason = %q, must not contain the raw driver error", failure.Reason)
			}
		})
	}
}

// TestBoundedHistoricalQueryWithNoRowsReportsOutOfRetention is round-1 F8:
// a historical query that finds nothing must say so as no_data with the
// retention reason, not as a clean `available`.
//
// The two are genuinely different answers -- "nothing happened then"
// versus "we do not retain data that far back" -- and reporting the second
// as the first is a quiet false negative. The rest of the answer survives
// either way (AC-3781-5).
func TestBoundedHistoricalQueryWithNoRowsReportsOutOfRetention(t *testing.T) {
	t.Parallel()
	asOf := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range timeAxisCases() {
		if !tc.answersHistory {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			client := &fakeClient{} // no tables seeded: every query returns zero rows
			provider := findProvider(t, devhealthfacts.NewProviders(client), tc.kind)
			result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"},
				historicalQuery(tc, contextfabric.TimeContext{Axis: contextfabric.TemporalValidTime, AsOf: &asOf}))
			if err != nil {
				t.Fatalf("ReadFacts() error = %v", err)
			}
			if result.State != contextfabric.SourceNoData {
				t.Fatalf("result.State = %q, want %q for a historical query that retained nothing", result.State, contextfabric.SourceNoData)
			}
			if !strings.Contains(result.Reason, "predate the retained corpus") {
				t.Fatalf("result.Reason = %q, want it to name the retention limitation", result.Reason)
			}
		})
	}
}

// TestCurrentAxisWithNoRowsStaysAvailable is the over-blocking guard for
// F8:zero rows on the CURRENT axis has always meant an ordinary empty read,
// and retention is not the question there. Reporting no_data for it would
// change long-standing behavior for every current-axis investigation.
func TestCurrentAxisWithNoRowsStaysAvailable(t *testing.T) {
	t.Parallel()
	for _, tc := range timeAxisCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			client := &fakeClient{}
			provider := findProvider(t, devhealthfacts.NewProviders(client), tc.kind)
			result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"},
				historicalQuery(tc, contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}))
			if err != nil {
				t.Fatalf("ReadFacts() error = %v", err)
			}
			if result.State != contextfabric.SourceAvailable {
				t.Fatalf("result.State = %q, want %q: an empty current-axis read is not a retention question", result.State, contextfabric.SourceAvailable)
			}
		})
	}
}
