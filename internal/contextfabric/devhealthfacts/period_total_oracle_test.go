package devhealthfacts_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The period total is computed from the facts the real repository metrics
// reader returns, under the real declarations. The expected sum is taken from
// the seeded rows here, not from the production function.
func TestAPeriodTotalOfTheRealRepositoryMetricsSeriesEqualsTheSumOfTheSeededRows(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	const periodDays = 30
	skipped := map[int]bool{3: true, 17: true, 29: true}
	var seeded [][]any
	var wantCommits, wantMerged int64
	var wantMissing []string
	for i := 0; i < periodDays; i++ {
		day := base.AddDate(0, 0, i).Format("2006-01-02")
		if skipped[i] {
			wantMissing = append(wantMissing, day)
			continue
		}
		row := metricsRow("repo-1")
		commits, merged := int64(5+i), int64(i%4)
		row[1], row[2], row[3] = day, commits, merged
		row[11] = int64(periodDays - len(skipped))
		seeded = append(seeded, row)
		wantCommits += commits
		wantMerged += merged
	}
	client := &fakeClient{tables: []fakeTable{{match: "FROM repo_metrics_daily", rows: seeded}}}
	providers := devhealthfacts.NewProviders(client)
	provider := findProvider(t, providers, contextfabric.FactMetrics)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactMetrics, Subjects: []contextfabric.SubjectRef{repoSubject("repo-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	var capabilities []contextfabric.FactCapability
	for _, p := range providers {
		capabilities = append(capabilities, p.Capability())
	}
	var period []string
	for i := 0; i < periodDays; i++ {
		period = append(period, base.AddDate(0, 0, i).Format("2006-01-02"))
	}
	totals, absences := contextfabric.PeriodTotalsForSubject(capabilities, result.Facts, repoSubject("repo-1"), period)
	if len(absences) != 0 {
		t.Fatalf("absences = %+v", absences)
	}
	got := map[string]contextfabric.PeriodTotal{}
	for _, total := range totals {
		got[total.Column] = total
	}
	if len(got) != 2 {
		t.Fatalf("totals = %+v, want exactly the two additive columns commits_count and prs_merged", totals)
	}
	for column, want := range map[string]int64{"commits_count": wantCommits, "prs_merged": wantMerged} {
		total, ok := got[column]
		if !ok || total.Sum != want {
			t.Errorf("%s total = %+v, want sum %d", column, total, want)
		}
		if total.Certified || total.DaysWithRow != periodDays-len(skipped) || total.DaysInPeriod != periodDays {
			t.Errorf("%s coverage = %+v, want %d of %d days, not certified", column, total, periodDays-len(skipped), periodDays)
		}
		if fmt.Sprint(total.MissingDays) != fmt.Sprint(wantMissing) {
			t.Errorf("%s missing days = %v, want %v", column, total.MissingDays, wantMissing)
		}
	}
}
