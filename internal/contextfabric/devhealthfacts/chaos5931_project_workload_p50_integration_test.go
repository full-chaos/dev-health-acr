package devhealthfacts_test

// CHAOS-5931: queryProjectWorkloadP50Max's own server-side argMax over a
// packed (p50_days, team_key, work_scope_id, insufficient_history,
// high_variance) string, the row_number()-per-(team,work_scope) dedup, and
// the project-identity join over capacity_forecasts all need a real server
// evaluating the real query text -- a fakeClient's canned rows cannot
// certify any of this. These tests EXECUTE WorkloadProvider.ReadFacts
// against a real ClickHouse over seeded projects/capacity_forecasts rows,
// mirroring chaos4363_project_rollups_integration_test.go's own real-server
// discipline for the sibling health/investment/readiness roll-ups.

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

func createCHAOS5931Tables(t *testing.T, ctx context.Context, connection clickhousedriver.Conn) {
	t.Helper()
	for _, statement := range devhealthschema.DDL("projects", "teams", "capacity_forecasts") {
		if err := connection.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
}

// TestCHAOS5931ProjectWorkloadP50AgainstRealClickHouse proves the whole
// promotion (max-wins, NULL exclusion, all-NULL honesty, as-of window,
// rerun idempotency, and survival past both the display cap and the
// row-level scan's shared budget) against one real server.
func TestCHAOS5931ProjectWorkloadP50AgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	createCHAOS5931Tables(t, ctx, direct)
	providers := devhealthfacts.NewProviders(query)

	seedProject := func(id, orgID, provider, projectKey string) {
		t.Helper()
		if err := direct.Exec(ctx, `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			id, orgID, provider, projectKey, "Project", uint8(1), "active", "", ts(2026, 1, 1, 0, 0, 0)); err != nil {
			t.Fatalf("seed projects row: %v", err)
		}
	}
	// seedForecast inserts one capacity_forecasts row directly keyed by the
	// project's own work_scope_id -- this producer has no
	// team_project_ownership hop at all (see this file's RISK-NOTES). p50
	// nil means NULL (no recorded p50_days for this row).
	seedForecast := func(orgID, forecastID, teamID, workScopeID string, p50 *uint16, insufficientHistory, highVariance uint8, computedAt time.Time) {
		t.Helper()
		if err := direct.Exec(ctx, `INSERT INTO capacity_forecasts (forecast_id, computed_at, team_id, work_scope_id, backlog_size, p50_days, throughput_mean, throughput_stddev, insufficient_history, high_variance, org_id) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			forecastID, computedAt, teamID, workScopeID, uint32(12), p50, 3.2, 0.8, insufficientHistory, highVariance, orgID); err != nil {
			t.Fatalf("seed capacity_forecasts row %s: %v", forecastID, err)
		}
	}
	// seedForecastUnattributed is seedForecast's team_id-NULL counterpart:
	// the source's own team_id was NULL, so the row carries real coverage
	// but no contributing team to cite as evidence for it.
	seedForecastUnattributed := func(orgID, forecastID, workScopeID string, p50 *uint16, insufficientHistory, highVariance uint8, computedAt time.Time) {
		t.Helper()
		if err := direct.Exec(ctx, `INSERT INTO capacity_forecasts (forecast_id, computed_at, work_scope_id, backlog_size, p50_days, throughput_mean, throughput_stddev, insufficient_history, high_variance, org_id) VALUES (?,?,?,?,?,?,?,?,?,?)`,
			forecastID, computedAt, workScopeID, uint32(12), p50, 3.2, 0.8, insufficientHistory, highVariance, orgID); err != nil {
			t.Fatalf("seed unattributed capacity_forecasts row %s: %v", forecastID, err)
		}
	}
	p50 := func(days uint16) *uint16 { return &days }
	readWorkloadFact := func(orgID, provider, projectID string, query contextfabric.FactQuery) *contextfabric.CanonicalFact {
		t.Helper()
		if query.Kind == "" {
			query.Kind = contextfabric.FactWorkload
		}
		if query.Time.Axis == "" {
			query.Time = contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}
		}
		query.Subjects = []contextfabric.SubjectRef{projectSubject(provider, projectID)}
		workloadProvider := findProvider(t, providers, contextfabric.FactWorkload)
		result, err := workloadProvider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, query)
		if err != nil {
			t.Fatalf("ReadFacts: %v", err)
		}
		if len(result.Facts) == 0 {
			return nil
		}
		return &result.Facts[0]
	}

	t.Run("workload_project_rollup_multi_team_max_wins_flags_from_winner_only", func(t *testing.T) {
		const orgID = "org-workload-p50-multi"
		seedProject("proj-multi", orgID, "linear", "MULTI1")
		seedForecast(orgID, "f-multi-1", "team-a", "proj-multi", p50(5), 1, 0, ts(2026, 8, 12, 6, 0, 0))
		seedForecast(orgID, "f-multi-2", "team-b", "proj-multi", p50(40), 0, 1, ts(2026, 8, 12, 6, 0, 0))
		fact := readWorkloadFact(orgID, "linear", "proj-multi", contextfabric.FactQuery{})
		if fact == nil {
			t.Fatal("facts = none, want a served roll-up")
		}
		if got := fact.Fields["forecast_p50_days"].Integer; got == nil || *got != 40 {
			t.Fatalf("forecast_p50_days = %#v, want 40 (the worst/longest of 5 and 40)", fact.Fields["forecast_p50_days"])
		}
		if got := fact.Fields["p50_basis"].String; got == nil || *got != "worst_of_team_breakdown" {
			t.Fatalf("p50_basis = %#v, want worst_of_team_breakdown", fact.Fields["p50_basis"])
		}
		// team-a's own flags (insufficient_history=true, high_variance=false)
		// must NOT leak onto the promoted value -- only team-b, the winner.
		if got := fact.Fields["insufficient_history"].Boolean; got == nil || *got {
			t.Fatalf("insufficient_history = %#v, want false (team-b's own flag, not team-a's)", fact.Fields["insufficient_history"])
		}
		if got := fact.Fields["high_variance"].Boolean; got == nil || !*got {
			t.Fatalf("high_variance = %#v, want true (team-b's own flag)", fact.Fields["high_variance"])
		}
	})

	t.Run("workload_project_rollup_null_p50_excluded_from_max", func(t *testing.T) {
		const orgID = "org-workload-p50-null-mix"
		seedProject("proj-nullmix", orgID, "linear", "NULLMIX1")
		seedForecast(orgID, "f-nullmix-1", "team-a", "proj-nullmix", nil, 0, 0, ts(2026, 8, 12, 6, 0, 0))
		seedForecast(orgID, "f-nullmix-2", "team-b", "proj-nullmix", p50(20), 0, 0, ts(2026, 8, 12, 6, 0, 0))
		fact := readWorkloadFact(orgID, "linear", "proj-nullmix", contextfabric.FactQuery{})
		if fact == nil {
			t.Fatal("facts = none, want a served roll-up")
		}
		if got := fact.Fields["forecast_p50_days"].Integer; got == nil || *got != 20 {
			t.Fatalf("forecast_p50_days = %#v, want 20 (the NULL row must not win or suppress the known reading)", fact.Fields["forecast_p50_days"])
		}
		if got := fact.Fields["team_breakdown_rows_total"].Integer; got == nil || *got != 2 {
			t.Fatalf("team_breakdown_rows_total = %#v, want 2 (both rows disclosed, even the NULL one)", fact.Fields["team_breakdown_rows_total"])
		}
	})

	t.Run("workload_project_rollup_all_null_p50_discloses_reason", func(t *testing.T) {
		const orgID = "org-workload-p50-all-null"
		seedProject("proj-allnull", orgID, "linear", "ALLNULL1")
		seedForecast(orgID, "f-allnull-1", "team-a", "proj-allnull", nil, 0, 0, ts(2026, 8, 12, 6, 0, 0))
		seedForecast(orgID, "f-allnull-2", "team-b", "proj-allnull", nil, 0, 0, ts(2026, 8, 12, 6, 0, 0))
		fact := readWorkloadFact(orgID, "linear", "proj-allnull", contextfabric.FactQuery{})
		if fact == nil {
			t.Fatal("facts = none, want a served roll-up (the breakdown itself is real)")
		}
		if _, ok := fact.Fields["forecast_p50_days"]; ok {
			t.Fatalf("fields = %#v, want forecast_p50_days absent", fact.Fields)
		}
		if _, ok := fact.Fields["p50_basis"]; ok {
			t.Fatalf("fields = %#v, want p50_basis absent", fact.Fields)
		}
		if got := fact.Fields["p50_unavailable_reason"].String; got == nil || *got != "no_known_forecast_p50_days" {
			t.Fatalf("p50_unavailable_reason = %#v, want no_known_forecast_p50_days", fact.Fields["p50_unavailable_reason"])
		}
		if len(fact.Fields["team_breakdown"].Rows) != 2 {
			t.Fatalf("team_breakdown rows = %#v, want 2 (both NULL rows still disclosed)", fact.Fields["team_breakdown"].Rows)
		}
	})

	// A team's forecast RERUN (same team, same work_scope, a later
	// computed_at) must collapse under row_number() the same way the
	// pre-existing row-level scan already does -- never double-counted into
	// team_breakdown_rows_total, and only the LATEST rerun's own p50/flags
	// contribute to the max. This is the workload analogue of
	// health_project_rollup_duplicate_ownership_source_is_idempotent: this
	// producer has no team_project_ownership hop to duplicate a SOURCE
	// through (it reads a direct work_scope_id match instead), so the
	// idempotency risk here is a duplicate/rerun ROW instead of a
	// duplicate ownership edge -- see RISK-NOTES.
	t.Run("workload_project_rollup_forecast_rerun_is_idempotent_not_double_counted", func(t *testing.T) {
		const orgID = "org-workload-p50-rerun"
		seedProject("proj-rerun", orgID, "linear", "RERUN1")
		seedForecast(orgID, "f-rerun-1", "team-a", "proj-rerun", p50(9), 0, 0, ts(2026, 8, 10, 6, 0, 0))
		seedForecast(orgID, "f-rerun-2", "team-a", "proj-rerun", p50(3), 0, 0, ts(2026, 8, 11, 6, 0, 0))
		seedForecast(orgID, "f-rerun-3", "team-a", "proj-rerun", p50(60), 0, 1, ts(2026, 8, 12, 6, 0, 0))
		fact := readWorkloadFact(orgID, "linear", "proj-rerun", contextfabric.FactQuery{})
		if fact == nil {
			t.Fatal("facts = none, want a served roll-up")
		}
		if got := fact.Fields["team_count"].Integer; got == nil || *got != 1 {
			t.Fatalf("team_count = %#v, want 1 (three reruns of the SAME team collapse to one)", fact.Fields["team_count"])
		}
		if got := fact.Fields["team_breakdown_rows_total"].Integer; got == nil || *got != 1 {
			t.Fatalf("team_breakdown_rows_total = %#v, want 1 (reruns dedupe under row_number() BEFORE the outer GROUP BY counts)", fact.Fields["team_breakdown_rows_total"])
		}
		if got := fact.Fields["forecast_p50_days"].Integer; got == nil || *got != 60 {
			t.Fatalf("forecast_p50_days = %#v, want 60 (the LATEST rerun's own p50, not the first two reruns' 9 or 3)", fact.Fields["forecast_p50_days"])
		}
		if got := fact.Fields["high_variance"].Boolean; got == nil || !*got {
			t.Fatalf("high_variance = %#v, want true (the latest rerun's own flag)", fact.Fields["high_variance"])
		}
	})

	t.Run("workload_project_rollup_as_of_window_excludes_a_later_p50", func(t *testing.T) {
		const orgID = "org-workload-p50-as-of"
		seedProject("proj-asof", orgID, "linear", "ASOF1")
		seedForecast(orgID, "f-asof-1", "team-a", "proj-asof", p50(10), 0, 0, ts(2026, 8, 1, 6, 0, 0))
		seedForecast(orgID, "f-asof-2", "team-a", "proj-asof", p50(90), 0, 0, ts(2026, 8, 20, 6, 0, 0))
		asOf := ts(2026, 8, 10, 0, 0, 0)
		fact := readWorkloadFact(orgID, "linear", "proj-asof", contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalValidTime, AsOf: &asOf},
		})
		if fact == nil {
			t.Fatal("facts = none, want a served roll-up as of 2026-08-10")
		}
		if got := fact.Fields["forecast_p50_days"].Integer; got == nil || *got != 10 {
			t.Fatalf("forecast_p50_days = %#v, want 10 (the 2026-08-20 p50=90 row is AFTER the as-of instant and must not leak in)", fact.Fields["forecast_p50_days"])
		}
	})

	// Two independent caps sit between a contributing row and a served
	// answer: the DISPLAY cap (MaxFactValueRows=64, team_breakdown's own
	// rendered row count) and the row-level scan's own SHARED
	// readers.DefaultRowLimit(200) budget. forecast_p50_days is read from
	// queryProjectWorkloadP50Max's SEPARATE server-side aggregate, which
	// GROUPs BY project and is never subject to either cap -- this seeds
	// past BOTH (210 > 200 > 64) with the worst p50 sorted last by team_key
	// (readers.ReadProjectWorkload's own "ORDER BY p.id, cf.work_scope_id,
	// cf.team_key"), so it is the first row either cap would drop, and
	// proves the served value is still correct.
	t.Run("workload_project_rollup_p50_survives_display_cap_truncation", func(t *testing.T) {
		const orgID = "org-workload-p50-display-cap"
		seedProject("proj-cap", orgID, "linear", "CAP1")
		const teamCount = 210 // > maxFactRowsPerQuery (200) > MaxFactValueRows (64)
		for i := 0; i < teamCount; i++ {
			teamID := "team-cap-" + paddedIndex(i)
			days := uint16(1)
			if i == teamCount-1 {
				days = 365 // the LAST team by sort order carries the worst p50
			}
			seedForecast(orgID, "f-cap-"+paddedIndex(i), teamID, "proj-cap", p50(days), 0, 0, ts(2026, 8, 12, 6, 0, 0))
		}
		workloadProvider := findProvider(t, providers, contextfabric.FactWorkload)
		result, err := workloadProvider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			Kind: contextfabric.FactWorkload, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-cap")},
		})
		if err != nil {
			t.Fatalf("ReadFacts: %v", err)
		}
		if len(result.Facts) == 0 {
			t.Fatal("facts = none, want a served roll-up")
		}
		if !result.Truncated {
			t.Fatalf("Truncated = false, want true (the row-level scan's shared %d-row budget must be exceeded by %d contributing team rows)", 200, teamCount)
		}
		fact := result.Facts[0]
		rows := fact.Fields["team_breakdown"].Rows
		if len(rows) >= teamCount {
			t.Fatalf("team_breakdown rows = %d, want fewer than %d (both caps must have fired)", len(rows), teamCount)
		}
		if got := fact.Fields["forecast_p50_days"].Integer; got == nil || *got != 365 {
			t.Fatalf("forecast_p50_days = %#v, want 365 (the true worst case, even though its own row sits past both the row-level scan's shared budget and the display cap)", fact.Fields["forecast_p50_days"])
		}
	})

	// A project can be ABSENT from the row-level breakdown scan entirely
	// (not merely missing its worst row) while a DIFFERENT project's own
	// rows exhaust the shared budget first -- proving population must come
	// from the uncapped p50 aggregate, never from which projects the
	// row-level scan happened to reach. Both projects are requested in ONE
	// call; the early-sorting project owns enough teams to exhaust the
	// shared 200-row budget by itself, and the late-sorting project's own
	// single row never reaches the scan at all.
	t.Run("workload_project_rollup_late_project_served_from_the_aggregate_with_evidence", func(t *testing.T) {
		const orgID = "org-workload-p50-late-project"
		seedProject("proj-aaa-cap", orgID, "linear", "AAACAP1")
		const teamCount = 210
		for i := 0; i < teamCount; i++ {
			teamID := "team-aaa-cap-" + paddedIndex(i)
			seedForecast(orgID, "f-aaa-cap-"+paddedIndex(i), teamID, "proj-aaa-cap", p50(1), 0, 0, ts(2026, 8, 12, 6, 0, 0))
		}
		seedProject("proj-zzz-late", orgID, "linear", "ZZZLATE1")
		seedForecast(orgID, "f-zzz-late", "team-zzz-late", "proj-zzz-late", p50(90), 1, 0, ts(2026, 8, 12, 6, 0, 0))

		workloadProvider := findProvider(t, providers, contextfabric.FactWorkload)
		result, err := workloadProvider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			Kind: contextfabric.FactWorkload, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-aaa-cap"), projectSubject("linear", "proj-zzz-late")},
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
		if got := late.Fields["forecast_p50_days"].Integer; got == nil || *got != 90 {
			t.Fatalf("proj-zzz-late forecast_p50_days = %#v, want 90 (from the aggregate, independent of the row-level scan)", late.Fields["forecast_p50_days"])
		}
		if got := late.Fields["p50_basis"].String; got == nil || *got != "worst_of_team_breakdown" {
			t.Fatalf("p50_basis = %#v", late.Fields["p50_basis"])
		}
		if got := late.Fields["insufficient_history"].Boolean; got == nil || !*got {
			t.Fatalf("insufficient_history = %#v, want true (the winning row's own flag)", late.Fields["insufficient_history"])
		}
		if _, ok := late.Fields["team_breakdown"]; ok {
			t.Fatalf("fields = %#v, want team_breakdown absent -- the shared scan never reached this project's own row", late.Fields)
		}
		if got := late.Fields["team_breakdown_rows_shown"].Integer; got == nil || *got != 0 {
			t.Fatalf("team_breakdown_rows_shown = %#v, want 0", late.Fields["team_breakdown_rows_shown"])
		}
		if got := late.Fields["team_breakdown_rows_total"].Integer; got == nil || *got != 1 {
			t.Fatalf("team_breakdown_rows_total = %#v, want 1 (the aggregate's own uncapped count)", late.Fields["team_breakdown_rows_total"])
		}
		foundTeamZZZLateRef := false
		for _, ref := range late.EvidenceRefIDs {
			if strings.Contains(ref, "team-zzz-late") {
				foundTeamZZZLateRef = true
			}
		}
		if !foundTeamZZZLateRef {
			t.Fatalf("evidence_ref_ids = %#v, want a ref citing team-zzz-late -- the scope that produced the promoted p50, even though the row-level scan never reached it", late.EvidenceRefIDs)
		}
	})

	// The promoted signal is TEAM-DERIVED: a row with no contributing team
	// must never win the max, however large its own p50_days, and must
	// never be counted as known -- there is no team to cite as evidence
	// for it.
	t.Run("workload_project_rollup_unattributed_forecast_excluded_from_max", func(t *testing.T) {
		const orgID = "org-workload-p50-unattributed"
		seedProject("proj-unattr", orgID, "linear", "UNATTR1")
		seedForecastUnattributed(orgID, "f-unattr-1", "proj-unattr", p50(999), 0, 0, ts(2026, 8, 12, 6, 0, 0))
		seedForecast(orgID, "f-unattr-2", "team-a", "proj-unattr", p50(10), 0, 0, ts(2026, 8, 12, 6, 0, 0))
		fact := readWorkloadFact(orgID, "linear", "proj-unattr", contextfabric.FactQuery{})
		if fact == nil {
			t.Fatal("facts = none, want a served roll-up")
		}
		if got := fact.Fields["forecast_p50_days"].Integer; got == nil || *got != 10 {
			t.Fatalf("forecast_p50_days = %#v, want 10 (team-a's own reading -- the unattributed row's 999 must never win)", fact.Fields["forecast_p50_days"])
		}
		if got := fact.Fields["p50_known_count"].Integer; got == nil || *got != 1 {
			t.Fatalf("p50_known_count = %#v, want 1", fact.Fields["p50_known_count"])
		}
		if got := fact.Fields["p50_excluded_unattributed_count"].Integer; got == nil || *got != 1 {
			t.Fatalf("p50_excluded_unattributed_count = %#v, want 1", fact.Fields["p50_excluded_unattributed_count"])
		}
	})

	t.Run("workload_project_rollup_all_unattributed_discloses_reason", func(t *testing.T) {
		const orgID = "org-workload-p50-all-unattributed"
		seedProject("proj-allunattr", orgID, "linear", "ALLUNATTR1")
		// A SECOND unattributed row for the SAME project would collide
		// under the base read's own row_number() dedup -- team_id NULL and
		// work_scope_id fixed to the project's own identity give both rows
		// the IDENTICAL partition key, so only the latest survives; a
		// project can only ever carry ONE unattributed row post-dedup, and
		// that is what real capacity_forecasts data can produce. One real,
		// unattributed row is sufficient to pin "the only reachable row
		// carries no contributing team".
		seedForecastUnattributed(orgID, "f-allunattr-1", "proj-allunattr", p50(30), 0, 0, ts(2026, 8, 12, 6, 0, 0))
		fact := readWorkloadFact(orgID, "linear", "proj-allunattr", contextfabric.FactQuery{})
		if fact == nil {
			t.Fatal("facts = none, want a served roll-up (the breakdown itself is real)")
		}
		if _, ok := fact.Fields["forecast_p50_days"]; ok {
			t.Fatalf("fields = %#v, want forecast_p50_days absent -- the reading is real but has no contributing team", fact.Fields)
		}
		if got := fact.Fields["p50_unavailable_reason"].String; got == nil || *got != "no_known_forecast_p50_days" {
			t.Fatalf("p50_unavailable_reason = %#v", fact.Fields["p50_unavailable_reason"])
		}
		if got := fact.Fields["p50_excluded_unattributed_count"].Integer; got == nil || *got != 1 {
			t.Fatalf("p50_excluded_unattributed_count = %#v, want 1", fact.Fields["p50_excluded_unattributed_count"])
		}
	})

	// The three-way population partition must always sum to the total: a
	// mix of a known reading, an unattributed reading and a null-p50
	// reading in ONE project.
	t.Run("workload_project_rollup_partition_counts_sum_to_total", func(t *testing.T) {
		const orgID = "org-workload-p50-partition"
		seedProject("proj-partition", orgID, "linear", "PARTITION1")
		seedForecast(orgID, "f-partition-known", "team-a", "proj-partition", p50(15), 0, 0, ts(2026, 8, 12, 6, 0, 0))
		seedForecastUnattributed(orgID, "f-partition-unattr", "proj-partition", p50(500), 0, 0, ts(2026, 8, 12, 6, 0, 0))
		seedForecast(orgID, "f-partition-nullp50", "team-b", "proj-partition", nil, 0, 0, ts(2026, 8, 12, 6, 0, 0))
		fact := readWorkloadFact(orgID, "linear", "proj-partition", contextfabric.FactQuery{})
		if fact == nil {
			t.Fatal("facts = none, want a served roll-up")
		}
		known := fact.Fields["p50_known_count"].Integer
		unattr := fact.Fields["p50_excluded_unattributed_count"].Integer
		nullP50 := fact.Fields["p50_excluded_null_p50_count"].Integer
		total := fact.Fields["team_breakdown_rows_total"].Integer
		if known == nil || unattr == nil || nullP50 == nil || total == nil {
			t.Fatalf("partition fields = known:%v unattributed:%v null_p50:%v total:%v, want all present", known, unattr, nullP50, total)
		}
		if *known != 1 || *unattr != 1 || *nullP50 != 1 {
			t.Fatalf("partition = known:%d unattributed:%d null_p50:%d, want 1/1/1", *known, *unattr, *nullP50)
		}
		if *known+*unattr+*nullP50 != *total {
			t.Fatalf("partition does not sum to the total: known:%d + unattributed:%d + null_p50:%d = %d, want %d", *known, *unattr, *nullP50, *known+*unattr+*nullP50, *total)
		}
		if got := fact.Fields["forecast_p50_days"].Integer; got == nil || *got != 15 {
			t.Fatalf("forecast_p50_days = %#v, want 15 (the only known, attributed reading)", fact.Fields["forecast_p50_days"])
		}
	})

	// The winner-selection comparator must rank by (attributed-known,
	// p50_days, hash) in that order -- a mutant that ranks by hash before
	// p50_days would pick the WRONG winner whenever a smaller-p50 row's own
	// hash exceeds the true winner's. A fake client cannot prove this (it
	// only matches SQL text and returns canned rows); this computes the
	// real server's own cityHash64 for a set of candidate rows FIRST, picks
	// one whose hash provably exceeds the true winner's while carrying a
	// SMALLER p50, and only then seeds and asserts -- so the adversarial
	// condition is verified to hold before it is relied on, never assumed.
	t.Run("workload_project_rollup_winner_selection_survives_adversarial_hash_order", func(t *testing.T) {
		const orgID = "org-workload-p50-hash-adversarial"
		const workScopeID = "proj-hash-adv"
		const winnerTeam = "team-hash-adv-winner"
		const winnerP50 = 50

		var winnerHash uint64
		if err := direct.QueryRow(ctx, `SELECT cityHash64(tuple(?, ?, toInt64(?)))`, winnerTeam, workScopeID, uint64(winnerP50)).Scan(&winnerHash); err != nil {
			t.Fatalf("compute winner hash: %v", err)
		}

		var decoyTeam string
		var decoyHash uint64
		rows, err := direct.Query(ctx, `SELECT team_key, cityHash64(tuple(team_key, ?, toInt64(10))) AS h
FROM (SELECT arrayJoin(arrayMap(x -> concat('team-hash-adv-decoy-', toString(x)), range(200))) AS team_key)
ORDER BY h DESC`, workScopeID)
		if err != nil {
			t.Fatalf("query candidate decoy hashes: %v", err)
		}
		found := false
		for rows.Next() {
			var team string
			var h uint64
			if err := rows.Scan(&team, &h); err != nil {
				rows.Close()
				t.Fatalf("scan candidate decoy hash: %v", err)
			}
			if h > winnerHash {
				decoyTeam, decoyHash = team, h
				found = true
				break
			}
		}
		rows.Close()
		if !found {
			t.Fatal("fixture defect: no candidate decoy team's hash exceeds the winner's own hash among 200 candidates -- widen the candidate pool")
		}
		t.Logf("adversarial fixture: winner hash=%d (p50=%d), decoy hash=%d (p50=10, team=%s)", winnerHash, winnerP50, decoyHash, decoyTeam)

		seedProject("proj-hash-adv", orgID, "linear", "HASHADV1")
		seedForecast(orgID, "f-hash-adv-winner", winnerTeam, workScopeID, p50(winnerP50), 0, 0, ts(2026, 8, 12, 6, 0, 0))
		seedForecast(orgID, "f-hash-adv-decoy", decoyTeam, workScopeID, p50(10), 0, 0, ts(2026, 8, 12, 6, 0, 0))
		fact := readWorkloadFact(orgID, "linear", "proj-hash-adv", contextfabric.FactQuery{})
		if fact == nil {
			t.Fatal("facts = none, want a served roll-up")
		}
		// A hash-first comparator would rank the decoy (the larger hash)
		// above the winner and promote its own p50 (10) instead of the
		// true worst case (50) -- this is the exact mutation class a
		// SQL-text-matching fake client cannot see.
		if got := fact.Fields["forecast_p50_days"].Integer; got == nil || *got != winnerP50 {
			t.Fatalf("forecast_p50_days = %#v, want %d (the true worst p50 -- a comparator ranking by hash before p50 would instead promote the decoy's 10)", fact.Fields["forecast_p50_days"], winnerP50)
		}
	})
}
