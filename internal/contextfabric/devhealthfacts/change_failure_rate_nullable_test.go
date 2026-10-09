package devhealthfacts_test

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func readCFR(t *testing.T, rows ...[]any) (contextfabric.FactProviderResult, *fakeClient) {
	t.Helper()
	client := &fakeClient{tables: []fakeTable{{match: "FROM repo_metrics_daily", rows: rows}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactMetrics)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactMetrics, Subjects: []contextfabric.SubjectRef{repoSubject("repo-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	return result, client
}

func cfrRow(day string, present bool) []any {
	row := metricsRow("repo-1")
	row[1] = day
	if !present {
		row[5], row[6] = uint8(0), float64(0)
	}
	return row
}

func TestChangeFailureRateStatementDistinguishesNullFromZero(t *testing.T) {
	t.Parallel()
	_, client := readCFR(t, cfrRow("2026-02-21", true))
	st := client.queries[len(client.queries)-1].statement
	for _, want := range []string{"toUInt8(isNotNull(change_failure_rate))", "toFloat64(ifNull(change_failure_rate, 0))", "ifNull(change_failure_rate, -1)"} {
		if !strings.Contains(st, want) {
			t.Fatalf("statement lacks %q", want)
		}
	}
}

func TestChangeFailureRateNullDayServesNoFieldAndNamesCoverage(t *testing.T) {
	t.Parallel()
	result, _ := readCFR(t, cfrRow("2026-02-21", false), cfrRow("2026-02-20", true))
	fact := result.Facts[0]
	rows := fact.Fields["daily_metrics"].Rows
	if _, ok := rows[0].Fields["change_failure_rate"]; ok {
		t.Fatalf("NULL day served change_failure_rate: %#v", rows[0].Fields)
	}
	if v, ok := rows[1].Fields["change_failure_rate"]; !ok || v.Number == nil || *v.Number != 0.1 {
		t.Fatalf("present day lost its value: %#v", rows[1].Fields)
	}
	if _, ok := fact.Fields["change_failure_rate"]; ok {
		t.Fatal("latest day is NULL: the scalar sibling must be absent, not zero")
	}
	if v := fact.Fields["change_failure_rate_days"]; v.Integer == nil || *v.Integer != 1 {
		t.Fatalf("change_failure_rate_days = %#v, want 1", v)
	}
	if !strings.Contains(result.Reason, "change_failure_rate not applicable on 1 of 2 days") {
		t.Fatalf("Reason = %q", result.Reason)
	}
}

func TestChangeFailureRatePresentDaysUnchangedAndSilent(t *testing.T) {
	t.Parallel()
	result, _ := readCFR(t, cfrRow("2026-02-21", true), cfrRow("2026-02-20", true))
	fact := result.Facts[0]
	if v := fact.Fields["change_failure_rate"]; v.Number == nil || *v.Number != 0.1 {
		t.Fatalf("scalar = %#v", v)
	}
	if v := fact.Fields["change_failure_rate_days"]; v.Integer == nil || *v.Integer != 2 {
		t.Fatalf("change_failure_rate_days = %#v, want 2", v)
	}
	if strings.Contains(result.Reason, "change_failure_rate") {
		t.Fatalf("Reason = %q, want no CFR note when every day is present", result.Reason)
	}
}

func TestChangeFailureRateCoverageCountsNullDaysNotPresentDays(t *testing.T) {
	t.Parallel()
	result, _ := readCFR(t, cfrRow("2026-02-21", false), cfrRow("2026-02-20", false), cfrRow("2026-02-19", true))
	if !strings.Contains(result.Reason, "change_failure_rate not applicable on 2 of 3 days") {
		t.Fatalf("Reason = %q", result.Reason)
	}
	if v := result.Facts[0].Fields["change_failure_rate_days"]; v.Integer == nil || *v.Integer != 1 {
		t.Fatalf("change_failure_rate_days = %#v, want 1", v)
	}
}
