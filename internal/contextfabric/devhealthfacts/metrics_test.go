package devhealthfacts_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func metricsRow(repoID string) []any {
	return []any{repoID, "2026-02-21", int64(42), int64(7), float64(12.5), float64(0.1), uint8(1), float64(3.5), int64(4), float64(0.2)}
}

func TestMetricsProviderHappyPath(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: "FROM repo_metrics_daily", rows: [][]any{metricsRow("repo-1")}},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactMetrics)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactMetrics, Subjects: []contextfabric.SubjectRef{repoSubject("repo-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %#v, want 1", result.Facts)
	}
	fact := result.Facts[0]
	if fact.Fields["commits_count"].Integer == nil || *fact.Fields["commits_count"].Integer != 42 {
		t.Fatalf("fields = %#v", fact.Fields)
	}
	if fact.Fields["mttr_hours"].Number == nil || *fact.Fields["mttr_hours"].Number != 3.5 {
		t.Fatalf("fields = %#v", fact.Fields)
	}
}

func TestMetricsProviderNoMTTROmitsField(t *testing.T) {
	t.Parallel()
	row := metricsRow("repo-1")
	row[6] = uint8(0)
	row[7] = float64(0)
	client := &fakeClient{tables: []fakeTable{{match: "FROM repo_metrics_daily", rows: [][]any{row}}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactMetrics)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactMetrics, Subjects: []contextfabric.SubjectRef{repoSubject("repo-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if _, ok := result.Facts[0].Fields["mttr_hours"]; ok {
		t.Fatalf("fields = %#v, want mttr_hours omitted", result.Facts[0].Fields)
	}
}

func TestMetricsProviderZeroRowSubjectHasNoFactEntry(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM repo_metrics_daily", rows: nil}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactMetrics)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactMetrics, Subjects: []contextfabric.SubjectRef{repoSubject("repo-404")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 0 || result.State != contextfabric.SourceAvailable {
		t.Fatalf("result = %+v", result)
	}
}

func TestMetricsProviderQueryErrorReturnsFactReadFailure(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM repo_metrics_daily", err: errors.New("boom")}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactMetrics)
	_, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactMetrics, Subjects: []contextfabric.SubjectRef{repoSubject("repo-1")},
	})
	var failure *contextfabric.FactReadFailure
	if !errors.As(err, &failure) || failure.State != contextfabric.SourceUnavailable {
		t.Fatalf("err = %v", err)
	}
}

// TestMetricsProviderScopedToOrgAndRequestedSubjects is the guard-sensitive
// org-scope AND subject-scope test (AC-3780-2/AC-3780-5): it checks both the
// captured bindings and the statement text, so it fails if either guard is
// removed, not just if the wrong value is bound.
func TestMetricsProviderScopedToOrgAndRequestedSubjects(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM repo_metrics_daily", rows: nil}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactMetrics)
	_, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-8"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactMetrics, Subjects: []contextfabric.SubjectRef{repoSubject("repo-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if got := client.orgIDBinding(); got != "org-8" {
		t.Fatalf("org_id binding = %q", got)
	}
	if got := client.idsBinding(); len(got) != 1 || got[0] != "repo-1" {
		t.Fatalf("ids binding = %#v, want exactly the requested subject", got)
	}
	assertQueryScopedToOrgAndSubjects(t, client.queries[len(client.queries)-1].statement)
}

// TestMetricsProviderRowForUnrequestedRepositoryNeverAppears is the F5
// result-content guard: even though the fake client can return a row for
// ANY repository (it doesn't execute the SQL's own org/id filters), the
// provider itself must never surface a fact for a subject the caller did
// not ask about.
func TestMetricsProviderRowForUnrequestedRepositoryNeverAppears(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM repo_metrics_daily", rows: [][]any{metricsRow("repo-other-org")}}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactMetrics)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactMetrics, Subjects: []contextfabric.SubjectRef{repoSubject("repo-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	for _, fact := range result.Facts {
		if fact.Subject.CanonicalID == "repository:repo-other-org" {
			t.Fatalf("facts = %#v, want no fact for the unrequested repository", result.Facts)
		}
	}
	if len(result.Facts) != 0 {
		t.Fatalf("facts = %#v, want empty", result.Facts)
	}
}

const maxMetricsRowsPerQueryForTest = 200

func metricsRows(n int) [][]any {
	rows := make([][]any, n)
	for i := 0; i < n; i++ {
		rows[i] = metricsRow("repo-" + strconv.Itoa(i))
	}
	return rows
}

func TestMetricsProviderTruncatesWhenRowCountReachesLimit(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM repo_metrics_daily", rows: metricsRows(maxMetricsRowsPerQueryForTest)}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactMetrics)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactMetrics, Subjects: []contextfabric.SubjectRef{repoSubject("repo-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if !result.Truncated {
		t.Fatalf("result.Truncated = false, want true when the row count reaches the limit")
	}
	if len(client.queries) == 0 || !strings.Contains(strings.ToUpper(client.queries[len(client.queries)-1].statement), "LIMIT") {
		t.Fatalf("query statement = %#v, want a LIMIT clause", client.queries)
	}
}

func TestMetricsProviderNotTruncatedBelowLimit(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM repo_metrics_daily", rows: metricsRows(maxMetricsRowsPerQueryForTest - 1)}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactMetrics)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactMetrics, Subjects: []contextfabric.SubjectRef{repoSubject("repo-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if result.Truncated {
		t.Fatalf("result.Truncated = true, want false when the row count is below the limit")
	}
}
