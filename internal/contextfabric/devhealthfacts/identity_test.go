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

func repoSubject(id string) contextfabric.SubjectRef {
	return contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:" + id, Label: id}
}

// workItemSubject mints a CHAOS-3898 "work_item.v2:<repo_id>:<id>" subject
// via identity.Derive itself, rather than hand-building the string -- so
// this test package can never drift from the same codec devhealthsource's
// producers and devhealthfacts' v2Index use.
func workItemSubject(repoID, id string) contextfabric.SubjectRef {
	canonicalID, omitted, err := identity.Derive(identity.KindWorkItem, []string{repoID, id}, nil)
	if err != nil || omitted {
		panic(fmt.Sprintf("workItemSubject(%q, %q): identity.Derive failed: omitted=%v err=%v", repoID, id, omitted, err))
	}
	return contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: canonicalID, Label: id}
}

func TestIdentityProviderHappyPath(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: "FROM repos", rows: [][]any{{"repo-1", "example-org/widget-service", "synthetic"}}},
		{match: "FROM work_items", rows: [][]any{{"WIDGET-101", "Investigate checkout flake", "repo-1"}}},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactIdentity)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time:     contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind:     contextfabric.FactIdentity,
		Subjects: []contextfabric.SubjectRef{repoSubject("repo-1"), workItemSubject("repo-1", "WIDGET-101")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if result.State != contextfabric.SourceAvailable || result.Version == "" {
		t.Fatalf("result = %+v", result)
	}
	if len(result.Facts) != 2 {
		t.Fatalf("facts = %#v, want 2", result.Facts)
	}
	for _, fact := range result.Facts {
		if fact.Kind != contextfabric.FactIdentity {
			t.Fatalf("fact kind = %q", fact.Kind)
		}
		if len(fact.EvidenceRefIDs) == 0 {
			t.Fatalf("fact missing evidence ref ids: %+v", fact)
		}
		switch fact.Subject.CanonicalID {
		case "repository:repo-1":
			if fact.Fields["name"].String == nil || *fact.Fields["name"].String != "example-org/widget-service" {
				t.Fatalf("repo identity fields = %#v", fact.Fields)
			}
		case "work_item.v2:repo-1:WIDGET-101":
			if fact.Fields["title"].String == nil || *fact.Fields["title"].String != "Investigate checkout flake" {
				t.Fatalf("work item identity fields = %#v", fact.Fields)
			}
		default:
			t.Fatalf("unexpected subject %q", fact.Subject.CanonicalID)
		}
	}
}

func TestIdentityProviderZeroRowSubjectHasNoFactEntry(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: "FROM repos", rows: nil},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactIdentity)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time:     contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind:     contextfabric.FactIdentity,
		Subjects: []contextfabric.SubjectRef{repoSubject("repo-missing")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 0 {
		t.Fatalf("facts = %#v, want none", result.Facts)
	}
	if result.State != contextfabric.SourceAvailable {
		t.Fatalf("state = %q, want available (zero rows is not an error)", result.State)
	}
}

func TestIdentityProviderQueryErrorReturnsFactReadFailure(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: "FROM repos", err: errors.New("clickhouse: connection reset")},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactIdentity)
	_, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time:     contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind:     contextfabric.FactIdentity,
		Subjects: []contextfabric.SubjectRef{repoSubject("repo-1")},
	})
	var failure *contextfabric.FactReadFailure
	if !errors.As(err, &failure) {
		t.Fatalf("err = %v, want *contextfabric.FactReadFailure", err)
	}
	if failure.State != contextfabric.SourceUnavailable {
		t.Fatalf("failure state = %q", failure.State)
	}
}

func TestIdentityProviderOrgScopedToRequestedSubjectsOnly(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: "FROM repos", rows: [][]any{{"repo-1", "example-org/widget-service", "synthetic"}}},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactIdentity)
	_, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time:     contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind:     contextfabric.FactIdentity,
		Subjects: []contextfabric.SubjectRef{repoSubject("repo-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if got := client.orgIDBinding(); got != "org-1" {
		t.Fatalf("org_id binding = %q, want org-1", got)
	}
	if got := client.idsBinding(); len(got) != 1 || got[0] != "repo-1" {
		t.Fatalf("ids binding = %#v, want [repo-1] (must not scan the whole org)", got)
	}
}

func TestMembershipProviderWorkItemReportsRepository(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: "FROM work_items", rows: [][]any{{"WIDGET-101", "repo-1", "example-org/widget-service"}}},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactMembership)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time:     contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind:     contextfabric.FactMembership,
		Subjects: []contextfabric.SubjectRef{workItemSubject("repo-1", "WIDGET-101")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %#v, want 1", result.Facts)
	}
	fact := result.Facts[0]
	if fact.Fields["repository_id"].String == nil || *fact.Fields["repository_id"].String != "repo-1" {
		t.Fatalf("membership fields = %#v", fact.Fields)
	}
	if fact.Fields["repository_name"].String == nil || *fact.Fields["repository_name"].String != "example-org/widget-service" {
		t.Fatalf("membership fields = %#v", fact.Fields)
	}
}

func TestMembershipProviderRepositoryReportsOrganization(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: "FROM repos", rows: [][]any{{"repo-1"}}},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactMembership)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time:     contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind:     contextfabric.FactMembership,
		Subjects: []contextfabric.SubjectRef{repoSubject("repo-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 || result.Facts[0].Fields["organization_id"].String == nil || *result.Facts[0].Fields["organization_id"].String != "org-1" {
		t.Fatalf("facts = %#v", result.Facts)
	}
}
