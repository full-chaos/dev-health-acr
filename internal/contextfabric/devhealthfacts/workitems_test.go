package devhealthfacts_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestWorkItemProvidersUseCurrentSelectorScopeAndThrowingSettings(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name contextfabric.FactKind
		rows [][]any
	}{
		{name: contextfabric.FactStatus, rows: [][]any{{"WI-1", "in_progress", "repo-b"}}},
		{name: contextfabric.FactWork, rows: [][]any{{"WI-1", "A scoped title", "repo-b"}}},
		{name: contextfabric.FactActualCompletion, rows: [][]any{{"WI-1", uint8(1), time.Date(2026, 1, 14, 12, 0, 0, 0, time.UTC), "repo-b"}}},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(string(tt.name), func(t *testing.T) {
			t.Parallel()
			client := &fakeClient{tables: []fakeTable{{match: "FROM work_items", rows: tt.rows}}}
			provider := findProvider(t, devhealthfacts.NewProviders(client), tt.name)
			query := contextfabric.FactQuery{
				Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
				Kind: tt.name, Subjects: []contextfabric.SubjectRef{workItemSubject("repo-b", "WI-1")},
				RequestedRepositoryScope: []string{" ACME/B ", "acme/c"},
			}
			principal := storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"acme/a", " ACME/B "}}
			result, err := provider.ReadFacts(context.Background(), principal, query)
			if err != nil {
				t.Fatalf("ReadFacts() error = %v", err)
			}
			if len(result.Facts) != 1 {
				t.Fatalf("facts = %#v, want one scanned fact", result.Facts)
			}
			if len(client.queries) != 1 {
				t.Fatalf("query count = %d, want one scoped content query and no metadata lookup", len(client.queries))
			}
			statement := client.queries[0].statement
			for _, fragment := range []string{
				"LEFT JOIN repos AS r FINAL ON r.id = w.repo_id AND r.org_id = w.org_id",
				"authorized_repo_slugs",
				"requested_repo_slugs",
				"SETTINGS",
				"timeout_overflow_mode = 'throw'",
				"read_overflow_mode = 'throw'",
				"result_overflow_mode = 'throw'",
			} {
				if !strings.Contains(statement, fragment) {
					t.Errorf("statement = %q, want %q", statement, fragment)
				}
			}
			if got := testBindingValue(t, client, "authorized_repo_slugs"); !reflect.DeepEqual(got, []string{"acme/a", "acme/b"}) {
				t.Errorf("authorized_repo_slugs = %#v, want normalized principal selectors", got)
			}
			if got := testBindingValue(t, client, "requested_repo_slugs"); !reflect.DeepEqual(got, []string{"acme/b", "acme/c"}) {
				t.Errorf("requested_repo_slugs = %#v, want normalized request selectors", got)
			}
			if got := testBindingValue(t, client, "authorized_repo_owners"); !reflect.DeepEqual(got, []string{}) {
				t.Errorf("authorized_repo_owners = %#v, want no owner selectors", got)
			}
			if got := testBindingValue(t, client, "requested_repo_owners"); !reflect.DeepEqual(got, []string{}) {
				t.Errorf("requested_repo_owners = %#v, want no owner selectors", got)
			}

			// The provider must build this scope from each current query. A
			// second request with a different selector cannot inherit the
			// first request's bindings.
			query.RequestedRepositoryScope = []string{"acme/c"}
			if _, err := provider.ReadFacts(context.Background(), principal, query); err != nil {
				t.Fatalf("second ReadFacts() error = %v", err)
			}
			if got := testBindingValueAt(t, client, 1, "requested_repo_slugs"); !reflect.DeepEqual(got, []string{"acme/c"}) {
				t.Fatalf("second requested_repo_slugs = %#v, want only current query selector", got)
			}
		})
	}
}

func TestWorkItemProvidersRefuseBeforeQueryWhenDeadlineHasNoWholeSecondCeiling(t *testing.T) {
	t.Parallel()
	for _, kind := range []contextfabric.FactKind{
		contextfabric.FactStatus,
		contextfabric.FactWork,
		contextfabric.FactActualCompletion,
	} {
		kind := kind
		t.Run(string(kind), func(t *testing.T) {
			t.Parallel()
			client := &fakeClient{}
			provider := findProvider(t, devhealthfacts.NewProviders(client), kind)
			ctx, cancel := context.WithTimeout(context.Background(), 900*time.Millisecond)
			defer cancel()
			_, err := provider.ReadFacts(ctx, storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
				Time:     contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
				Kind:     kind,
				Subjects: []contextfabric.SubjectRef{workItemSubject("repo-1", "WI-1")},
			})
			var failure *contextfabric.FactReadFailure
			if !errors.As(err, &failure) || failure.State != contextfabric.SourceUnavailable {
				t.Fatalf("ReadFacts() error = %v, want SourceUnavailable FactReadFailure", err)
			}
			if len(client.queries) != 0 {
				t.Fatalf("query count = %d, want zero because no positive whole-second server ceiling fits", len(client.queries))
			}
		})
	}
}

func TestWorkItemProvidersDoNotQueryWithCanceledOrExpiredContext(t *testing.T) {
	t.Parallel()
	contexts := []struct {
		name string
		new  func() (context.Context, context.CancelFunc)
	}{
		{
			name: "canceled",
			new: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx, cancel
			},
		},
		{
			name: "expired",
			new: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(time.Millisecond))
				<-ctx.Done()
				return ctx, cancel
			},
		},
	}
	for _, tc := range contexts {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := tc.new()
			defer cancel()
			for _, kind := range []contextfabric.FactKind{
				contextfabric.FactStatus,
				contextfabric.FactWork,
				contextfabric.FactActualCompletion,
			} {
				kind := kind
				t.Run(string(kind), func(t *testing.T) {
					t.Parallel()
					client := &fakeClient{}
					provider := findProvider(t, devhealthfacts.NewProviders(client), kind)
					_, err := provider.ReadFacts(ctx, storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
						Time:     contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
						Kind:     kind,
						Subjects: []contextfabric.SubjectRef{workItemSubject("repo-1", "WI-1")},
					})
					var failure *contextfabric.FactReadFailure
					if !errors.As(err, &failure) || failure.State != contextfabric.SourceUnavailable {
						t.Fatalf("ReadFacts() error = %v, want SourceUnavailable FactReadFailure", err)
					}
					if len(client.queries) != 0 {
						t.Fatalf("query count = %d, want zero for %s context", len(client.queries), tc.name)
					}
				})
			}
		})
	}
}

func testBindingValue(t *testing.T, client *fakeClient, name string) any {
	t.Helper()
	return testBindingValueAt(t, client, len(client.queries)-1, name)
}

func testBindingValueAt(t *testing.T, client *fakeClient, queryIndex int, name string) any {
	t.Helper()
	if queryIndex < 0 || queryIndex >= len(client.queries) {
		t.Fatalf("query index %d out of range for %d queries", queryIndex, len(client.queries))
	}
	for _, binding := range client.queries[queryIndex].bindings {
		if binding.Name == name {
			return binding.Value
		}
	}
	t.Fatalf("binding %q not found in query %d", name, queryIndex)
	return nil
}

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
