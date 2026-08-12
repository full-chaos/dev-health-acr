package devhealthfacts_test

import (
	"context"
	"errors"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestBlockersProviderHappyPath(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: "FROM work_item_dependencies", rows: [][]any{{"WIDGET-099", "WIDGET-101"}}},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactBlockers)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Kind: contextfabric.FactBlockers, Subjects: []contextfabric.SubjectRef{workItemSubject("WIDGET-101")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %#v, want 1", result.Facts)
	}
	fact := result.Facts[0]
	if fact.Subject.CanonicalID != "work_item:WIDGET-101" {
		t.Fatalf("fact subject = %+v", fact.Subject)
	}
	if fact.Fields["blocked_by_work_item_id"].String == nil || *fact.Fields["blocked_by_work_item_id"].String != "WIDGET-099" {
		t.Fatalf("fields = %#v", fact.Fields)
	}
}

func TestBlockersProviderZeroRowSubjectHasNoFactEntry(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM work_item_dependencies", rows: nil}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactBlockers)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Kind: contextfabric.FactBlockers, Subjects: []contextfabric.SubjectRef{workItemSubject("WIDGET-101")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 0 || result.State != contextfabric.SourceAvailable {
		t.Fatalf("result = %+v", result)
	}
}

func TestBlockersProviderQueryErrorReturnsFactReadFailure(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM work_item_dependencies", err: errors.New("boom")}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactBlockers)
	_, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Kind: contextfabric.FactBlockers, Subjects: []contextfabric.SubjectRef{workItemSubject("WIDGET-101")},
	})
	var failure *contextfabric.FactReadFailure
	if !errors.As(err, &failure) || failure.State != contextfabric.SourceUnavailable {
		t.Fatalf("err = %v", err)
	}
}

func TestBlockersProviderOrgScoped(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM work_item_dependencies", rows: nil}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactBlockers)
	_, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-7"}, contextfabric.FactQuery{
		Kind: contextfabric.FactBlockers, Subjects: []contextfabric.SubjectRef{workItemSubject("WIDGET-101")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if got := client.orgIDBinding(); got != "org-7" {
		t.Fatalf("org_id binding = %q", got)
	}
	if got := client.idsBinding(); len(got) != 1 || got[0] != "WIDGET-101" {
		t.Fatalf("ids binding = %#v, want exactly the requested subject", got)
	}
}

func TestRequiredChildrenProviderHappyPath(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: "FROM work_item_dependencies", rows: [][]any{{"WIDGET-101", "WIDGET-200", "related_to"}}},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactRequiredChildren)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Kind: contextfabric.FactRequiredChildren, Subjects: []contextfabric.SubjectRef{workItemSubject("WIDGET-101")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %#v, want 1", result.Facts)
	}
	fact := result.Facts[0]
	if fact.Fields["required_child_work_item_id"].String == nil || *fact.Fields["required_child_work_item_id"].String != "WIDGET-200" {
		t.Fatalf("fields = %#v", fact.Fields)
	}
	if fact.Fields["relationship_type"].String == nil || *fact.Fields["relationship_type"].String != "related_to" {
		t.Fatalf("fields = %#v", fact.Fields)
	}
}

func TestRequiredChildrenProviderZeroRowSubjectHasNoFactEntry(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM work_item_dependencies", rows: nil}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactRequiredChildren)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Kind: contextfabric.FactRequiredChildren, Subjects: []contextfabric.SubjectRef{workItemSubject("WIDGET-101")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 0 || result.State != contextfabric.SourceAvailable {
		t.Fatalf("result = %+v", result)
	}
}

func TestRequiredChildrenProviderQueryErrorReturnsFactReadFailure(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM work_item_dependencies", err: errors.New("boom")}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactRequiredChildren)
	_, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Kind: contextfabric.FactRequiredChildren, Subjects: []contextfabric.SubjectRef{workItemSubject("WIDGET-101")},
	})
	var failure *contextfabric.FactReadFailure
	if !errors.As(err, &failure) || failure.State != contextfabric.SourceUnavailable {
		t.Fatalf("err = %v", err)
	}
}
