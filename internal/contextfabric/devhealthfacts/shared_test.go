package devhealthfacts_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// timeAxisCase names one provider/subject combination covering all seven
// provider files in this package, so the H6 refusal check is proven
// uniformly rather than for a single representative provider.
type timeAxisCase struct {
	name    string
	kind    contextfabric.FactKind
	subject contextfabric.SubjectRef
}

func timeAxisCases() []timeAxisCase {
	return []timeAxisCase{
		{"identity", contextfabric.FactIdentity, repoSubject("repo-1")},
		{"membership", contextfabric.FactMembership, repoSubject("repo-1")},
		{"status", contextfabric.FactStatus, workItemSubject("WIDGET-101")},
		{"work", contextfabric.FactWork, workItemSubject("WIDGET-101")},
		{"actual_completion", contextfabric.FactActualCompletion, workItemSubject("WIDGET-101")},
		{"blockers", contextfabric.FactBlockers, workItemSubject("WIDGET-101")},
		{"required_children", contextfabric.FactRequiredChildren, workItemSubject("WIDGET-101")},
		{"pull_requests", contextfabric.FactPullRequests, pullRequestSubject("repo-1", "1042")},
		{"reviews", contextfabric.FactReviews, reviewSubject("review-1")},
		{"continuous_integration", contextfabric.FactContinuousIntegration, ciRunSubject("run-1")},
		{"deployments", contextfabric.FactDeployments, deploymentSubject("deploy-1")},
		{"incidents", contextfabric.FactIncidents, incidentSubject("incident-1")},
	}
}

// TestProvidersRefuseNonCurrentTimeAxis is the H6 regression test: every
// provider in this package must refuse (SourceUnconfigured, no facts) a
// FactQuery whose Time.Axis is anything other than
// contextfabric.TemporalCurrent, and must never reach ClickHouse to do so --
// querying current data and presenting it as an answer to a
// historical/point-in-time question would be a false historical answer.
func TestProvidersRefuseNonCurrentTimeAxis(t *testing.T) {
	t.Parallel()
	for _, tc := range timeAxisCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			client := &fakeClient{}
			provider := findProvider(t, devhealthfacts.NewProviders(client), tc.kind)
			result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
				Time:     contextfabric.TimeContext{Axis: contextfabric.TemporalValidTime},
				Kind:     tc.kind,
				Subjects: []contextfabric.SubjectRef{tc.subject},
			})
			if err != nil {
				t.Fatalf("ReadFacts() error = %v, want nil (refusal is a result, not an error)", err)
			}
			if result.State != contextfabric.SourceUnconfigured {
				t.Fatalf("result.State = %q, want %q", result.State, contextfabric.SourceUnconfigured)
			}
			if len(result.Facts) != 0 {
				t.Fatalf("result.Facts = %#v, want empty", result.Facts)
			}
			if len(client.queries) != 0 {
				t.Fatalf("client.queries = %#v, want no ClickHouse query issued for an unsupported time axis", client.queries)
			}
		})
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
