package devhealthfacts_test

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestTeamWithANativeMetricsFactIsNotToldNoRollupApplies(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM team_metrics_daily", rows: [][]any{teamMetricsRow("CHAOS")}}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactMetrics)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactMetrics, Subjects: []contextfabric.SubjectRef{teamSubject("CHAOS")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %d, want the team's native metrics fact", len(result.Facts))
	}
	if got := result.SubjectReasons[contextfabric.FactSubjectKey(teamSubject("CHAOS"))]; strings.Contains(got, "owns no repository") {
		t.Fatalf("the team with a served fact carries %q", got)
	}
	if strings.Contains(result.Reason+result.RequestNote, "own no repository") {
		t.Fatalf("kind/request text %q says a requested team owns no repository, though it holds a fact", result.Reason+result.RequestNote)
	}
}
