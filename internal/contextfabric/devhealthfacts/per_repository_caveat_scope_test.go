package devhealthfacts_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestRevertRateGapSentenceRidesOnlyTheRepositoryWithTheGap(t *testing.T) {
	t.Parallel()
	gap, complete := repoSubject("repo-gap"), repoSubject("repo-full")
	rows := [][]any{}
	add := func(repo, day string, present bool, total, revertDays int64) {
		row := metricsRow(repo)
		row[1] = day
		if !present {
			row[5], row[6] = uint8(0), float64(0)
		}
		row[11], row[12] = total, revertDays
		rows = append(rows, row)
	}
	add("repo-gap", "2026-02-21", false, 2, 1)
	add("repo-gap", "2026-02-20", true, 2, 1)
	add("repo-full", "2026-02-21", true, 3, 3)
	add("repo-full", "2026-02-20", true, 3, 3)
	add("repo-full", "2026-02-19", true, 3, 3)
	client := &fakeClient{tables: []fakeTable{{match: "FROM repo_metrics_daily", rows: rows}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactMetrics)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactMetrics,
		Subjects: []contextfabric.SubjectRef{gap, complete},
	})
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	requireOnly(t, "kind reason", result.Reason, "", revertSentence)
	requireOnly(t, "gapped repository row reason", ownReason(result, gap), "revert_rate not applicable on 1 of 2 days")
	requireOnly(t, "complete repository row reason", ownReason(result, complete), "", revertSentence)
}

func TestOwningTeamsTruncatedSentenceRidesOnlyTheRepositoryWhoseOwnersWereCut(t *testing.T) {
	t.Parallel()
	cutRows := make([][]any, 0, contextfabric.MaxFactValueRows)
	for i := 0; i < contextfabric.MaxFactValueRows; i++ {
		cutRows = append(cutRows, []any{"repo-cut", fmt.Sprintf("team-%03d", i), "Team", int64(contextfabric.MaxFactValueRows + 5)})
	}
	rows := append(cutRows, []any{"repo-fit", "team-a", "Alpha", int64(1)})
	client := &fakeClient{tables: []fakeTable{
		{match: "team_repo_ownership", rows: rows},
		{match: "FROM repos", rows: [][]any{{"repo-cut", "acme/cut", "github"}, {"repo-fit", "acme/fit", "github"}}},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactIdentity)
	cut, fit := repoSubject("repo-cut"), repoSubject("repo-fit")
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactIdentity,
		Subjects: []contextfabric.SubjectRef{cut, fit},
	})
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	requireOnly(t, "kind reason", result.Reason, "", "owning_teams_truncated")
	requireOnly(t, "cut repository row reason", ownReason(result, cut), "owning_teams_truncated")
	requireOnly(t, "fitting repository row reason", ownReason(result, fit), "", "owning_teams_truncated")
}
