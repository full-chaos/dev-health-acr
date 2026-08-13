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

func readinessRow(teamID string) []any {
	return []any{teamID, "scope-1", "linear", "2026-02-22", int64(18), int64(2), int64(20), uint8(1), float64(0.9)}
}

func TestReadinessProviderHappyPath(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM estimate_coverage_metrics_daily", rows: [][]any{readinessRow("CHAOS")}}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactReadiness)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactReadiness, Subjects: []contextfabric.SubjectRef{teamSubject("CHAOS")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %#v, want 1", result.Facts)
	}
	fact := result.Facts[0]
	if fact.Fields["estimate_coverage_ratio"].Number == nil || *fact.Fields["estimate_coverage_ratio"].Number != 0.9 {
		t.Fatalf("fields = %#v", fact.Fields)
	}
	if fact.Fields["backlog_size"].Integer == nil || *fact.Fields["backlog_size"].Integer != 20 {
		t.Fatalf("fields = %#v", fact.Fields)
	}
	// Semantic-honesty guard: a readiness fact must state, in its own
	// structure, that it is backlog estimate coverage, never a general
	// release/ship-readiness verdict (team-lead review requirement).
	if fact.Fields["basis"].String == nil || *fact.Fields["basis"].String != "estimate_coverage" {
		t.Fatalf("fields = %#v, want basis=estimate_coverage", fact.Fields)
	}
}

func TestReadinessProviderNoRatioOmitsField(t *testing.T) {
	t.Parallel()
	row := readinessRow("CHAOS")
	row[7] = uint8(0)
	row[8] = float64(0)
	client := &fakeClient{tables: []fakeTable{{match: "FROM estimate_coverage_metrics_daily", rows: [][]any{row}}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactReadiness)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactReadiness, Subjects: []contextfabric.SubjectRef{teamSubject("CHAOS")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if _, ok := result.Facts[0].Fields["estimate_coverage_ratio"]; ok {
		t.Fatalf("fields = %#v, want estimate_coverage_ratio omitted", result.Facts[0].Fields)
	}
}

func TestReadinessProviderZeroRowSubjectHasNoFactEntry(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM estimate_coverage_metrics_daily", rows: nil}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactReadiness)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactReadiness, Subjects: []contextfabric.SubjectRef{teamSubject("ghost")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 0 || result.State != contextfabric.SourceAvailable {
		t.Fatalf("result = %+v", result)
	}
}

func TestReadinessProviderQueryErrorReturnsFactReadFailure(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM estimate_coverage_metrics_daily", err: errors.New("boom")}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactReadiness)
	_, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactReadiness, Subjects: []contextfabric.SubjectRef{teamSubject("CHAOS")},
	})
	var failure *contextfabric.FactReadFailure
	if !errors.As(err, &failure) || failure.State != contextfabric.SourceUnavailable {
		t.Fatalf("err = %v", err)
	}
}

func TestReadinessProviderScopedToOrgAndRequestedSubjects(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM estimate_coverage_metrics_daily", rows: nil}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactReadiness)
	_, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-8"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactReadiness, Subjects: []contextfabric.SubjectRef{teamSubject("CHAOS")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if got := client.orgIDBinding(); got != "org-8" {
		t.Fatalf("org_id binding = %q", got)
	}
	if got := client.idsBinding(); len(got) != 1 || got[0] != "CHAOS" {
		t.Fatalf("ids binding = %#v, want exactly the requested subject", got)
	}
	assertQueryScopedToOrgAndSubjects(t, client.queries[len(client.queries)-1].statement)
}

// TestReadinessProviderRowForUnrequestedTeamNeverAppears is the F5
// result-content guard.
func TestReadinessProviderRowForUnrequestedTeamNeverAppears(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM estimate_coverage_metrics_daily", rows: [][]any{readinessRow("other-team")}}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactReadiness)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactReadiness, Subjects: []contextfabric.SubjectRef{teamSubject("CHAOS")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 0 {
		t.Fatalf("facts = %#v, want empty -- the returned row belongs to an unrequested team", result.Facts)
	}
}

const maxReadinessRowsPerQueryForTest = 200

func readinessRows(n int) [][]any {
	rows := make([][]any, n)
	for i := 0; i < n; i++ {
		rows[i] = readinessRow("CHAOS")
		rows[i][1] = "scope-" + strconv.Itoa(i)
	}
	return rows
}

func TestReadinessProviderTruncatesWhenRowCountReachesLimit(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM estimate_coverage_metrics_daily", rows: readinessRows(maxReadinessRowsPerQueryForTest)}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactReadiness)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactReadiness, Subjects: []contextfabric.SubjectRef{teamSubject("CHAOS")},
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
