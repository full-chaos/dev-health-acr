package devhealthfacts_test

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// investment_metrics_daily keeps one row per (org, day, repo, team, area,
// stream). Two repositories of one team and stream on one day must both count:
// the newest row of each repository is taken first, then the repositories are
// summed.
func TestInvestmentSumsTheNewestRowOfEachRepositoryAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	createCHAOS4363Tables(t, ctx, direct)
	providers := devhealthfacts.NewProviders(query)

	const orgID = "org-investment-two-repos"
	if err := direct.Exec(ctx, `ALTER TABLE investment_metrics_daily ADD COLUMN IF NOT EXISTS repo_id Nullable(UUID)`); err != nil {
		t.Fatalf("add repo_id: %v", err)
	}
	if err := direct.Exec(ctx, `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		"proj-repos", orgID, "linear", "REPO1", "Project", uint8(1), "active", "", ts(2026, 1, 1, 0, 0, 0)); err != nil {
		t.Fatalf("seed projects row: %v", err)
	}
	if err := direct.Exec(ctx, `INSERT INTO teams (id, name, description, updated_at, org_id, provider, project_keys, is_active) VALUES (?, ?, NULL, ?, ?, ?, [], ?)`,
		"team-repos", "Team Repos", ts(2026, 1, 1, 0, 0, 0), orgID, "linear", uint8(1)); err != nil {
		t.Fatalf("seed teams row: %v", err)
	}
	if err := direct.Exec(ctx, `INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		orgID, "linear", "team-repos", "irrelevant", "REPO1", "native", ts(2026, 1, 1, 0, 0, 0), nil, ts(2026, 1, 1, 0, 0, 0)); err != nil {
		t.Fatalf("seed team_project_ownership: %v", err)
	}
	insert := func(repoID string, computedHour int, deliveryUnits uint32) {
		t.Helper()
		if err := direct.Exec(ctx, `INSERT INTO investment_metrics_daily (repo_id, day, team_id, investment_area, project_stream, delivery_units, work_items_completed, prs_merged, churn_loc, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			repoID, date(2026, 8, 12), "team-repos", "product", "growth", deliveryUnits, uint32(1), uint32(1), uint64(1), ts(2026, 8, 12, computedHour, 0, 0), orgID); err != nil {
			t.Fatalf("seed investment_metrics_daily: %v", err)
		}
	}
	const repoA, repoB = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	insert(repoA, 5, 99)
	insert(repoA, 7, 10)
	insert(repoB, 6, 88)
	insert(repoB, 8, 20)

	result, err := findProvider(t, providers, contextfabric.FactInvestment).ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-repos")},
	})
	if err != nil || len(result.Facts) != 1 {
		t.Fatalf("ReadFacts = %v, %v; want one fact", result.Facts, err)
	}
	rows := result.Facts[0].Fields["team_breakdown"].Rows
	if len(rows) != 1 {
		t.Fatalf("team_breakdown rows = %d, want 1", len(rows))
	}
	got := rows[0].Fields["delivery_units"].Integer
	if got == nil {
		t.Fatalf("delivery_units missing")
	}
	if *got != 30 {
		t.Fatalf("delivery_units = %d, want 30 (newest row of repo A 10 + newest row of repo B 20)", *got)
	}
}
