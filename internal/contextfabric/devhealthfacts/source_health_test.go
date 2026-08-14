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

func sourceHealthRow(provider string) []any {
	// duration_ms is uint64, matching the production column.
	return []any{provider, "success", int64(412), uint64(9800), "", "2026-08-12 03:00:00"}
}

func TestSourceHealthProviderHappyPath(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM backfill_log", rows: [][]any{sourceHealthRow("github")}}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactSourceHealth)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactSourceHealth, Subjects: []contextfabric.SubjectRef{organizationSubject("org-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %#v, want 1", result.Facts)
	}
	fact := result.Facts[0]
	if fact.Fields["provider"].String == nil || *fact.Fields["provider"].String != "github" {
		t.Fatalf("fields = %#v", fact.Fields)
	}
	if fact.Fields["status"].String == nil || *fact.Fields["status"].String != "success" {
		t.Fatalf("fields = %#v", fact.Fields)
	}
	if _, ok := fact.Fields["error_message"]; ok {
		t.Fatalf("fields = %#v, want error_message omitted when empty", fact.Fields)
	}
}

func TestSourceHealthProviderErrorMessagePresentWhenNonEmpty(t *testing.T) {
	t.Parallel()
	row := sourceHealthRow("github")
	row[4] = "rate limited"
	client := &fakeClient{tables: []fakeTable{{match: "FROM backfill_log", rows: [][]any{row}}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactSourceHealth)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactSourceHealth, Subjects: []contextfabric.SubjectRef{organizationSubject("org-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if result.Facts[0].Fields["error_message"].String == nil || *result.Facts[0].Fields["error_message"].String != "rate limited" {
		t.Fatalf("fields = %#v", result.Facts[0].Fields)
	}
}

func TestSourceHealthProviderZeroRowHasNoFactEntry(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM backfill_log", rows: nil}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactSourceHealth)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactSourceHealth, Subjects: []contextfabric.SubjectRef{organizationSubject("org-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 0 || result.State != contextfabric.SourceAvailable {
		t.Fatalf("result = %+v", result)
	}
}

func TestSourceHealthProviderQueryErrorReturnsFactReadFailure(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM backfill_log", err: errors.New("boom")}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactSourceHealth)
	_, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactSourceHealth, Subjects: []contextfabric.SubjectRef{organizationSubject("org-1")},
	})
	var failure *contextfabric.FactReadFailure
	if !errors.As(err, &failure) || failure.State != contextfabric.SourceUnavailable {
		t.Fatalf("err = %v", err)
	}
}

// TestSourceHealthProviderOrgScoped is the guard-sensitive org-scope test:
// it checks both the org_id binding and the statement text.
func TestSourceHealthProviderOrgScoped(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM backfill_log", rows: nil}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactSourceHealth)
	_, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-8"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactSourceHealth, Subjects: []contextfabric.SubjectRef{organizationSubject("org-8")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if got := client.orgIDBinding(); got != "org-8" {
		t.Fatalf("org_id binding = %q", got)
	}
	if !strings.Contains(client.queries[len(client.queries)-1].statement, "org_id = {org_id:String}") {
		t.Fatalf("statement = %q, want it to filter by org_id = {org_id:String}", client.queries[len(client.queries)-1].statement)
	}
}

// TestSourceHealthProviderRejectsMismatchedOrganizationSubject is this
// provider's subject-scope guard (there is no per-row subject column to
// filter by, so the guard lives in Go, not SQL): a requested organization
// subject that is not the caller's own organization must never be honored,
// and must never even reach ClickHouse.
func TestSourceHealthProviderRejectsMismatchedOrganizationSubject(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM backfill_log", rows: [][]any{sourceHealthRow("github")}}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactSourceHealth)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactSourceHealth, Subjects: []contextfabric.SubjectRef{organizationSubject("org-2")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 0 {
		t.Fatalf("facts = %#v, want empty for a mismatched organization subject", result.Facts)
	}
	if len(client.queries) != 0 {
		t.Fatalf("client.queries = %#v, want no ClickHouse query issued for a mismatched organization subject", client.queries)
	}
}

const maxSourceHealthRowsPerQueryForTest = 200

func sourceHealthRows(n int) [][]any {
	rows := make([][]any, n)
	for i := 0; i < n; i++ {
		rows[i] = sourceHealthRow("provider-" + strconv.Itoa(i))
	}
	return rows
}

func TestSourceHealthProviderTruncatesWhenRowCountReachesLimit(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM backfill_log", rows: sourceHealthRows(maxSourceHealthRowsPerQueryForTest)}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactSourceHealth)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactSourceHealth, Subjects: []contextfabric.SubjectRef{organizationSubject("org-1")},
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
