package devhealthfacts_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// deploymentSubject mints a CHAOS-3898 "deployment.v2:<repo_id>:<id>"
// subject via identity.Derive, mirroring workItemSubject's rationale
// (identity_test.go).
func deploymentSubject(repoID, id string) contextfabric.SubjectRef {
	canonicalID, omitted, err := identity.Derive(identity.KindDeployment, []string{repoID, id}, nil)
	if err != nil || omitted {
		panic(fmt.Sprintf("deploymentSubject(%q, %q): identity.Derive failed: omitted=%v err=%v", repoID, id, omitted, err))
	}
	return contextfabric.SubjectRef{Kind: contextfabric.SubjectDeployment, CanonicalID: canonicalID, Label: id}
}

// deployMetricsRow shapes one deploy_metrics_daily aggregate row:
// (repo_id, day, deployments_count, failed_deployments_count,
// hasDeployTime, deploy_time_p50_hours, hasLeadTime, lead_time_p50_hours).
func deployMetricsRow(repoID string) []any {
	return []any{repoID, "2026-02-21", int64(12), int64(2), uint8(1), float64(1.5), uint8(1), float64(3.0)}
}

// TestDeploymentsProviderRepositoryAggregateHappyPath is CHAOS-4347's
// repository-scoped widening: a genuinely repository-scoped aggregate read
// of deploy_metrics_daily, distinct from the per-deployment status shape.
func TestDeploymentsProviderRepositoryAggregateHappyPath(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: "FROM deploy_metrics_daily", rows: [][]any{deployMetricsRow("repo-1")}},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactDeployments)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactDeployments, Subjects: []contextfabric.SubjectRef{repoSubject("repo-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %#v, want 1", result.Facts)
	}
	fact := result.Facts[0]
	if fact.Fields["deployments_count"].Integer == nil || *fact.Fields["deployments_count"].Integer != 12 {
		t.Fatalf("deployments_count = %#v", fact.Fields["deployments_count"])
	}
	if fact.Fields["failed_deployments_count"].Integer == nil || *fact.Fields["failed_deployments_count"].Integer != 2 {
		t.Fatalf("failed_deployments_count = %#v", fact.Fields["failed_deployments_count"])
	}
	if fact.Fields["deploy_time_p50_hours"].Number == nil || *fact.Fields["deploy_time_p50_hours"].Number != 1.5 {
		t.Fatalf("deploy_time_p50_hours = %#v", fact.Fields["deploy_time_p50_hours"])
	}
	// Grain is NOT asserted here: on the current axis, timebound.go's
	// effectiveGrain always reports GrainInstant by construction ("now"
	// needs no bucket) regardless of the provider's own grain -- see
	// ci_test.go's identical comment.
	assertQueryScopedToOrgAndSubjects(t, client.queries[len(client.queries)-1].statement)
}

// TestDeploymentsProviderRepositoryAggregateNoNullDurationsOmitsFields pins
// the nullable-column contract deploy_metrics_daily's own
// deploy_time_p50_hours/lead_time_p50_hours declare.
func TestDeploymentsProviderRepositoryAggregateNoNullDurationsOmitsFields(t *testing.T) {
	t.Parallel()
	row := deployMetricsRow("repo-1")
	row[4], row[5] = uint8(0), float64(0)
	row[6], row[7] = uint8(0), float64(0)
	client := &fakeClient{tables: []fakeTable{{match: "FROM deploy_metrics_daily", rows: [][]any{row}}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactDeployments)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactDeployments, Subjects: []contextfabric.SubjectRef{repoSubject("repo-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if _, ok := result.Facts[0].Fields["deploy_time_p50_hours"]; ok {
		t.Fatalf("fields = %#v, want deploy_time_p50_hours omitted", result.Facts[0].Fields)
	}
	if _, ok := result.Facts[0].Fields["lead_time_p50_hours"]; ok {
		t.Fatalf("fields = %#v, want lead_time_p50_hours omitted", result.Facts[0].Fields)
	}
}

func TestDeploymentsProviderHappyPath(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: "FROM deployments", rows: [][]any{{"deploy-1", "success", "production", "repo-1"}}},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactDeployments)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactDeployments, Subjects: []contextfabric.SubjectRef{deploymentSubject("repo-1", "deploy-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %#v, want 1", result.Facts)
	}
	fact := result.Facts[0]
	if fact.Fields["status"].String == nil || *fact.Fields["status"].String != "success" {
		t.Fatalf("fields = %#v", fact.Fields)
	}
	if fact.Fields["environment"].String == nil || *fact.Fields["environment"].String != "production" {
		t.Fatalf("fields = %#v", fact.Fields)
	}
}

func TestDeploymentsProviderZeroRowSubjectHasNoFactEntry(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM deployments", rows: nil}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactDeployments)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactDeployments, Subjects: []contextfabric.SubjectRef{deploymentSubject("repo-1", "deploy-404")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 0 || result.State != contextfabric.SourceNoData {
		t.Fatalf("result = %+v", result)
	}
}

func TestDeploymentsProviderQueryErrorReturnsFactReadFailure(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM deployments", err: errors.New("boom")}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactDeployments)
	_, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactDeployments, Subjects: []contextfabric.SubjectRef{deploymentSubject("repo-1", "deploy-1")},
	})
	var failure *contextfabric.FactReadFailure
	if !errors.As(err, &failure) || failure.State != contextfabric.SourceUnavailable {
		t.Fatalf("err = %v", err)
	}
}

func TestDeploymentsProviderOrgScoped(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM deployments", rows: nil}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactDeployments)
	_, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-6"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactDeployments, Subjects: []contextfabric.SubjectRef{deploymentSubject("repo-1", "deploy-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if got := client.orgIDBinding(); got != "org-6" {
		t.Fatalf("org_id binding = %q", got)
	}
	if got := client.idsBinding(); len(got) != 1 || got[0] != "repo-1:deploy-1" {
		t.Fatalf("ids binding = %#v, want exactly the requested subject", got)
	}
}
