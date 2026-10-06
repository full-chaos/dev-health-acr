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
	insert := func(repoID, stream string, computedHour int, deliveryUnits uint32) {
		t.Helper()
		if err := direct.Exec(ctx, `INSERT INTO investment_metrics_daily (repo_id, day, team_id, investment_area, project_stream, delivery_units, work_items_completed, prs_merged, churn_loc, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			repoID, date(2026, 8, 12), "team-repos", "product", stream, deliveryUnits, uint32(1), uint32(1), uint64(1), ts(2026, 8, 12, computedHour, 0, 0), orgID); err != nil {
			t.Fatalf("seed investment_metrics_daily: %v", err)
		}
	}
	const repoA, repoB = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	const noRepository = "00000000-0000-0000-0000-000000000000"
	// growth: two repositories, each with a stale row.
	insert(repoA, "growth", 5, 99)
	insert(repoA, "growth", 7, 10)
	insert(repoB, "growth", 6, 88)
	insert(repoB, "growth", 8, 20)
	// no-repository: work items with no repository carry the nil UUID and count.
	insert(noRepository, "no-repository", 5, 50)
	insert(noRepository, "no-repository", 9, 7)
	// zeroed: the newest row is zero, so the older non-zero row is never served.
	insert(repoA, "zeroed", 4, 40)
	insert(repoA, "zeroed", 9, 0)

	result, err := findProvider(t, providers, contextfabric.FactInvestment).ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-repos")},
	})
	if err != nil || len(result.Facts) != 1 {
		t.Fatalf("ReadFacts = %v, %v; want one fact", result.Facts, err)
	}
	want := map[string]int64{"growth": 30, "no-repository": 7, "zeroed": 0}
	got := map[string]int64{}
	for _, row := range result.Facts[0].Fields["team_breakdown"].Rows {
		stream := row.Fields["project_stream"].String
		units := row.Fields["delivery_units"].Integer
		if stream == nil || units == nil {
			t.Fatalf("row without project_stream or delivery_units: %v", row.Fields)
		}
		got[*stream] = *units
	}
	for stream, units := range want {
		value, ok := got[stream]
		if !ok || value != units {
			t.Errorf("stream %q: delivery_units = %d (present %v), want %d; all = %v", stream, value, ok, units, got)
		}
	}
}
