package devhealthfacts_test

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// newCHAOS3780IntegrationClient starts a real (non-TLS) ClickHouse
// container -- mirroring devhealthsource's
// newDevHealthClickHouseIntegrationClient -- because none of the findings
// below (row_number()/PARTITION BY tie-breaking, ReplacingMergeTree FINAL
// dedup, a real LIMIT clause) can be exercised by this package's fakeClient:
// it returns whatever rows a test hands it verbatim, it does not execute
// SQL. Proving CHAOS-3780's Codex round-1 fixes (F1-F4) actually hold
// requires a real server evaluating the real query text.
func newCHAOS3780IntegrationClient(t *testing.T, ctx context.Context) (query *runtimeclickhouse.Client, direct clickhousedriver.Conn) {
	t.Helper()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: "clickhouse/clickhouse-server:24.8", ExposedPorts: []string{"9000/tcp"},
			Env:        map[string]string{"CLICKHOUSE_USER": "acr", "CLICKHOUSE_PASSWORD": "acr", "CLICKHOUSE_DB": "default"},
			WaitingFor: wait.ForListeningPort("9000/tcp").WithStartupTimeout(2 * time.Minute),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start ClickHouse container: %v", err)
	}
	t.Cleanup(func() {
		if err := container.Terminate(context.Background()); err != nil {
			t.Errorf("terminate ClickHouse container: %v", err)
		}
	})
	host, err := container.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := container.MappedPort(ctx, "9000/tcp")
	if err != nil {
		t.Fatal(err)
	}
	addr := net.JoinHostPort(host, port.Port())

	direct, err = clickhousedriver.Open(&clickhousedriver.Options{
		Addr: []string{addr}, Auth: clickhousedriver.Auth{Database: "default", Username: "acr", Password: "acr"}, DialTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("open native ClickHouse connection: %v", err)
	}
	t.Cleanup(func() {
		if err := direct.Close(); err != nil {
			t.Errorf("close native ClickHouse connection: %v", err)
		}
	})
	pingDeadline := time.Now().Add(30 * time.Second)
	for {
		if pingErr := direct.Ping(ctx); pingErr == nil {
			break
		} else if time.Now().After(pingDeadline) {
			t.Fatalf("clickhouse not ready for connections: %v", pingErr)
		}
		time.Sleep(500 * time.Millisecond)
	}

	query, err = runtimeclickhouse.NewClickHouseQueryClientWithOptions(runtimeclickhouse.Options{
		DSN: "clickhouse://acr:acr@" + addr + "/default", DialTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("open production ClickHouse query client: %v", err)
	}
	t.Cleanup(func() {
		if err := query.Close(); err != nil {
			t.Errorf("close production ClickHouse query client: %v", err)
		}
	})
	return query, direct
}

// repositoryMetricsTimeContext (CHAOS-4418) spans a fixed calendar range
// explicitly instead of leaning on readRepositoryMetrics' own default
// window. That default (metricsSeriesDefaultWindow, 90 days) trails the
// REAL wall clock, while every fixture below seeds a fixed calendar day --
// so a bare TemporalCurrent read returns zero rows the moment CI runs more
// than 90 days after that anchor, and the test fails for the calendar
// rather than for the behaviour it claims to pin. This is a calendar time
// bomb introduced by CHAOS-4418 making this reader window-scoped at all
// (before it, the reader took the latest day regardless of any window);
// F2_metrics_truncates_one_repositorys_own_series_past_its_per_repository_cap
// hit it on CI first. An explicit window is also the more faithful test of
// each claim: the per-repository SQL cap, the intraday dedup, and the
// org-scoping all fire independently of the window logic.
func repositoryMetricsTimeContext(from, to time.Time) contextfabric.TimeContext {
	return contextfabric.TimeContext{
		Axis:           contextfabric.TemporalCurrent,
		EvidenceWindow: &contractsv1.ContextFabricRequestedEvidenceWindow{Start: &from, End: &to},
	}
}

// devhealthschema:not-a-production-replica the table names passed to devhealthschema.DDL below
// select what to render; the schema itself is the declaration's, not this
// file's.
// createCHAOS3780Tables creates every source table CHAOS-3780's providers
// read, with the real production engine, sort key, and nullability shape
// (verified against the live dev ClickHouse instance during the Codex
// round-1 probe -- see the commit message and team-lead report for the
// exact `DESCRIBE TABLE`/`SHOW CREATE TABLE` evidence).
func createCHAOS3780Tables(t *testing.T, ctx context.Context, connection clickhousedriver.Conn) {
	t.Helper()
	// Rendered from the shared production declaration (CHAOS-3781 round-3
	// F4). These six tables were hand-written here, which is the stale
	// replica risk devhealthschema exists to remove: a type corrected
	// upstream would have been fixed in the declaration and in the parity
	// guards while this file quietly kept testing the old shape.
	// CHAOS-4398: work_unit_investments/work_item_team_attributions/repos
	// added so InvestmentProvider's NEW readTeamThemeMix call (issued
	// alongside every legacy investment_metrics_daily read) has real
	// tables to query -- left unseeded in every F4 scenario below, which
	// exercises the zero-current-effort skip path (readTeamThemeMix emits
	// no fact, never affecting these tests' own `len(result.Facts)`
	// assertions).
	for _, statement := range devhealthschema.DDL("repo_metrics_daily", "compounding_risk_daily", "capacity_forecasts", "investment_metrics_daily", "estimate_coverage_metrics_daily", "recommendations_daily", "work_unit_investments", "work_item_team_attributions", "repos") {
		if err := connection.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
}

// TestCHAOS3780FindingsAgainstRealClickHouse is the F5 real-ClickHouse proof
// Codex round-1 asked for: none of F1-F4's fixes (row_number() tie-breaking,
// a fired predicate applied AFTER latest-row selection, per-scope
// partitioning, FINAL dedup) can be exercised by the fakeClient, which never
// executes SQL. One shared container backs every scenario below; each uses
// its own org_id to stay isolated from the others.
func TestCHAOS3780FindingsAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	createCHAOS3780Tables(t, ctx, direct)
	providers := devhealthfacts.NewProviders(query)

	t.Run("F1_deficiency_does_not_resurrect_after_ops_clears_it", func(t *testing.T) {
		const orgID = "org-f1"
		// The exact live-data shape: an OLDER window fired=true, a NEWER
		// window (later window_end) fired=false -- Ops already cleared it.
		if err := direct.Exec(ctx, `INSERT INTO recommendations_daily (team_id, org_id, rule_id, window_start, window_end, fired, severity, title, rationale, success_criterion, computed_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			"TEAM1", orgID, "saturation", date(2026, 7, 25), date(2026, 8, 8), true, "critical", "Saturation", "was high", "drops below threshold", ts(2026, 8, 8, 2, 0, 0)); err != nil {
			t.Fatalf("seed old fired row: %v", err)
		}
		if err := direct.Exec(ctx, `INSERT INTO recommendations_daily (team_id, org_id, rule_id, window_start, window_end, fired, severity, title, rationale, success_criterion, computed_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			"TEAM1", orgID, "saturation", date(2026, 7, 29), date(2026, 8, 12), false, "critical", "Saturation", "cleared", "drops below threshold", ts(2026, 8, 12, 2, 0, 0)); err != nil {
			t.Fatalf("seed new cleared row: %v", err)
		}
		provider := findProvider(t, providers, contextfabric.FactOperationalDeficiencies)
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			Kind: contextfabric.FactOperationalDeficiencies,
			Subjects: []contextfabric.SubjectRef{{
				Kind: contextfabric.SubjectTeam, CanonicalID: "team:TEAM1", Label: "TEAM1",
			}},
		})
		if err != nil {
			t.Fatalf("ReadFacts() error = %v", err)
		}
		if len(result.Facts) != 0 {
			t.Fatalf("facts = %#v, want EMPTY -- the truly latest evaluation cleared this rule, it must not resurface", result.Facts)
		}
	})

	t.Run("F1_deficiency_still_reports_when_latest_evaluation_is_fired", func(t *testing.T) {
		const orgID = "org-f1b"
		if err := direct.Exec(ctx, `INSERT INTO recommendations_daily (team_id, org_id, rule_id, window_start, window_end, fired, severity, title, rationale, success_criterion, computed_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			"TEAM1", orgID, "saturation", date(2026, 7, 29), date(2026, 8, 12), true, "critical", "Saturation", "still high", "drops below threshold", ts(2026, 8, 12, 2, 0, 0)); err != nil {
			t.Fatalf("seed latest fired row: %v", err)
		}
		provider := findProvider(t, providers, contextfabric.FactOperationalDeficiencies)
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			Kind: contextfabric.FactOperationalDeficiencies,
			Subjects: []contextfabric.SubjectRef{{
				Kind: contextfabric.SubjectTeam, CanonicalID: "team:TEAM1", Label: "TEAM1",
			}},
		})
		if err != nil {
			t.Fatalf("ReadFacts() error = %v", err)
		}
		if len(result.Facts) != 1 || result.Facts[0].Fields["title"].String == nil || *result.Facts[0].Fields["title"].String != "Saturation" {
			t.Fatalf("facts = %#v, want exactly 1 fired deficiency", result.Facts)
		}
	})

	// FINAL_binding_later_version_wins_over_replaced_key is Codex round-2
	// gap #3: recommendations_daily's ReplacingMergeTree(computed_at)
	// sorting key is (org_id, team_id, rule_id, window_end) -- two rows
	// sharing that EXACT key are two "versions" of one logical row. This
	// seeds both an old and a new version and asserts the new version's
	// fields (not the old one's) are what the provider reads.
	//
	// Verified by hand (delete FINAL, rerun, restore): this specific
	// assertion does NOT fail when FROM recommendations_daily FINAL is
	// changed to FROM recommendations_daily. Reported, not silently
	// "fixed" to force a red/green result -- the empirical finding is
	// itself the useful signal here. The reason is structural: this
	// provider's row_number() ORDER BY already includes computed_at
	// DESC as its second term, and computed_at is ALSO
	// ReplacingMergeTree(computed_at)'s own version column -- "keep the
	// row with the max version" and "rank by computed_at DESC, take
	// rn=1" are the same selection rule over the same column, so FINAL
	// can never disagree with what row_number() already picks. The same
	// holds for capacity_forecasts and estimate_coverage_metrics_daily
	// (both also ReplacingMergeTree(computed_at), both also carry
	// computed_at DESC as an ORDER BY term). FINAL stays -- it matches
	// this package's convention (every one of the other providers
	// applies it) and is free insurance against a future edit that
	// reorders or drops computed_at from an ORDER BY clause -- but no
	// correctness claim in this package rests on it alone.
	t.Run("FINAL_binding_later_version_wins_over_replaced_key", func(t *testing.T) {
		const orgID = "org-final"
		if err := direct.Exec(ctx, `INSERT INTO recommendations_daily (team_id, org_id, rule_id, window_start, window_end, fired, severity, title, rationale, success_criterion, computed_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			"TEAM1", orgID, "saturation", date(2026, 7, 29), date(2026, 8, 12), true, "critical", "OLD VERSION", "stale", "drops below threshold", ts(2026, 8, 12, 2, 0, 0)); err != nil {
			t.Fatalf("seed old version: %v", err)
		}
		// Same (org_id, team_id, rule_id, window_end) -- the replacing key --
		// a later computed_at, different content.
		if err := direct.Exec(ctx, `INSERT INTO recommendations_daily (team_id, org_id, rule_id, window_start, window_end, fired, severity, title, rationale, success_criterion, computed_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			"TEAM1", orgID, "saturation", date(2026, 7, 29), date(2026, 8, 12), true, "high", "NEW VERSION", "fresh", "drops below threshold", ts(2026, 8, 12, 20, 0, 0)); err != nil {
			t.Fatalf("seed new version: %v", err)
		}
		provider := findProvider(t, providers, contextfabric.FactOperationalDeficiencies)
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			Kind: contextfabric.FactOperationalDeficiencies,
			Subjects: []contextfabric.SubjectRef{{
				Kind: contextfabric.SubjectTeam, CanonicalID: "team:TEAM1", Label: "TEAM1",
			}},
		})
		if err != nil {
			t.Fatalf("ReadFacts() error = %v", err)
		}
		if len(result.Facts) != 1 {
			t.Fatalf("facts = %#v, want exactly 1 (the replaced key collapses to one logical row)", result.Facts)
		}
		title := result.Facts[0].Fields["title"].String
		severity := result.Facts[0].Fields["severity"].String
		if title == nil || *title != "NEW VERSION" || severity == nil || *severity != "high" {
			t.Fatalf("fields = %#v, want the NEW VERSION (later computed_at)", result.Facts[0].Fields)
		}
	})

	t.Run("F2_metrics_whole_fresh_row_wins_never_a_stitched_combination", func(t *testing.T) {
		const orgID = "org-f2"
		repoID := "22222222-2222-2222-2222-222222222222"
		// Same day, two reruns (the live-data shape), each with a
		// DIFFERENT computed_at and internally consistent but mutually
		// exclusive values -- if independent argMax() per field were
		// still used, a value from one row could pair with a value from
		// the other.
		if err := direct.Exec(ctx, `INSERT INTO repo_metrics_daily (repo_id, org_id, day, commits_count, prs_merged, median_pr_cycle_hours, change_failure_rate, mttr_hours, bus_factor, code_ownership_gini, computed_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			repoID, orgID, date(2026, 8, 12), uint32(10), uint32(1), 5.0, 0.05, nil, uint32(2), 0.1, ts(2026, 8, 12, 10, 0, 0)); err != nil {
			t.Fatalf("seed stale metrics row: %v", err)
		}
		if err := direct.Exec(ctx, `INSERT INTO repo_metrics_daily (repo_id, org_id, day, commits_count, prs_merged, median_pr_cycle_hours, change_failure_rate, mttr_hours, bus_factor, code_ownership_gini, computed_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			repoID, orgID, date(2026, 8, 12), uint32(99), uint32(42), 12.5, 0.5, 3.5, uint32(9), 0.9, ts(2026, 8, 12, 22, 0, 0)); err != nil {
			t.Fatalf("seed fresh metrics row: %v", err)
		}
		provider := findProvider(t, providers, contextfabric.FactMetrics)
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
			Time: repositoryMetricsTimeContext(date(2026, 8, 1), date(2026, 8, 31)),
			Kind: contextfabric.FactMetrics, Subjects: []contextfabric.SubjectRef{repoSubject(repoID)},
		})
		if err != nil {
			t.Fatalf("ReadFacts() error = %v", err)
		}
		if len(result.Facts) != 1 {
			t.Fatalf("facts = %#v, want 1", result.Facts)
		}
		fact := result.Facts[0]
		dailyMetrics := fact.Fields["daily_metrics"].Rows
		if len(dailyMetrics) != 1 {
			t.Fatalf("daily_metrics = %#v, want exactly 1 row -- the same-day rerun must collapse to one, not two series entries", dailyMetrics)
		}
		row := dailyMetrics[0].Fields
		commits := row["commits_count"].Integer
		prs := row["prs_merged"].Integer
		if commits == nil || *commits != 99 || prs == nil || *prs != 42 {
			t.Fatalf("fields = %#v, want the fresh row's whole combination (commits_count=99, prs_merged=42), not a stitched mix", row)
		}
		if row["mttr_hours"].Number == nil || *row["mttr_hours"].Number != 3.5 {
			t.Fatalf("fields = %#v, want the fresh row's mttr_hours=3.5", row)
		}
	})

	// F2_health_whole_fresh_row_wins_never_a_stitched_combination is the
	// health.go counterpart of the metrics.go F2 test above (Codex round-2
	// gap #1): it must be impossible for health.go to regress to
	// independent per-field argMax without a real-suite test noticing --
	// this seeds a same-day rerun with a DIFFERENT severity AND a
	// DIFFERENT compounding_risk on the fresh row, and asserts both come
	// from the SAME (freshest) row.
	t.Run("F2_health_whole_fresh_row_wins_never_a_stitched_combination", func(t *testing.T) {
		const orgID = "org-f2-health"
		repoID := "55555555-5555-5555-5555-555555555555"
		if err := direct.Exec(ctx, `INSERT INTO compounding_risk_daily (org_id, scope, scope_id, day, severity, compounding_risk, computed_at) VALUES (?,?,?,?,?,?,?)`,
			orgID, "repo", repoID, date(2026, 8, 12), "low", 0.1, ts(2026, 8, 12, 10, 0, 0)); err != nil {
			t.Fatalf("seed stale health row: %v", err)
		}
		if err := direct.Exec(ctx, `INSERT INTO compounding_risk_daily (org_id, scope, scope_id, day, severity, compounding_risk, computed_at) VALUES (?,?,?,?,?,?,?)`,
			orgID, "repo", repoID, date(2026, 8, 12), "high", 0.95, ts(2026, 8, 12, 22, 0, 0)); err != nil {
			t.Fatalf("seed fresh health row: %v", err)
		}
		provider := findProvider(t, providers, contextfabric.FactHealth)
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			Kind: contextfabric.FactHealth, Subjects: []contextfabric.SubjectRef{repoSubject(repoID)},
		})
		if err != nil {
			t.Fatalf("ReadFacts() error = %v", err)
		}
		if len(result.Facts) != 1 {
			t.Fatalf("facts = %#v, want 1", result.Facts)
		}
		fact := result.Facts[0]
		severity := fact.Fields["severity"].String
		risk := fact.Fields["compounding_risk"].Number
		if severity == nil || *severity != "high" || risk == nil || *risk != 0.95 {
			t.Fatalf("fields = %#v, want the fresh row's whole combination (severity=high, compounding_risk=0.95), not a stitched mix -- this is exactly what would still pass if health.go regressed to independent argMax(severity,...)/argMax(compounding_risk,...)", fact.Fields)
		}
	})

	// M1_identical_computed_at_tie_resolves_to_the_same_row_every_time is
	// Codex round-2 gap #2: whole-row row_number() selection kills field
	// stitching, but without a total order two rows tied on every ORDER BY
	// term before the final tiebreaker could still let a DIFFERENT one win
	// on different executions of the identical query -- a fact that flaps
	// with no data change. This seeds two rows in the SAME partition with
	// the SAME day and the SAME computed_at (the live 86-way tie shape
	// found in compounding_risk_daily), runs the query twice, and asserts
	// the same severity wins both times.
	t.Run("M1_identical_computed_at_tie_resolves_to_the_same_row_every_time", func(t *testing.T) {
		const orgID = "org-m1-tie"
		// Manual verification against real ClickHouse (docker exec,
		// clickhouse-client, day DESC/computed_at DESC only, no further
		// tiebreaker) showed a single tied pair flip in roughly half of 10
		// repeated executions of the IDENTICAL query against UNCHANGED
		// data -- genuinely non-deterministic. That flip did not reproduce
		// through 10 repeated calls to the SAME provider instance in a Go
		// test (the physical part layout does not change across calls a
		// few milliseconds apart, so repeat-the-same-call is not a
		// reliable reproduction from Go). What DOES reproduce it
		// reliably: many independent (repo_id, tied pair) partitions
		// evaluated within ONE query execution -- ClickHouse's per-
		// partition window evaluation is not guaranteed to resolve a tie
		// the same way across partitions, so this seeds 40 repositories,
		// each with its own identical-computed_at tie between "low" and
		// "elevated", and asserts every one resolves the same way. Without
		// the tiebreaker this is flaky (some repos land on "low", others
		// on "elevated", nondeterministically); with it, all 40 agree.
		const repoCount = 40
		subjects := make([]contextfabric.SubjectRef, 0, repoCount)
		tiedAt := ts(2026, 8, 12, 12, 0, 0)
		for i := 0; i < repoCount; i++ {
			repoID := "77777777-7777-7777-7777-" + padHex(i)
			if err := direct.Exec(ctx, `INSERT INTO compounding_risk_daily (org_id, scope, scope_id, day, severity, compounding_risk, computed_at) VALUES (?,?,?,?,?,?,?)`,
				orgID, "repo", repoID, date(2026, 8, 12), "low", 0.2, tiedAt); err != nil {
				t.Fatalf("seed tied row A for repo %d: %v", i, err)
			}
			if err := direct.Exec(ctx, `INSERT INTO compounding_risk_daily (org_id, scope, scope_id, day, severity, compounding_risk, computed_at) VALUES (?,?,?,?,?,?,?)`,
				orgID, "repo", repoID, date(2026, 8, 12), "elevated", 0.6, tiedAt); err != nil {
				t.Fatalf("seed tied row B for repo %d: %v", i, err)
			}
			subjects = append(subjects, repoSubject(repoID))
		}
		provider := findProvider(t, providers, contextfabric.FactHealth)
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			Kind: contextfabric.FactHealth, Subjects: subjects,
		})
		if err != nil {
			t.Fatalf("ReadFacts() error = %v", err)
		}
		if len(result.Facts) != repoCount {
			t.Fatalf("facts = %d, want %d", len(result.Facts), repoCount)
		}
		seen := map[string]int{}
		for _, fact := range result.Facts {
			if fact.Fields["severity"].String != nil {
				seen[*fact.Fields["severity"].String]++
			}
		}
		if len(seen) != 1 {
			t.Fatalf("severity outcomes across %d identically-tied repositories = %#v, want all %d to agree -- row_number() has no total order", repoCount, seen, repoCount)
		}
	})

	t.Run("F2_metrics_org_and_subject_scoped_on_real_data", func(t *testing.T) {
		const orgA, orgB = "org-f2-iso-a", "org-f2-iso-b"
		repoID := "33333333-3333-3333-3333-333333333333"
		if err := direct.Exec(ctx, `INSERT INTO repo_metrics_daily (repo_id, org_id, day, commits_count, prs_merged, median_pr_cycle_hours, change_failure_rate, mttr_hours, bus_factor, code_ownership_gini, computed_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			repoID, orgA, date(2026, 8, 12), uint32(5), uint32(1), 1.0, 0.0, nil, uint32(1), 0.1, ts(2026, 8, 12, 10, 0, 0)); err != nil {
			t.Fatalf("seed org-a metrics row: %v", err)
		}
		// Same repo_id, a DIFFERENT organization, with a value that must
		// never leak into org-a's answer.
		if err := direct.Exec(ctx, `INSERT INTO repo_metrics_daily (repo_id, org_id, day, commits_count, prs_merged, median_pr_cycle_hours, change_failure_rate, mttr_hours, bus_factor, code_ownership_gini, computed_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			repoID, orgB, date(2026, 8, 12), uint32(777), uint32(777), 777.0, 0.9, nil, uint32(1), 0.1, ts(2026, 8, 12, 10, 0, 0)); err != nil {
			t.Fatalf("seed org-b metrics row: %v", err)
		}
		provider := findProvider(t, providers, contextfabric.FactMetrics)
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgA}, contextfabric.FactQuery{
			Time: repositoryMetricsTimeContext(date(2026, 8, 1), date(2026, 8, 31)),
			Kind: contextfabric.FactMetrics, Subjects: []contextfabric.SubjectRef{repoSubject(repoID)},
		})
		if err != nil {
			t.Fatalf("ReadFacts() error = %v", err)
		}
		orgADailyMetrics := result.Facts[0].Fields["daily_metrics"].Rows
		if len(result.Facts) != 1 || len(orgADailyMetrics) != 1 || orgADailyMetrics[0].Fields["commits_count"].Integer == nil || *orgADailyMetrics[0].Fields["commits_count"].Integer != 5 {
			t.Fatalf("facts = %#v, want org-a's own row (commits_count=5), never org-b's colliding repo_id row", result.Facts)
		}
	})

	// F2_metrics_no_cross_repository_starvation_beyond_the_old_query_wide_cap
	// is CHAOS-4418's own re-pin of this real-ClickHouse test (codex R1/R2:
	// this scenario -- 201 DIFFERENT repositories, one row each -- used to
	// be exactly where the OLD query-wide maxFactRowsPerQuery (200) cap
	// fired, truncating the 201st repository's data away even though every
	// repository individually had a real, tiny answer to give. That WAS
	// the starvation bug this ticket exists to fix, not a correct
	// behavior to keep pinned: readRepositoryMetrics no longer has a
	// query-wide LIMIT at all (a per-repository `LIMIT n BY repo_id`
	// instead), so all 201 repositories must now come back with their own
	// real 1-row fact, untruncated.
	t.Run("F2_metrics_no_cross_repository_starvation_beyond_the_old_query_wide_cap", func(t *testing.T) {
		const orgID = "org-f2-trunc"
		const repoCount = 201 // formerly maxFactRowsPerQuery (200) + 1
		subjects := make([]contextfabric.SubjectRef, 0, repoCount)
		for i := 0; i < repoCount; i++ {
			repoID := "44444444-4444-4444-4444-" + padHex(i)
			if err := direct.Exec(ctx, `INSERT INTO repo_metrics_daily (repo_id, org_id, day, commits_count, prs_merged, median_pr_cycle_hours, change_failure_rate, mttr_hours, bus_factor, code_ownership_gini, computed_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
				repoID, orgID, date(2026, 8, 12), uint32(1), uint32(1), 1.0, 0.0, nil, uint32(1), 0.1, ts(2026, 8, 12, 10, 0, 0)); err != nil {
				t.Fatalf("seed repository %d: %v", i, err)
			}
			subjects = append(subjects, repoSubject(repoID))
		}
		provider := findProvider(t, providers, contextfabric.FactMetrics)
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
			Time: repositoryMetricsTimeContext(date(2026, 8, 1), date(2026, 8, 31)),
			Kind: contextfabric.FactMetrics, Subjects: subjects,
		})
		if err != nil {
			t.Fatalf("ReadFacts() error = %v", err)
		}
		if result.Truncated {
			t.Fatalf("result.Truncated = true, want false: %d distinct repositories each with their own 1-row series must not starve each other out of a shared budget that no longer exists", repoCount)
		}
		if len(result.Facts) != repoCount {
			t.Fatalf("len(result.Facts) = %d, want exactly %d -- every requested repository must get its own fact, none silently dropped", len(result.Facts), repoCount)
		}
	})

	// F2_metrics_truncates_one_repositorys_own_series_past_its_per_repository_cap
	// pins the REAL truncation shape post-CHAOS-4418: metricsSeriesPerRepositoryRowCap
	// (200, metrics.go) bounds ONE repository's own day series via `LIMIT
	// 200 BY repo_id`, independent of how many other repositories are
	// requested alongside it.
	t.Run("F2_metrics_truncates_one_repositorys_own_series_past_its_per_repository_cap", func(t *testing.T) {
		const orgID = "org-f2-trunc-one-repo"
		const repoID = "55555555-4444-4444-4444-000000000001"
		const dayCount = 201 // metricsSeriesPerRepositoryRowCap (200) + 1
		start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
		for i := 0; i < dayCount; i++ {
			day := start.AddDate(0, 0, i)
			if err := direct.Exec(ctx, `INSERT INTO repo_metrics_daily (repo_id, org_id, day, commits_count, prs_merged, median_pr_cycle_hours, change_failure_rate, mttr_hours, bus_factor, code_ownership_gini, computed_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
				repoID, orgID, day, uint32(1), uint32(1), 1.0, 0.0, nil, uint32(1), 0.1, day.Add(10*time.Hour)); err != nil {
				t.Fatalf("seed day %d: %v", i, err)
			}
		}
		provider := findProvider(t, providers, contextfabric.FactMetrics)
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
			Time: repositoryMetricsTimeContext(start, start.AddDate(0, 0, dayCount)),
			Kind: contextfabric.FactMetrics, Subjects: []contextfabric.SubjectRef{repoSubject(repoID)},
		})
		if err != nil {
			t.Fatalf("ReadFacts() error = %v", err)
		}
		if !result.Truncated {
			t.Fatalf("result.Truncated = false, want true: %d real days for ONE repository exceeds its own per-repository cap", dayCount)
		}
		if len(result.Facts) != 1 {
			t.Fatalf("len(result.Facts) = %d, want exactly 1", len(result.Facts))
		}
		// capFactValueRows' own 64-row per-fact cap is stricter than the
		// 200-row SQL cap, so the RENDERED table is 64 rows either way --
		// this test's own point is that the SQL cap does not itself
		// silently drop the whole fact or error the query, which the
		// production MaxResultRows driver setting (codex R3) is what
		// actually protects against for a wide multi-repository request.
		if got := len(result.Facts[0].Fields["daily_metrics"].Rows); got != 64 {
			t.Fatalf("daily_metrics rows = %d, want 64 (capFactValueRows' own cap)", got)
		}
	})

	t.Run("F3_workload_emits_one_fact_per_work_scope_not_a_silent_collapse", func(t *testing.T) {
		const orgID = "org-f3"
		at := ts(2026, 8, 10, 4, 0, 1)
		if err := direct.Exec(ctx, `INSERT INTO capacity_forecasts (forecast_id, computed_at, team_id, work_scope_id, backlog_size, p50_days, throughput_mean, throughput_stddev, org_id) VALUES (?,?,?,?,?,?,?,?,?)`,
			"forecast-a", at, "TEAM1", "scope-a", uint32(10), uint16(4), 3.0, 0.5, orgID); err != nil {
			t.Fatalf("seed scope-a forecast: %v", err)
		}
		if err := direct.Exec(ctx, `INSERT INTO capacity_forecasts (forecast_id, computed_at, team_id, work_scope_id, backlog_size, p50_days, throughput_mean, throughput_stddev, org_id) VALUES (?,?,?,?,?,?,?,?,?)`,
			"forecast-b", at, "TEAM1", "scope-b", uint32(365), uint16(120), 0.01, 0.1, orgID); err != nil {
			t.Fatalf("seed scope-b forecast: %v", err)
		}
		provider := findProvider(t, providers, contextfabric.FactWorkload)
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			Kind: contextfabric.FactWorkload, Subjects: []contextfabric.SubjectRef{{
				Kind: contextfabric.SubjectTeam, CanonicalID: "team:TEAM1", Label: "TEAM1",
			}},
		})
		if err != nil {
			t.Fatalf("ReadFacts() error = %v", err)
		}
		if len(result.Facts) != 2 {
			t.Fatalf("facts = %#v, want 2 -- one per work_scope_id, neither silently collapsed into the other", result.Facts)
		}
		scopes := map[string]bool{}
		for _, fact := range result.Facts {
			if fact.Fields["work_scope_id"].String != nil {
				scopes[*fact.Fields["work_scope_id"].String] = true
			}
		}
		if !scopes["scope-a"] || !scopes["scope-b"] {
			t.Fatalf("scopes seen = %#v, want both scope-a and scope-b named in the payload", scopes)
		}
	})

	t.Run("F4_investment_breaks_same_day_rerun_ties_with_computed_at", func(t *testing.T) {
		const orgID = "org-f4-inv"
		if err := direct.Exec(ctx, `INSERT INTO investment_metrics_daily (day, team_id, investment_area, project_stream, delivery_units, work_items_completed, prs_merged, churn_loc, cycle_p50_hours, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			date(2026, 8, 12), "TEAM1", "product", "growth", uint32(5), uint32(1), uint32(1), uint64(100), 10.0, ts(2026, 8, 12, 8, 0, 0), orgID); err != nil {
			t.Fatalf("seed stale investment row: %v", err)
		}
		if err := direct.Exec(ctx, `INSERT INTO investment_metrics_daily (day, team_id, investment_area, project_stream, delivery_units, work_items_completed, prs_merged, churn_loc, cycle_p50_hours, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			date(2026, 8, 12), "TEAM1", "product", "growth", uint32(30), uint32(12), uint32(4), uint64(850), 18.5, ts(2026, 8, 12, 20, 0, 0), orgID); err != nil {
			t.Fatalf("seed fresh investment row: %v", err)
		}
		provider := findProvider(t, providers, contextfabric.FactInvestment)
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{{
				Kind: contextfabric.SubjectTeam, CanonicalID: "team:TEAM1", Label: "TEAM1",
			}},
		})
		if err != nil {
			t.Fatalf("ReadFacts() error = %v", err)
		}
		if len(result.Facts) != 1 {
			t.Fatalf("facts = %#v, want 1 (same-day rerun collapses to the freshest row)", result.Facts)
		}
		units := result.Facts[0].Fields["delivery_units"].Integer
		if units == nil || *units != 30 {
			t.Fatalf("fields = %#v, want the fresh row's delivery_units=30", result.Facts[0].Fields)
		}
	})

	t.Run("F4_readiness_partitions_by_provider_never_collapses_across_providers", func(t *testing.T) {
		const orgID = "org-f4-ready"
		if err := direct.Exec(ctx, `INSERT INTO estimate_coverage_metrics_daily (day, provider, work_scope_id, team_id, estimated_count, unestimated_count, backlog_size, ratio, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?,?)`,
			date(2026, 8, 12), "gitlab", "scope-1", "TEAM1", uint32(18), uint32(2), uint32(20), 0.9, ts(2026, 8, 12, 8, 0, 0), orgID); err != nil {
			t.Fatalf("seed gitlab readiness row: %v", err)
		}
		if err := direct.Exec(ctx, `INSERT INTO estimate_coverage_metrics_daily (day, provider, work_scope_id, team_id, estimated_count, unestimated_count, backlog_size, ratio, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?,?)`,
			date(2026, 8, 12), "linear", "scope-1", "TEAM1", uint32(3), uint32(9), uint32(12), 0.25, ts(2026, 8, 12, 8, 0, 0), orgID); err != nil {
			t.Fatalf("seed linear readiness row: %v", err)
		}
		provider := findProvider(t, providers, contextfabric.FactReadiness)
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			Kind: contextfabric.FactReadiness, Subjects: []contextfabric.SubjectRef{{
				Kind: contextfabric.SubjectTeam, CanonicalID: "team:TEAM1", Label: "TEAM1",
			}},
		})
		if err != nil {
			t.Fatalf("ReadFacts() error = %v", err)
		}
		if len(result.Facts) != 2 {
			t.Fatalf("facts = %#v, want 2 -- one per provider sharing work_scope_id=scope-1, neither collapsed into the other", result.Facts)
		}
		providersSeen := map[string]float64{}
		for _, fact := range result.Facts {
			if fact.Fields["provider"].String != nil && fact.Fields["estimate_coverage_ratio"].Number != nil {
				providersSeen[*fact.Fields["provider"].String] = *fact.Fields["estimate_coverage_ratio"].Number
			}
		}
		if providersSeen["gitlab"] != 0.9 || providersSeen["linear"] != 0.25 {
			t.Fatalf("providersSeen = %#v, want gitlab=0.9 and linear=0.25 as distinct facts", providersSeen)
		}
	})
}

func date(year int, month time.Month, day int) time.Time {
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

func ts(year int, month time.Month, day, hour, min, sec int) time.Time {
	return time.Date(year, month, day, hour, min, sec, 0, time.UTC)
}

func padHex(i int) string {
	s := strconv.FormatInt(int64(i), 16)
	for len(s) < 12 {
		s = "0" + s
	}
	return s
}
