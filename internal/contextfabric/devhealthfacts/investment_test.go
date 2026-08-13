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

func investmentRow(teamID string) []any {
	return []any{teamID, "product", "growth", "2026-02-22", int64(30), int64(12), int64(4), int64(850), float64(18.5)}
}

func TestInvestmentProviderHappyPath(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM investment_metrics_daily", rows: [][]any{investmentRow("CHAOS")}}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactInvestment)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{teamSubject("CHAOS")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %#v, want 1", result.Facts)
	}
	fact := result.Facts[0]
	if fact.Fields["investment_area"].String == nil || *fact.Fields["investment_area"].String != "product" {
		t.Fatalf("fields = %#v", fact.Fields)
	}
	if fact.Fields["delivery_units"].Integer == nil || *fact.Fields["delivery_units"].Integer != 30 {
		t.Fatalf("fields = %#v", fact.Fields)
	}
}

// TestInvestmentProviderMultipleAreasProduceMultipleFacts proves one team
// can carry several (investment_area, project_stream) facts at once -- this
// provider is a passthrough, not a summary.
func TestInvestmentProviderMultipleAreasProduceMultipleFacts(t *testing.T) {
	t.Parallel()
	rowB := investmentRow("CHAOS")
	rowB[1] = "quality"
	client := &fakeClient{tables: []fakeTable{{match: "FROM investment_metrics_daily", rows: [][]any{investmentRow("CHAOS"), rowB}}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactInvestment)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{teamSubject("CHAOS")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 2 {
		t.Fatalf("facts = %#v, want 2", result.Facts)
	}
}

func TestInvestmentProviderZeroRowSubjectHasNoFactEntry(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM investment_metrics_daily", rows: nil}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactInvestment)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{teamSubject("ghost")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 0 || result.State != contextfabric.SourceAvailable {
		t.Fatalf("result = %+v", result)
	}
}

func TestInvestmentProviderQueryErrorReturnsFactReadFailure(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM investment_metrics_daily", err: errors.New("boom")}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactInvestment)
	_, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{teamSubject("CHAOS")},
	})
	var failure *contextfabric.FactReadFailure
	if !errors.As(err, &failure) || failure.State != contextfabric.SourceUnavailable {
		t.Fatalf("err = %v", err)
	}
}

func TestInvestmentProviderScopedToOrgAndRequestedSubjects(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM investment_metrics_daily", rows: nil}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactInvestment)
	_, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-8"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{teamSubject("CHAOS")},
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

// TestInvestmentProviderRowForUnrequestedTeamNeverAppears is the F5
// result-content guard.
func TestInvestmentProviderRowForUnrequestedTeamNeverAppears(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM investment_metrics_daily", rows: [][]any{investmentRow("other-team")}}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactInvestment)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{teamSubject("CHAOS")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 0 {
		t.Fatalf("facts = %#v, want empty -- the returned row belongs to an unrequested team", result.Facts)
	}
}

const maxInvestmentRowsPerQueryForTest = 200

func investmentRows(n int) [][]any {
	rows := make([][]any, n)
	for i := 0; i < n; i++ {
		rows[i] = investmentRow("CHAOS")
		rows[i][2] = "stream-" + strconv.Itoa(i)
	}
	return rows
}

func TestInvestmentProviderTruncatesWhenRowCountReachesLimit(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM investment_metrics_daily", rows: investmentRows(maxInvestmentRowsPerQueryForTest)}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactInvestment)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{teamSubject("CHAOS")},
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
