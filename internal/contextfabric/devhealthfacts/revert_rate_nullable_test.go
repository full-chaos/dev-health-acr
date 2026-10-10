package devhealthfacts_test

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func readRevert(t *testing.T, rows ...[]any) (contextfabric.FactProviderResult, *fakeClient) {
	t.Helper()
	// The statement counts per repository over the whole window; the fake
	// replays that count on every row.
	var present int64
	for _, row := range rows {
		if row[5].(uint8) == 1 {
			present++
		}
	}
	for _, row := range rows {
		row[11], row[12] = int64(len(rows)), present
	}
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

func revertRow(day string, present bool) []any {
	row := metricsRow("repo-1")
	row[1] = day
	if !present {
		row[5], row[6] = uint8(0), float64(0)
	}
	return row
}

func TestRevertRateStatementDistinguishesNullFromZero(t *testing.T) {
	t.Parallel()
	_, client := readRevert(t, revertRow("2026-02-21", true))
	st := client.queries[len(client.queries)-1].statement
	for _, want := range []string{"sum(toInt64(isNotNull(revert_rate))) OVER (PARTITION BY repo_id) AS revert_days", "toUInt8(isNotNull(revert_rate))", "toFloat64(ifNull(revert_rate, 0))", "ifNull(revert_rate, -1)"} {
		if !strings.Contains(st, want) {
			t.Fatalf("statement lacks %q", want)
		}
	}
}

func TestRevertRateNullDayServesNoFieldAndNamesCoverage(t *testing.T) {
	t.Parallel()
	result, _ := readRevert(t, revertRow("2026-02-21", false), revertRow("2026-02-20", true))
	fact := result.Facts[0]
	rows := fact.Fields["daily_metrics"].Rows
	if _, ok := rows[0].Fields["revert_rate"]; ok {
		t.Fatalf("NULL day served revert_rate: %#v", rows[0].Fields)
	}
	if v, ok := rows[1].Fields["revert_rate"]; !ok || v.Number == nil || *v.Number != 0.1 {
		t.Fatalf("present day lost its value: %#v", rows[1].Fields)
	}
	if _, ok := fact.Fields["revert_rate"]; ok {
		t.Fatal("latest day is NULL: the scalar sibling must be absent, not zero")
	}
	if v := fact.Fields["revert_rate_days"]; v.Integer == nil || *v.Integer != 1 {
		t.Fatalf("revert_rate_days = %#v, want 1", v)
	}
	if !strings.Contains(result.Reason, "revert_rate not applicable on 1 of 2 days") {
		t.Fatalf("Reason = %q", result.Reason)
	}
}

func TestRevertRatePresentDaysUnchangedAndSilent(t *testing.T) {
	t.Parallel()
	result, _ := readRevert(t, revertRow("2026-02-21", true), revertRow("2026-02-20", true))
	fact := result.Facts[0]
	if v := fact.Fields["revert_rate"]; v.Number == nil || *v.Number != 0.1 {
		t.Fatalf("scalar = %#v", v)
	}
	if v := fact.Fields["revert_rate_days"]; v.Integer == nil || *v.Integer != 2 {
		t.Fatalf("revert_rate_days = %#v, want 2", v)
	}
	if strings.Contains(result.Reason, "revert_rate") {
		t.Fatalf("Reason = %q, want no CFR note when every day is present", result.Reason)
	}
}

func TestRevertRateCoverageCountsNullDaysNotPresentDays(t *testing.T) {
	t.Parallel()
	result, _ := readRevert(t, revertRow("2026-02-21", false), revertRow("2026-02-20", false), revertRow("2026-02-19", true))
	if !strings.Contains(result.Reason, "revert_rate not applicable on 2 of 3 days") {
		t.Fatalf("Reason = %q", result.Reason)
	}
	if v := result.Facts[0].Fields["revert_rate_days"]; v.Integer == nil || *v.Integer != 1 {
		t.Fatalf("revert_rate_days = %#v, want 1", v)
	}
}

// A window wider than the per-repository row cap: the query counts the
// present days over the whole window, so the 50 oldest NULL days the cap cut
// off are still named.
func TestRevertRateCoverageCountsDaysBeyondTheRowCap(t *testing.T) {
	t.Parallel()
	row := revertRow("2026-02-21", true)
	client := &fakeClient{tables: []fakeTable{{match: "FROM repo_metrics_daily", rows: [][]any{row}}}}
	row[11], row[12] = int64(250), int64(200)
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactMetrics)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactMetrics, Subjects: []contextfabric.SubjectRef{repoSubject("repo-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if v := result.Facts[0].Fields["revert_rate_days"]; v.Integer == nil || *v.Integer != 200 {
		t.Fatalf("revert_rate_days = %#v, want 200", v)
	}
	if !strings.Contains(result.Reason, "revert_rate not applicable on 50 of 250 days") {
		t.Fatalf("Reason = %q", result.Reason)
	}
}
