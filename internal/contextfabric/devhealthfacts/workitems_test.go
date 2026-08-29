package devhealthfacts_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestStatusProviderHappyPath(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: "FROM work_items", rows: [][]any{{"WIDGET-101", "in_progress", "repo-1"}}},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactStatus)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactStatus, Subjects: []contextfabric.SubjectRef{workItemSubject("repo-1", "WIDGET-101")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %#v, want 1", result.Facts)
	}
	fact := result.Facts[0]
	if fact.Kind != contextfabric.FactStatus || fact.Subject.CanonicalID != "work_item.v2:repo-1:WIDGET-101" {
		t.Fatalf("fact = %+v", fact)
	}
	if fact.Fields["status"].String == nil || *fact.Fields["status"].String != "in_progress" {
		t.Fatalf("fields = %#v", fact.Fields)
	}
}

func TestStatusProviderZeroRowSubjectHasNoFactEntry(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM work_items", rows: nil}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactStatus)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactStatus, Subjects: []contextfabric.SubjectRef{workItemSubject("repo-1", "WIDGET-404")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 0 || result.State != contextfabric.SourceNoData {
		t.Fatalf("result = %+v", result)
	}
}

func TestStatusProviderQueryErrorReturnsFactReadFailure(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM work_items", err: errors.New("boom")}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactStatus)
	_, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactStatus, Subjects: []contextfabric.SubjectRef{workItemSubject("repo-1", "WIDGET-101")},
	})
	var failure *contextfabric.FactReadFailure
	if !errors.As(err, &failure) || failure.State != contextfabric.SourceUnavailable {
		t.Fatalf("err = %v", err)
	}
}

func TestStatusProviderOrgScoped(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM work_items", rows: [][]any{{"WIDGET-101", "open", "repo-1"}, {"WIDGET-102", "open", "repo-1"}}}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactStatus)
	_, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-9"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactStatus, Subjects: []contextfabric.SubjectRef{workItemSubject("repo-1", "WIDGET-101")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if got := client.orgIDBinding(); got != "org-9" {
		t.Fatalf("org_id binding = %q", got)
	}
	if got := client.idsBinding(); len(got) != 1 || got[0] != "repo-1:WIDGET-101" {
		t.Fatalf("ids binding = %#v, want exactly the requested subject", got)
	}
}

func TestStatusProviderEmptyStatusIsNull(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM work_items", rows: [][]any{{"WIDGET-101", "", "repo-1"}}}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactStatus)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactStatus, Subjects: []contextfabric.SubjectRef{workItemSubject("repo-1", "WIDGET-101")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if !result.Facts[0].Fields["status"].Null {
		t.Fatalf("fields = %#v, want null status", result.Facts[0].Fields)
	}
}

func TestWorkProviderHappyPath(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM work_items", rows: [][]any{{"WIDGET-101", "Investigate checkout flake", "repo-1"}}}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactWork)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactWork, Subjects: []contextfabric.SubjectRef{workItemSubject("repo-1", "WIDGET-101")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 || result.Facts[0].Fields["title"].String == nil || *result.Facts[0].Fields["title"].String != "Investigate checkout flake" {
		t.Fatalf("facts = %#v", result.Facts)
	}
}

func TestActualCompletionProviderCompleted(t *testing.T) {
	t.Parallel()
	completedAt := time.Date(2026, 1, 14, 12, 0, 0, 0, time.UTC)
	client := &fakeClient{tables: []fakeTable{{match: "FROM work_items", rows: [][]any{{"WIDGET-101", uint8(1), completedAt, "repo-1"}}}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactActualCompletion)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactActualCompletion, Subjects: []contextfabric.SubjectRef{workItemSubject("repo-1", "WIDGET-101")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %#v", result.Facts)
	}
	fact := result.Facts[0]
	if fact.Fields["completed"].Boolean == nil || !*fact.Fields["completed"].Boolean {
		t.Fatalf("fields = %#v, want completed=true", fact.Fields)
	}
	if fact.Fields["completed_at"].String == nil || *fact.Fields["completed_at"].String != completedAt.Format(time.RFC3339) {
		t.Fatalf("fields = %#v", fact.Fields)
	}
}

func TestActualCompletionProviderNotCompletedOmitsCompletedAt(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM work_items", rows: [][]any{{"WIDGET-101", uint8(0), time.Unix(0, 0).UTC(), "repo-1"}}}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactActualCompletion)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactActualCompletion, Subjects: []contextfabric.SubjectRef{workItemSubject("repo-1", "WIDGET-101")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	fact := result.Facts[0]
	if fact.Fields["completed"].Boolean == nil || *fact.Fields["completed"].Boolean {
		t.Fatalf("fields = %#v, want completed=false", fact.Fields)
	}
	if _, ok := fact.Fields["completed_at"]; ok {
		t.Fatalf("fields = %#v, want no completed_at when not completed", fact.Fields)
	}
}
