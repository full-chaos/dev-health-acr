package devhealthfacts_test

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// Append-only daily tables hold several rows per key and day when a day is
// recomputed. The newest computed_at row must be the only one served. The
// older row is inserted AFTER the newer one so insertion order cannot pick it.
func TestDailyTablesServeOnlyTheNewestRowPerKeyAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	createCHAOS4363Tables(t, ctx, direct)
	providers := devhealthfacts.NewProviders(query)

	const orgID = "org-daily-newest-row"
	if err := direct.Exec(ctx, `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		"proj-newest", orgID, "linear", "NEW1", "Project", uint8(1), "active", "", ts(2026, 1, 1, 0, 0, 0)); err != nil {
		t.Fatalf("seed projects row: %v", err)
	}
	if err := direct.Exec(ctx, `INSERT INTO teams (id, name, description, updated_at, org_id, provider, project_keys, is_active) VALUES (?, ?, NULL, ?, ?, ?, [], ?)`,
		"team-newest", "Team Newest", ts(2026, 1, 1, 0, 0, 0), orgID, "linear", uint8(1)); err != nil {
		t.Fatalf("seed teams row: %v", err)
	}
	if err := direct.Exec(ctx, `INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		orgID, "linear", "team-newest", "irrelevant", "NEW1", "native", ts(2026, 1, 1, 0, 0, 0), nil, ts(2026, 1, 1, 0, 0, 0)); err != nil {
		t.Fatalf("seed team_project_ownership: %v", err)
	}

	insertInvestment := func(computedHour int, deliveryUnits uint32) {
		t.Helper()
		if err := direct.Exec(ctx, `INSERT INTO investment_metrics_daily (day, team_id, investment_area, project_stream, delivery_units, work_items_completed, prs_merged, churn_loc, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?,?)`,
			date(2026, 8, 12), "team-newest", "product", "growth", deliveryUnits, uint32(1), uint32(1), uint64(1), ts(2026, 8, 12, computedHour, 0, 0), orgID); err != nil {
			t.Fatalf("seed investment_metrics_daily: %v", err)
		}
	}
	insertInvestment(7, 30)
	insertInvestment(5, 99)

	insertCoverage := func(computedHour int, estimated uint32) {
		t.Helper()
		if err := direct.Exec(ctx, `INSERT INTO estimate_coverage_metrics_daily (day, provider, work_scope_id, team_id, estimated_count, unestimated_count, backlog_size, ratio, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?,?)`,
			date(2026, 8, 12), "linear", "proj-newest", "team-newest", estimated, uint32(2), estimated+2, 0.9, ts(2026, 8, 12, computedHour, 0, 0), orgID); err != nil {
			t.Fatalf("seed estimate_coverage_metrics_daily: %v", err)
		}
	}
	insertCoverage(7, 18)
	insertCoverage(5, 50)

	read := func(kind contextfabric.FactKind) contextfabric.CanonicalFact {
		t.Helper()
		result, err := findProvider(t, providers, kind).ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			Kind: kind, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-newest")},
		})
		if err != nil || len(result.Facts) != 1 {
			t.Fatalf("ReadFacts(%s) = %v, %v; want one fact", kind, result.Facts, err)
		}
		return result.Facts[0]
	}

	t.Run("investment", func(t *testing.T) {
		rows := read(contextfabric.FactInvestment).Fields["team_breakdown"].Rows
		if len(rows) != 1 {
			t.Fatalf("team_breakdown rows = %d, want 1 (one row per team, area, stream)", len(rows))
		}
		if got := rows[0].Fields["delivery_units"].Integer; got == nil || *got != 30 {
			t.Fatalf("delivery_units = %v, want 30 (the newest computed_at row)", got)
		}
	})

	t.Run("readiness", func(t *testing.T) {
		rows := read(contextfabric.FactReadiness).Fields["team_breakdown"].Rows
		if len(rows) != 1 {
			t.Fatalf("team_breakdown rows = %d, want 1", len(rows))
		}
		if got := rows[0].Fields["estimated_count"].Integer; got == nil || *got != 18 {
			t.Fatalf("estimated_count = %v, want 18 (the newest computed_at row, not 68)", got)
		}
	})
}
