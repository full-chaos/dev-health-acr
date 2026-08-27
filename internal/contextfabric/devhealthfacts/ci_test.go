package devhealthfacts_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// ciRunSubject mints a CHAOS-3898 "ci_pipeline_run.v2:<repo_id>:<run_id>"
// subject via identity.Derive, mirroring workItemSubject's rationale
// (identity_test.go).
func ciRunSubject(repoID, runID string) contextfabric.SubjectRef {
	canonicalID, omitted, err := identity.Derive(identity.KindCIPipelineRun, []string{repoID, runID}, nil)
	if err != nil || omitted {
		panic(fmt.Sprintf("ciRunSubject(%q, %q): identity.Derive failed: omitted=%v err=%v", repoID, runID, omitted, err))
	}
	return contextfabric.SubjectRef{Kind: contractsv1.ContextFabricSubjectCIRun, CanonicalID: canonicalID, Label: runID}
}

// cicdMetricsRow shapes one cicd_metrics_daily aggregate row: (repo_id,
// day, pipelines_count, success_rate, hasAvgDuration,
// avg_duration_minutes, hasP90Duration, p90_duration_minutes, hasAvgQueue,
// avg_queue_minutes).
func cicdMetricsRow(repoID string) []any {
	return []any{repoID, "2026-02-21", int64(30), float64(0.9), uint8(1), float64(12.0), uint8(1), float64(25.0), uint8(1), float64(3.0)}
}

// TestContinuousIntegrationProviderRepositoryAggregateHappyPath is
// CHAOS-4347's repository-scoped widening: a genuinely repository-scoped
// aggregate read of cicd_metrics_daily, distinct from the per-run status
// shape.
func TestContinuousIntegrationProviderRepositoryAggregateHappyPath(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: "FROM cicd_metrics_daily", rows: [][]any{cicdMetricsRow("repo-1")}},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactContinuousIntegration)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactContinuousIntegration, Subjects: []contextfabric.SubjectRef{repoSubject("repo-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %#v, want 1", result.Facts)
	}
	fact := result.Facts[0]
	if fact.Fields["pipelines_count"].Integer == nil || *fact.Fields["pipelines_count"].Integer != 30 {
		t.Fatalf("pipelines_count = %#v", fact.Fields["pipelines_count"])
	}
	if fact.Fields["success_rate"].Number == nil || *fact.Fields["success_rate"].Number != 0.9 {
		t.Fatalf("success_rate = %#v", fact.Fields["success_rate"])
	}
	if fact.Fields["p90_duration_minutes"].Number == nil || *fact.Fields["p90_duration_minutes"].Number != 25.0 {
		t.Fatalf("p90_duration_minutes = %#v", fact.Fields["p90_duration_minutes"])
	}
	// Grain is NOT asserted here: on the current axis, timebound.go's
	// effectiveGrain always reports GrainInstant by construction ("now"
	// needs no bucket) regardless of the provider's own grain -- the
	// day-vs-instant distinction this file's ReadFacts computes only
	// reaches a historical answer's temporal label.
	assertQueryScopedToOrgAndSubjects(t, client.queries[len(client.queries)-1].statement)
}

// TestContinuousIntegrationProviderCombinesRunAndRepositoryShapes proves
// the two subject kinds compose in one call: a query naming a ci_run AND a
// repository gets a fact for each, from two separate queries.
func TestContinuousIntegrationProviderCombinesRunAndRepositoryShapes(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: "FROM ci_pipeline_runs", rows: [][]any{{"run-1", "success", "repo-1"}}},
		{match: "FROM cicd_metrics_daily", rows: [][]any{cicdMetricsRow("repo-1")}},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactContinuousIntegration)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactContinuousIntegration,
		Subjects: []contextfabric.SubjectRef{
			ciRunSubject("repo-1", "run-1"), repoSubject("repo-1"),
		},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 2 {
		t.Fatalf("facts = %#v, want 2 (one per subject kind)", result.Facts)
	}
}

func TestContinuousIntegrationProviderHappyPath(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM ci_pipeline_runs", rows: [][]any{{"run-1", "success", "repo-1"}}}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactContinuousIntegration)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactContinuousIntegration, Subjects: []contextfabric.SubjectRef{ciRunSubject("repo-1", "run-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 || result.Facts[0].Fields["status"].String == nil || *result.Facts[0].Fields["status"].String != "success" {
		t.Fatalf("facts = %#v", result.Facts)
	}
}

func TestContinuousIntegrationProviderZeroRowSubjectHasNoFactEntry(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM ci_pipeline_runs", rows: nil}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactContinuousIntegration)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactContinuousIntegration, Subjects: []contextfabric.SubjectRef{ciRunSubject("repo-1", "run-404")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 0 || result.State != contextfabric.SourceAvailable {
		t.Fatalf("result = %+v", result)
	}
}

func TestContinuousIntegrationProviderQueryErrorReturnsFactReadFailure(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM ci_pipeline_runs", err: errors.New("boom")}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactContinuousIntegration)
	_, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactContinuousIntegration, Subjects: []contextfabric.SubjectRef{ciRunSubject("repo-1", "run-1")},
	})
	var failure *contextfabric.FactReadFailure
	if !errors.As(err, &failure) || failure.State != contextfabric.SourceUnavailable {
		t.Fatalf("err = %v", err)
	}
}

func TestContinuousIntegrationProviderOrgScoped(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM ci_pipeline_runs", rows: nil}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactContinuousIntegration)
	_, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-5"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactContinuousIntegration, Subjects: []contextfabric.SubjectRef{ciRunSubject("repo-1", "run-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if got := client.orgIDBinding(); got != "org-5" {
		t.Fatalf("org_id binding = %q", got)
	}
	if got := client.idsBinding(); len(got) != 1 || got[0] != "repo-1:run-1" {
		t.Fatalf("ids binding = %#v, want exactly the requested subject", got)
	}
}
