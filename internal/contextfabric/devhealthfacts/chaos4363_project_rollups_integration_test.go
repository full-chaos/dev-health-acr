package devhealthfacts_test

import (
	"context"
	"strings"
	"testing"
	"time"

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
// work_unit_investments and repos are CHAOS-5930's addition: the SAME
// InvestmentProvider.ReadFacts project branch this suite already drives now
// always also calls readProjectThemeMix, so a project-subject investment
// read here needs both tables present regardless of whether a given
// subtest seeds any rows into them.
func createCHAOS4363Tables(t *testing.T, ctx context.Context, connection clickhousedriver.Conn) {
	t.Helper()
	for _, statement := range devhealthschema.DDL(
		"projects", "team_project_ownership", "team_repo_ownership", "teams",
		"investment_metrics_daily", "capacity_forecasts", "estimate_coverage_metrics_daily",
		"compounding_risk_daily", "work_unit_investments", "repos", "work_item_team_attributions",
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
		// The team row is "elevated", the repo row is "high" -- the
		// promoted severity is the worst band across both, executed
		// against a real server evaluating the real query text, not a
		// canned-row substitute.
		if got := fact.Fields["severity"].String; got == nil || *got != "high" {
			t.Fatalf("severity = %#v, want high", fact.Fields["severity"])
		}
		if got := fact.Fields["severity_basis"].String; got == nil || *got != "worst_of_team_and_repo_breakdown" {
			t.Fatalf("severity_basis = %#v", fact.Fields["severity_basis"])
		}
	})

	seedRepoOwnership := func(orgID, teamID, repoID, repoFullName string) {
		t.Helper()
		if err := direct.Exec(ctx, `INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			orgID, "github", teamID, repoID, repoFullName, "exact", "native", uint8(1), uint16(1), int32(1), ts(2026, 1, 1, 0, 0, 0), nil, ts(2026, 1, 1, 0, 0, 0)); err != nil {
			t.Fatalf("seed team_repo_ownership: %v", err)
		}
	}
	seedRisk := func(orgID, scope, scopeID, severity string, risk float64, day time.Time) {
		t.Helper()
		if err := direct.Exec(ctx, `INSERT INTO compounding_risk_daily (org_id, day, scope, scope_id, compounding_risk, severity, computed_at) VALUES (?,?,?,?,?,?,?)`,
			orgID, day, scope, scopeID, risk, severity, ts(2026, 8, 12, 6, 0, 0)); err != nil {
			t.Fatalf("seed %s-scope compounding_risk_daily row: %v", scope, err)
		}
	}
	readHealthFact := func(orgID, provider, projectID string, query contextfabric.FactQuery) *contextfabric.CanonicalFact {
		t.Helper()
		if query.Kind == "" {
			query.Kind = contextfabric.FactHealth
		}
		if query.Time.Axis == "" {
			query.Time = contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}
		}
		query.Subjects = []contextfabric.SubjectRef{projectSubject(provider, projectID)}
		provider2 := findProvider(t, providers, contextfabric.FactHealth)
		result, err := provider2.ReadFacts(ctx, storage.Principal{OrgID: orgID}, query)
		if err != nil {
			t.Fatalf("ReadFacts: %v", err)
		}
		if len(result.Facts) == 0 {
			return nil
		}
		return &result.Facts[0]
	}

	// "only unknown": every breakdown row this project can reach reports
	// "unknown" -- the fact must still be served (the breakdown itself is
	// real data), but severity/severity_basis must be absent, with a named
	// reason, never a defaulted "low".
	t.Run("health_project_rollup_only_unknown_severity_discloses_reason", func(t *testing.T) {
		const orgID = "org-health-severity-unknown"
		seedProject("proj-unknown", orgID, "linear", "UNK1")
		seedTeam("team-unknown-a", orgID, "Team Unknown A")
		seedOwnership(orgID, "linear", "team-unknown-a", "UNK1")
		seedRisk(orgID, "team", "team-unknown-a", "unknown", 0.10, date(2026, 8, 12))
		fact := readHealthFact(orgID, "linear", "proj-unknown", contextfabric.FactQuery{})
		if fact == nil {
			t.Fatal("facts = none, want a served roll-up (the breakdown row is real)")
		}
		if _, ok := fact.Fields["severity"]; ok {
			t.Fatalf("fields = %#v, want severity absent", fact.Fields)
		}
		if got := fact.Fields["severity_unavailable_reason"].String; got == nil || *got != "no_known_severity_breakdown_rows" {
			t.Fatalf("severity_unavailable_reason = %#v", fact.Fields["severity_unavailable_reason"])
		}
	})

	// A team owning TWO projects is a coverage filter, never a weight: each
	// project's own max is computed independently over its own breakdown,
	// and an unrelated repo owned by only ONE of the two projects must
	// never affect the OTHER project's severity.
	t.Run("health_project_rollup_shared_team_across_two_projects_is_a_coverage_filter", func(t *testing.T) {
		const orgID = "org-health-severity-shared-team"
		repoID := "44444444-4444-4444-4444-444444444444"
		seedProject("proj-shared-a", orgID, "linear", "SHARED-A")
		seedProject("proj-shared-b", orgID, "linear", "SHARED-B")
		seedTeam("team-shared-x", orgID, "Team Shared X")
		// seedOwnership's own project_id literal ("irrelevant") is shared by
		// every caller -- team_project_ownership's ReplacingMergeTree sorts
		// on (org_id, provider, project_id, team_id, source, valid_from), so
		// two ownership rows for the SAME team with the SAME literal
		// project_id collapse into one under FINAL. A team owning two
		// DISTINCT projects needs each row's project_id to actually name
		// its own project.
		if err := direct.Exec(ctx, `INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?), (?,?,?,?,?,?,?,?,?)`,
			orgID, "linear", "team-shared-x", "proj-shared-a", "SHARED-A", "native", ts(2026, 1, 1, 0, 0, 0), nil, ts(2026, 1, 1, 0, 0, 0),
			orgID, "linear", "team-shared-x", "proj-shared-b", "SHARED-B", "native", ts(2026, 1, 1, 0, 0, 0), nil, ts(2026, 1, 1, 0, 0, 0)); err != nil {
			t.Fatalf("seed team_project_ownership rows: %v", err)
		}
		seedRepoOwnership(orgID, "team-shared-x", repoID, "acme/shared-only-a")
		seedRisk(orgID, "team", "team-shared-x", "high", 0.70, date(2026, 8, 12))
		seedRisk(orgID, "repo", repoID, "low", 0.05, date(2026, 8, 12))
		// SHARED-A only reaches team-shared-x THROUGH team_project_ownership
		// too, but repoID is owned by team-shared-x, which owns BOTH
		// projects -- so the repo row is reachable from either project's own
		// join. It must never downgrade EITHER project's max below "high".
		factA := readHealthFact(orgID, "linear", "proj-shared-a", contextfabric.FactQuery{})
		factB := readHealthFact(orgID, "linear", "proj-shared-b", contextfabric.FactQuery{})
		if factA == nil || factB == nil {
			t.Fatalf("facts = %+v / %+v, want both projects served", factA, factB)
		}
		if got := factA.Fields["severity"].String; got == nil || *got != "high" {
			t.Fatalf("proj-shared-a severity = %#v, want high (the low repo row must not downgrade the team's high)", factA.Fields["severity"])
		}
		if got := factB.Fields["severity"].String; got == nil || *got != "high" {
			t.Fatalf("proj-shared-b severity = %#v, want high (the SAME team row, reached from a different project, reports the same value -- a coverage filter, not a weight)", factB.Fields["severity"])
		}
	})

	// A team owning ONE project through more than one ownership `source`
	// row (dedupeTeamRow's own documented case) must still report a single
	// severity value, never doubled or altered by the duplication.
	t.Run("health_project_rollup_duplicate_ownership_source_is_idempotent", func(t *testing.T) {
		const orgID = "org-health-severity-dup-source"
		seedProject("proj-dup", orgID, "linear", "DUP1")
		seedTeam("team-dup-a", orgID, "Team Dup A")
		seedOwnership(orgID, "linear", "team-dup-a", "DUP1")
		if err := direct.Exec(ctx, `INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			orgID, "linear", "team-dup-a", "irrelevant", "DUP1", "provider_access", ts(2026, 1, 1, 0, 0, 0), nil, ts(2026, 1, 1, 0, 0, 0)); err != nil {
			t.Fatalf("seed second team_project_ownership source row: %v", err)
		}
		seedRisk(orgID, "team", "team-dup-a", "high", 0.75, date(2026, 8, 12))
		fact := readHealthFact(orgID, "linear", "proj-dup", contextfabric.FactQuery{})
		if fact == nil {
			t.Fatal("facts = none, want a served roll-up")
		}
		if got := fact.Fields["team_count"].Integer; got == nil || *got != 1 {
			t.Fatalf("team_count = %#v, want 1 (deduped across the two ownership source rows)", fact.Fields["team_count"])
		}
		if got := fact.Fields["severity"].String; got == nil || *got != "high" {
			t.Fatalf("severity = %#v, want high", fact.Fields["severity"])
		}
	})

	// as-of: a later day's severity must never leak into a bounded as-of
	// read -- the promoted value comes from the SAME timeBound-filtered
	// rows risk_breakdown already uses, so this is the same guarantee
	// applied to the new field.
	t.Run("health_project_rollup_as_of_window_excludes_a_later_severity", func(t *testing.T) {
		const orgID = "org-health-severity-as-of"
		seedProject("proj-asof", orgID, "linear", "ASOF1")
		seedTeam("team-asof-a", orgID, "Team AsOf A")
		seedOwnership(orgID, "linear", "team-asof-a", "ASOF1")
		seedRisk(orgID, "team", "team-asof-a", "low", 0.10, date(2026, 8, 1))
		seedRisk(orgID, "team", "team-asof-a", "high", 0.90, date(2026, 8, 20))
		asOf := ts(2026, 8, 10, 0, 0, 0)
		fact := readHealthFact(orgID, "linear", "proj-asof", contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalValidTime, AsOf: &asOf},
		})
		if fact == nil {
			t.Fatal("facts = none, want a served roll-up as of 2026-08-10")
		}
		if got := fact.Fields["severity"].String; got == nil || *got != "low" {
			t.Fatalf("severity = %#v, want low (the 2026-08-20 high row is AFTER the as-of instant and must not leak in)", fact.Fields["severity"])
		}
	})

	// Two independent caps sit between a contributing row and a served
	// answer: the DISPLAY cap (MaxFactValueRows=64, risk_breakdown's own
	// rendered row count) and the row-level breakdown scan's own SHARED
	// withRowLimit(maxFactRowsPerQuery=200) budget. severity is read from
	// queryProjectHealthSeverityMax's SEPARATE server-side aggregate,
	// which GROUPs BY project and is never subject to either cap -- this
	// seeds past BOTH (210 > 200 > 64) with the worst band sorted last (so
	// it is the first row either cap would drop) and proves the served
	// severity is still correct.
	t.Run("health_project_rollup_severity_survives_display_cap_truncation", func(t *testing.T) {
		const orgID = "org-health-severity-display-cap"
		seedProject("proj-cap", orgID, "linear", "CAP1")
		seedTeam("team-cap-a", orgID, "Team Cap A")
		seedOwnership(orgID, "linear", "team-cap-a", "CAP1")
		const repoCount = 210 // > maxFactRowsPerQuery (200) > MaxFactValueRows (64)
		for i := 0; i < repoCount; i++ {
			// repo_id is Nullable(UUID) -- a well-formed UUID per index, not
			// an arbitrary string, or the column silently reads NULL and
			// the repo branch's own `repo_id IS NOT NULL` guard drops every
			// row.
			repoID := "55555555-5555-5555-5555-" + paddedIndex(i) + "00000000"
			seedRepoOwnership(orgID, "team-cap-a", repoID, "acme/cap-"+paddedIndex(i))
			severity := "low"
			// The LAST repo by scope_id sort order (risk_breakdown's own
			// ORDER BY project_key, scope, scope_id) carries the worst band,
			// so it sits past BOTH caps if severity were read off the
			// row-level scan instead of the separate aggregate.
			if i == repoCount-1 {
				severity = "high"
			}
			seedRisk(orgID, "repo", repoID, severity, 0.10, date(2026, 8, 12))
		}
		healthProvider := findProvider(t, providers, contextfabric.FactHealth)
		result, err := healthProvider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			Kind: contextfabric.FactHealth, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-cap")},
		})
		if err != nil {
			t.Fatalf("ReadFacts: %v", err)
		}
		if len(result.Facts) == 0 {
			t.Fatal("facts = none, want a served roll-up")
		}
		if !result.Truncated {
			t.Fatalf("Truncated = false, want true (the row-level scan's shared %d-row budget must be exceeded by %d contributing repo rows)", 200, repoCount)
		}
		fact := result.Facts[0]
		rows := fact.Fields["risk_breakdown"].Rows
		if len(rows) >= repoCount {
			t.Fatalf("risk_breakdown rows = %d, want fewer than %d (both caps must have fired)", len(rows), repoCount)
		}
		if got := fact.Fields["severity"].String; got == nil || *got != "high" {
			t.Fatalf("severity = %#v, want high (the true worst band, even though its own row sits past both the row-level scan's shared budget and the display cap)", fact.Fields["severity"])
		}
	})

	// A project can be ABSENT from the row-level breakdown scan entirely
	// (not merely missing its worst row) while a DIFFERENT project's own
	// rows exhaust the shared budget first -- proving population must come
	// from the uncapped severity aggregate, never from which projects the
	// row-level scan happened to reach. Both projects are requested in ONE
	// call; the early-sorting project owns enough repos to exhaust the
	// shared 200-row budget by itself, and the late-sorting project's own
	// single row never reaches the scan at all.
	t.Run("health_project_rollup_late_project_served_from_the_aggregate_with_evidence", func(t *testing.T) {
		const orgID = "org-health-severity-late-project"
		seedProject("proj-aaa-cap", orgID, "linear", "AAACAP1")
		seedTeam("team-aaa-cap", orgID, "Team AAA Cap")
		seedOwnership(orgID, "linear", "team-aaa-cap", "AAACAP1")
		const repoCount = 210
		for i := 0; i < repoCount; i++ {
			repoID := "66666666-6666-6666-6666-" + paddedIndex(i) + "00000000"
			seedRepoOwnership(orgID, "team-aaa-cap", repoID, "acme/late-"+paddedIndex(i))
			seedRisk(orgID, "repo", repoID, "low", 0.10, date(2026, 8, 12))
		}
		seedProject("proj-zzz-late", orgID, "linear", "ZZZLATE1")
		seedTeam("team-zzz-late", orgID, "Team ZZZ Late")
		seedOwnership(orgID, "linear", "team-zzz-late", "ZZZLATE1")
		seedRisk(orgID, "team", "team-zzz-late", "high", 0.90, date(2026, 8, 12))

		healthProvider := findProvider(t, providers, contextfabric.FactHealth)
		result, err := healthProvider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			Kind: contextfabric.FactHealth, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-aaa-cap"), projectSubject("linear", "proj-zzz-late")},
		})
		if err != nil {
			t.Fatalf("ReadFacts: %v", err)
		}
		if !result.Truncated {
			t.Fatal("Truncated = false, want true -- proj-aaa-cap's own 210 rows must exhaust the shared budget")
		}
		if len(result.Facts) != 2 {
			t.Fatalf("facts = %d, want 2 -- proj-zzz-late must be served even though its row never reached the shared row-level scan", len(result.Facts))
		}
		var late *contextfabric.CanonicalFact
		for i := range result.Facts {
			if result.Facts[i].Subject.CanonicalID == projectSubject("linear", "proj-zzz-late").CanonicalID {
				late = &result.Facts[i]
			}
		}
		if late == nil {
			t.Fatalf("facts = %#v, want proj-zzz-late present", result.Facts)
		}
		if got := late.Fields["severity"].String; got == nil || *got != "high" {
			t.Fatalf("proj-zzz-late severity = %#v, want high (from the aggregate, independent of the row-level scan)", late.Fields["severity"])
		}
		if got := late.Fields["severity_basis"].String; got == nil || *got != "worst_of_team_and_repo_breakdown" {
			t.Fatalf("severity_basis = %#v", late.Fields["severity_basis"])
		}
		if _, ok := late.Fields["risk_breakdown"]; ok {
			t.Fatalf("fields = %#v, want risk_breakdown absent -- the shared scan never reached this project's own row", late.Fields)
		}
		if got := late.Fields["risk_breakdown_rows_shown"].Integer; got == nil || *got != 0 {
			t.Fatalf("risk_breakdown_rows_shown = %#v, want 0", late.Fields["risk_breakdown_rows_shown"])
		}
		if got := late.Fields["risk_breakdown_rows_total"].Integer; got == nil || *got != 1 {
			t.Fatalf("risk_breakdown_rows_total = %#v, want 1 (the aggregate's own uncapped count)", late.Fields["risk_breakdown_rows_total"])
		}
		foundTeamZZZLateRef := false
		for _, ref := range late.EvidenceRefIDs {
			if strings.Contains(ref, "team-zzz-late") {
				foundTeamZZZLateRef = true
			}
		}
		if !foundTeamZZZLateRef {
			t.Fatalf("evidence_ref_ids = %#v, want a ref citing team-zzz-late -- the scope that produced the promoted severity, even though the row-level scan never reached it", late.EvidenceRefIDs)
		}
	})
}
