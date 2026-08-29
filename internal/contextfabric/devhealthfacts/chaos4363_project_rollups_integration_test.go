package devhealthfacts_test

import (
	"context"
	"testing"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// createCHAOS4363Tables creates every source table CHAOS-4363's new project
// rollups read, rendered from the shared production declaration
// (devhealthschema) -- including team_repo_ownership, which codex round-1
// P2 found undeclared there (this table's first reader in this package).
func createCHAOS4363Tables(t *testing.T, ctx context.Context, connection clickhousedriver.Conn) {
	t.Helper()
	for _, statement := range devhealthschema.DDL(
		"projects", "team_project_ownership", "team_repo_ownership", "teams",
		"investment_metrics_daily", "capacity_forecasts", "estimate_coverage_metrics_daily",
		"compounding_risk_daily",
	) {
		if err := connection.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
}

// TestCHAOS4363ProjectRollupsAgainstRealClickHouse proves the fakeClient-based
// unit tests' claims for the four widened providers (investment, workload,
// readiness, health) actually hold against a real server evaluating the
// real query text: the team_project_ownership join, per-provider
// row_number()/PARTITION BY tie-breaking, and -- for health specifically --
// the team_repo_ownership second hop and the UNION ALL + outer LIMIT/ORDER
// BY fix (codex round-1 P2: LIMIT appended directly after two UNION ALL'd
// SELECTs binds only to the second branch).
func TestCHAOS4363ProjectRollupsAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	createCHAOS4363Tables(t, ctx, direct)
	providers := devhealthfacts.NewProviders(query)

	seedProject := func(id, orgID, provider, projectKey string) {
		t.Helper()
		if err := direct.Exec(ctx, `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			id, orgID, provider, projectKey, "Project", uint8(1), "active", "", ts(2026, 1, 1, 0, 0, 0)); err != nil {
			t.Fatalf("seed projects row: %v", err)
		}
	}
	seedTeam := func(id, orgID, name string) {
		t.Helper()
		// Columns match devhealthschema's own declared subset of `teams`
		// (id, name, description, updated_at, org_id, provider,
		// native_team_key, project_keys, is_active) -- a deliberate PARTIAL
		// declaration of the production table (only columns this package's
		// readers actually touch), not the full live column set.
		if err := direct.Exec(ctx, `INSERT INTO teams (id, name, description, updated_at, org_id, provider, project_keys, is_active) VALUES (?, ?, NULL, ?, ?, ?, [], ?)`,
			id, name, ts(2026, 1, 1, 0, 0, 0), orgID, "linear", uint8(1)); err != nil {
			t.Fatalf("seed teams row: %v", err)
		}
	}
	seedOwnership := func(orgID, provider, teamID, projectKey string) {
		t.Helper()
		if err := direct.Exec(ctx, `INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			orgID, provider, teamID, "irrelevant", projectKey, "native", ts(2026, 1, 1, 0, 0, 0), nil, ts(2026, 1, 1, 0, 0, 0)); err != nil {
			t.Fatalf("seed team_project_ownership: %v", err)
		}
	}

	t.Run("investment_project_rollup_breaks_down_by_team_never_sums_across_areas", func(t *testing.T) {
		const orgID = "org-investment-rollup"
		seedProject("proj-inv", orgID, "linear", "INV1")
		seedTeam("team-inv-a", orgID, "Team Inv A")
		seedOwnership(orgID, "linear", "team-inv-a", "INV1")
		if err := direct.Exec(ctx, `INSERT INTO investment_metrics_daily (day, team_id, investment_area, project_stream, delivery_units, work_items_completed, prs_merged, churn_loc, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?,?)`,
			date(2026, 8, 12), "team-inv-a", "product", "growth", uint32(30), uint32(12), uint32(4), uint64(850), ts(2026, 8, 12, 6, 0, 0), orgID); err != nil {
			t.Fatalf("seed investment row: %v", err)
		}
		provider := findProvider(t, providers, contextfabric.FactInvestment)
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-inv")},
		})
		if err != nil {
			t.Fatalf("ReadFacts() error = %v", err)
		}
		if len(result.Facts) != 1 {
			t.Fatalf("facts = %#v, want exactly 1", result.Facts)
		}
		fact := result.Facts[0]
		if got := fact.Fields["team_count"].Integer; got == nil || *got != 1 {
			t.Fatalf("team_count = %#v, want 1", fact.Fields["team_count"])
		}
		rows := fact.Fields["team_breakdown"].Rows
		if len(rows) != 1 {
			t.Fatalf("team_breakdown = %#v, want 1 row", rows)
		}
		if got := rows[0].Fields["delivery_units"].Integer; got == nil || *got != 30 {
			t.Fatalf("delivery_units = %#v, want 30", rows[0].Fields["delivery_units"])
		}
	})

	t.Run("workload_project_rollup_breaks_down_by_team", func(t *testing.T) {
		const orgID = "org-workload-rollup"
		seedProject("proj-wl", orgID, "linear", "WL1")
		seedTeam("team-wl-a", orgID, "Team Workload A")
		seedOwnership(orgID, "linear", "team-wl-a", "WL1")
		if err := direct.Exec(ctx, `INSERT INTO capacity_forecasts (forecast_id, computed_at, team_id, work_scope_id, backlog_size, throughput_mean, throughput_stddev, insufficient_history, high_variance, org_id) VALUES (?,?,?,?,?,?,?,?,?,?)`,
			// CHAOS-4521b: work_scope_id IS the project's own identity now;
			// the ownership row above is left in place to prove this read no
			// longer depends on it.
			"forecast-1", ts(2026, 8, 12, 6, 0, 0), "team-wl-a", "proj-wl", uint32(12), 3.2, 0.8, uint8(0), uint8(1), orgID); err != nil {
			t.Fatalf("seed capacity_forecasts row: %v", err)
		}
		provider := findProvider(t, providers, contextfabric.FactWorkload)
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			Kind: contextfabric.FactWorkload, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-wl")},
		})
		if err != nil {
			t.Fatalf("ReadFacts() error = %v", err)
		}
		if len(result.Facts) != 1 {
			t.Fatalf("facts = %#v, want exactly 1", result.Facts)
		}
		rows := result.Facts[0].Fields["team_breakdown"].Rows
		if len(rows) != 1 {
			t.Fatalf("team_breakdown = %#v, want 1 row", rows)
		}
		if got := rows[0].Fields["backlog_size"].Integer; got == nil || *got != 12 {
			t.Fatalf("backlog_size = %#v, want 12", rows[0].Fields["backlog_size"])
		}
	})

	t.Run("readiness_project_rollup_breaks_down_by_team", func(t *testing.T) {
		const orgID = "org-readiness-rollup"
		seedProject("proj-rd", orgID, "linear", "RD1")
		seedTeam("team-rd-a", orgID, "Team Readiness A")
		seedOwnership(orgID, "linear", "team-rd-a", "RD1")
		if err := direct.Exec(ctx, `INSERT INTO estimate_coverage_metrics_daily (day, provider, work_scope_id, team_id, estimated_count, unestimated_count, backlog_size, ratio, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?,?)`,
			// CHAOS-4521b: as above -- the project's own work scope.
			date(2026, 8, 12), "linear", "proj-rd", "team-rd-a", uint32(18), uint32(2), uint32(20), 0.9, ts(2026, 8, 12, 6, 0, 0), orgID); err != nil {
			t.Fatalf("seed estimate_coverage row: %v", err)
		}
		provider := findProvider(t, providers, contextfabric.FactReadiness)
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			Kind: contextfabric.FactReadiness, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-rd")},
		})
		if err != nil {
			t.Fatalf("ReadFacts() error = %v", err)
		}
		if len(result.Facts) != 1 {
			t.Fatalf("facts = %#v, want exactly 1", result.Facts)
		}
		rows := result.Facts[0].Fields["team_breakdown"].Rows
		if len(rows) != 1 {
			t.Fatalf("team_breakdown = %#v, want 1 row", rows)
		}
		if got := rows[0].Fields["estimated_count"].Integer; got == nil || *got != 18 {
			t.Fatalf("estimated_count = %#v, want 18", rows[0].Fields["estimated_count"])
		}
	})

	// health is the two-layer (team + repo, UNION ALL) rollup -- this
	// subtest is the direct proof for codex round-1 P2 (UNION+LIMIT
	// wrapping): both a team-scope row AND a repo-scope row (reached one
	// hop further through team_repo_ownership) must survive in the SAME
	// read, which only happens if the LIMIT/ORDER BY apply to the combined
	// result, not just the second (repo) branch.
	t.Run("health_project_rollup_breaks_down_by_team_and_repo_via_union", func(t *testing.T) {
		const orgID = "org-health-rollup"
		repoID := "33333333-3333-3333-3333-333333333333"
		seedProject("proj-health", orgID, "linear", "HEALTH1")
		seedTeam("team-health-a", orgID, "Team Health A")
		seedOwnership(orgID, "linear", "team-health-a", "HEALTH1")
		if err := direct.Exec(ctx, `INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			orgID, "github", "team-health-a", repoID, "acme/service", "exact", "native", uint8(1), uint16(1), int32(1), ts(2026, 1, 1, 0, 0, 0), nil, ts(2026, 1, 1, 0, 0, 0)); err != nil {
			t.Fatalf("seed team_repo_ownership: %v", err)
		}
		if err := direct.Exec(ctx, `INSERT INTO compounding_risk_daily (org_id, day, scope, scope_id, compounding_risk, severity, computed_at) VALUES (?,?,?,?,?,?,?)`,
			orgID, date(2026, 8, 12), "team", "team-health-a", 0.55, "elevated", ts(2026, 8, 12, 6, 0, 0)); err != nil {
			t.Fatalf("seed team-scope compounding_risk_daily row: %v", err)
		}
		if err := direct.Exec(ctx, `INSERT INTO compounding_risk_daily (org_id, day, scope, scope_id, compounding_risk, severity, computed_at) VALUES (?,?,?,?,?,?,?)`,
			orgID, date(2026, 8, 12), "repo", repoID, 0.81, "high", ts(2026, 8, 12, 6, 0, 0)); err != nil {
			t.Fatalf("seed repo-scope compounding_risk_daily row: %v", err)
		}
		provider := findProvider(t, providers, contextfabric.FactHealth)
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			Kind: contextfabric.FactHealth, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-health")},
		})
		if err != nil {
			t.Fatalf("ReadFacts() error = %v", err)
		}
		if len(result.Facts) != 1 {
			t.Fatalf("facts = %#v, want exactly 1", result.Facts)
		}
		fact := result.Facts[0]
		if got := fact.Fields["team_count"].Integer; got == nil || *got != 1 {
			t.Fatalf("team_count = %#v, want 1", fact.Fields["team_count"])
		}
		if got := fact.Fields["repo_count"].Integer; got == nil || *got != 1 {
			t.Fatalf("repo_count = %#v, want 1", fact.Fields["repo_count"])
		}
		rows := fact.Fields["risk_breakdown"].Rows
		if len(rows) != 2 {
			t.Fatalf("risk_breakdown = %#v, want 2 rows (one team, one repo) -- if this is 1, the UNION+LIMIT bug truncated one branch", rows)
		}
		var sawTeam, sawRepo bool
		for _, row := range rows {
			scope := row.Fields["scope"].String
			if scope == nil {
				continue
			}
			switch *scope {
			case "team":
				sawTeam = true
			case "repo":
				sawRepo = true
			}
		}
		if !sawTeam || !sawRepo {
			t.Fatalf("risk_breakdown rows = %#v, want both a team row and a repo row", rows)
		}
	})
}
