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
