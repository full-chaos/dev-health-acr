package devhealthfacts_test

import (
	"context"
	"errors"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func incidentSubject(id string) contextfabric.SubjectRef {
	return contextfabric.SubjectRef{Kind: contextfabric.SubjectIncident, CanonicalID: "incident:" + id, Label: id}
}

func TestIncidentsProviderHappyPath(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: "FROM operational_incidents", rows: [][]any{{"incident-1", "open", "high"}}},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactIncidents)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Kind: contextfabric.FactIncidents, Subjects: []contextfabric.SubjectRef{incidentSubject("incident-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %#v, want 1", result.Facts)
	}
	fact := result.Facts[0]
	if fact.Fields["status"].String == nil || *fact.Fields["status"].String != "open" {
		t.Fatalf("fields = %#v", fact.Fields)
	}
	if fact.Fields["severity"].String == nil || *fact.Fields["severity"].String != "high" {
		t.Fatalf("fields = %#v", fact.Fields)
	}
}

func TestIncidentsProviderZeroRowSubjectHasNoFactEntry(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM operational_incidents", rows: nil}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactIncidents)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Kind: contextfabric.FactIncidents, Subjects: []contextfabric.SubjectRef{incidentSubject("incident-404")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 0 || result.State != contextfabric.SourceAvailable {
		t.Fatalf("result = %+v", result)
	}
}

func TestIncidentsProviderQueryErrorReturnsFactReadFailure(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM operational_incidents", err: errors.New("boom")}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactIncidents)
	_, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Kind: contextfabric.FactIncidents, Subjects: []contextfabric.SubjectRef{incidentSubject("incident-1")},
	})
	var failure *contextfabric.FactReadFailure
	if !errors.As(err, &failure) || failure.State != contextfabric.SourceUnavailable {
		t.Fatalf("err = %v", err)
	}
}

func TestIncidentsProviderOrgScoped(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM operational_incidents", rows: nil}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactIncidents)
	_, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-8"}, contextfabric.FactQuery{
		Kind: contextfabric.FactIncidents, Subjects: []contextfabric.SubjectRef{incidentSubject("incident-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if got := client.orgIDBinding(); got != "org-8" {
		t.Fatalf("org_id binding = %q", got)
	}
	if got := client.idsBinding(); len(got) != 1 || got[0] != "incident-1" {
		t.Fatalf("ids binding = %#v, want exactly the requested subject", got)
	}
}
