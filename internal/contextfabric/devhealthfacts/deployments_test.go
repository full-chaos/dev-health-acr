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
	if len(result.Facts) != 0 || result.State != contextfabric.SourceAvailable {
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
