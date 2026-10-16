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

// CHAOS-9883: a team whose daily_health table lost rows to the display cap
// names the loss on its own fact; the kind result is not truncated by it,
// and the scalar severity and risk come from a separate read, never from the
// cut rows.
func TestChaos9883DailyHealthCutIsDisclosedOnTheSubjectNotTheKind(t *testing.T) {
	t.Parallel()
	var series [][]any
	for i := 0; i < contextfabric.MaxFactValueRows+10; i++ {
		day := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -i).Format("2006-01-02")
		series = append(series, healthTeamDailySeriesRow("CHECKOUT", day, "high", uint8(1), 0.61))
	}
	client := &fakeClient{tables: []fakeTable{
		{match: healthScalarMatch, rows: [][]any{healthRow("CHECKOUT"), healthRow("IDENTITY")}},
		{match: healthDailySeriesMatch, rows: append(series, healthTeamDailySeriesRow("IDENTITY", "2026-09-27", "high", uint8(1), 0.61))},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactHealth)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactHealth, Subjects: []contextfabric.SubjectRef{teamSubject("CHECKOUT"), teamSubject("IDENTITY")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if result.Truncated {
		t.Fatalf("result.Truncated = true, want false: only a display cap cut one subject's daily table")
	}
	for _, fact := range result.Facts {
		omitted, cut := fact.Fields["daily_health_omitted_count"]
		wantCut := fact.Subject.CanonicalID == "team:CHECKOUT"
		if cut != wantCut {
			t.Errorf("%s daily_health_omitted_count present = %v, want %v", fact.Subject.CanonicalID, cut, wantCut)
		}
		if cut && (omitted.Integer == nil || *omitted.Integer != 10) {
			t.Errorf("%s daily_health_omitted_count = %v, want 10", fact.Subject.CanonicalID, omitted)
		}
		if got := fact.Fields["compounding_risk"].Number; got == nil || *got != 0.62 {
			t.Errorf("%s compounding_risk = %v, want the scalar read's 0.62", fact.Subject.CanonicalID, fmt.Sprint(got))
		}
	}
}
