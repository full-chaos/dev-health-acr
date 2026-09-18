package devhealthfacts_test

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestCHAOS5952HealthFreshnessWindowAgainstRealClickHouse proves the SQL
// itself -- freshnessIsKnownSQL/freshnessIsFreshSQL (health.go) -- computes
// the freshness classification correctly off a real day column and a real
// server clock, which chaos5952_health_freshness_test.go's fakeClient
// sweep cannot: a fake client replays canned rows verbatim, it never
// evaluates the row_number() preference-for-known-rows ordering or the
// dateDiff-equivalent window arithmetic.
func TestCHAOS5952HealthFreshnessWindowAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	createCHAOS4363Tables(t, ctx, direct)
	providers := devhealthfacts.NewProviders(query)

	seedRisk := func(orgID, scope, scopeID, severity string, risk float64, day, computedAt time.Time) {
		t.Helper()
		if err := direct.Exec(ctx, `INSERT INTO compounding_risk_daily (org_id, day, scope, scope_id, compounding_risk, severity, computed_at) VALUES (?,?,?,?,?,?,?)`,
			orgID, day, scope, scopeID, risk, severity, computedAt); err != nil {
			t.Fatalf("seed %s-scope compounding_risk_daily row: %v", scope, err)
		}
	}
	readHealth := func(orgID string, subject contextfabric.SubjectRef, query contextfabric.FactQuery) *contextfabric.CanonicalFact {
		t.Helper()
		if query.Kind == "" {
			query.Kind = contextfabric.FactHealth
		}
		if query.Time.Axis == "" {
			query.Time = contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}
		}
		query.Subjects = []contextfabric.SubjectRef{subject}
		provider := findProvider(t, providers, contextfabric.FactHealth)
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, query)
		if err != nil {
			t.Fatalf("ReadFacts: %v", err)
		}
		if len(result.Facts) == 0 {
			return nil
		}
		return &result.Facts[0]
	}

	// Boundary: a known band exactly 14 days old is still FRESH (the
	// window is inclusive of its own boundary day).
	t.Run("repo_scope_known_band_exactly_14_days_old_is_fresh", func(t *testing.T) {
		const orgID = "org-5952-boundary-fresh"
		repoID := "10000000-0000-0000-0000-000000000001"
		day := recentHealthDay(14)
		seedRisk(orgID, "repo", repoID, "elevated", 0.40, day, day.Add(6*time.Hour))
		fact := readHealth(orgID, repoSubject(repoID), contextfabric.FactQuery{})
		if fact == nil {
			t.Fatal("facts = none, want a served fact")
		}
		if got := fact.Fields["severity"].String; got == nil || *got != "elevated" {
			t.Fatalf("severity = %#v, want elevated (exactly 14 days old is still inside the window)", fact.Fields["severity"])
		}
		if _, ok := fact.Fields["severity_unavailable_reason"]; ok {
			t.Fatalf("fields = %#v, want severity_unavailable_reason absent", fact.Fields)
		}
	})

	// Boundary: 15 days old is STALE -- one day past the same boundary.
	t.Run("repo_scope_known_band_15_days_old_is_stale", func(t *testing.T) {
		const orgID = "org-5952-boundary-stale"
		repoID := "10000000-0000-0000-0000-000000000002"
		day := recentHealthDay(15)
		seedRisk(orgID, "repo", repoID, "elevated", 0.40, day, day.Add(6*time.Hour))
		fact := readHealth(orgID, repoSubject(repoID), contextfabric.FactQuery{})
		if fact == nil {
			t.Fatal("facts = none, want a served fact")
		}
		if got := fact.Fields["severity"].String; got == nil || *got != "unknown" {
			t.Fatalf("severity = %#v, want unknown (15 days old is one day past the window)", fact.Fields["severity"])
		}
		if got := fact.Fields["severity_unavailable_reason"].String; got == nil || *got != "known_severity_stale_beyond_freshness_window" {
			t.Fatalf("severity_unavailable_reason = %#v, want known_severity_stale_beyond_freshness_window", fact.Fields["severity_unavailable_reason"])
		}
	})

	// The literal-latest-row defect this ticket exists to fix: today's row
	// is unknown (no first-reviewed PRs today), but a real band was known 3
	// days ago -- the served fact must carry THAT band, not "unknown".
	t.Run("repo_scope_latest_day_unknown_known_band_three_days_earlier_wins", func(t *testing.T) {
		const orgID = "org-5952-recent-known"
		repoID := "10000000-0000-0000-0000-000000000003"
		known := recentHealthDay(3)
		latest := recentHealthDay(0)
		seedRisk(orgID, "repo", repoID, "high", 0.70, known, known.Add(6*time.Hour))
		seedRisk(orgID, "repo", repoID, "unknown", 0, latest, latest.Add(6*time.Hour))
		fact := readHealth(orgID, repoSubject(repoID), contextfabric.FactQuery{})
		if fact == nil {
			t.Fatal("facts = none, want a served fact")
		}
		if got := fact.Fields["severity"].String; got == nil || *got != "high" {
			t.Fatalf("severity = %#v, want high (the known band 3 days earlier, not the literal latest unknown row)", fact.Fields["severity"])
		}
		if got := fact.Fields["severity_as_of"].String; got == nil || *got != known.Format("2006-01-02") {
			t.Fatalf("severity_as_of = %#v, want %s", fact.Fields["severity_as_of"], known.Format("2006-01-02"))
		}
	})

	// Every reachable row is unknown -- served as unknown with the
	// pre-existing (never-known) reason.
	t.Run("repo_scope_all_rows_unknown", func(t *testing.T) {
		const orgID = "org-5952-never-known"
		repoID := "10000000-0000-0000-0000-000000000004"
		day := recentHealthDay(1)
		seedRisk(orgID, "repo", repoID, "unknown", 0, day, day.Add(6*time.Hour))
		fact := readHealth(orgID, repoSubject(repoID), contextfabric.FactQuery{})
		if fact == nil {
			t.Fatal("facts = none, want a served fact")
		}
		if got := fact.Fields["severity"].String; got == nil || *got != "unknown" {
			t.Fatalf("severity = %#v, want unknown", fact.Fields["severity"])
		}
		if got := fact.Fields["severity_unavailable_reason"].String; got == nil || *got != "no_known_severity_breakdown_rows" {
			t.Fatalf("severity_unavailable_reason = %#v, want no_known_severity_breakdown_rows", fact.Fields["severity_unavailable_reason"])
		}
	})

	// A row AFTER the as-of bound must never leak into a bounded read, even
	// though it would otherwise be "fresh" relative to it -- as-of honesty
	// composed with the freshness window.
	t.Run("repo_scope_known_row_after_as_of_bound_is_excluded", func(t *testing.T) {
		const orgID = "org-5952-as-of-honesty"
		repoID := "10000000-0000-0000-0000-000000000005"
		asOf := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
		seedRisk(orgID, "repo", repoID, "low", 0.10, time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC), time.Date(2026, 8, 5, 6, 0, 0, 0, time.UTC))
		seedRisk(orgID, "repo", repoID, "high", 0.95, time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC), time.Date(2026, 8, 12, 6, 0, 0, 0, time.UTC))
		fact := readHealth(orgID, repoSubject(repoID), contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalValidTime, AsOf: &asOf},
		})
		if fact == nil {
			t.Fatal("facts = none, want a served fact as of 2026-08-10")
		}
		if got := fact.Fields["severity"].String; got == nil || *got != "low" {
			t.Fatalf("severity = %#v, want low (the 2026-08-12 high row is AFTER the as-of instant)", fact.Fields["severity"])
		}
	})

	// The freshness window measures against the REQUEST's own as-of
	// instant, never wall-clock time: a known band 10 days before a
	// historical as-of bound is fresh under THAT bound, regardless of how
	// long ago that historical instant itself was relative to when the
	// suite actually runs.
	t.Run("historical_as_of_window_measures_against_the_requested_instant_not_wall_clock", func(t *testing.T) {
		const orgID = "org-5952-historical-as-of"
		repoID := "10000000-0000-0000-0000-000000000006"
		asOf := time.Date(2026, 1, 20, 0, 0, 0, 0, time.UTC)
		day := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC) // 10 days before asOf
		seedRisk(orgID, "repo", repoID, "elevated", 0.45, day, day.Add(6*time.Hour))
		fact := readHealth(orgID, repoSubject(repoID), contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalValidTime, AsOf: &asOf},
		})
		if fact == nil {
			t.Fatal("facts = none, want a served fact as of 2026-01-20")
		}
		if got := fact.Fields["severity"].String; got == nil || *got != "elevated" {
			t.Fatalf("severity = %#v, want elevated (10 days before the REQUESTED as-of instant, fresh regardless of real wall-clock time)", fact.Fields["severity"])
		}
	})

	// seedMinimalProject wires one project to one team (native ownership)
	// and, when repoID is non-empty, that team to one repo -- the minimal
	// ownership chain readProjectHealth/queryProjectHealthSeverityMax both
	// walk, shared by every project-scope subtest below.
	seedMinimalProject := func(orgID, projectID, projectKey, teamID, repoID string) {
		t.Helper()
		epoch := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		if err := direct.Exec(ctx, `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			projectID, orgID, "linear", projectKey, "Project", uint8(1), "active", "", epoch); err != nil {
			t.Fatalf("seed projects row: %v", err)
		}
		if err := direct.Exec(ctx, `INSERT INTO teams (id, name, description, updated_at, org_id, provider, project_keys, is_active) VALUES (?, ?, NULL, ?, ?, ?, [], ?)`,
			teamID, teamID, epoch, orgID, "linear", uint8(1)); err != nil {
			t.Fatalf("seed teams row: %v", err)
		}
		if err := direct.Exec(ctx, `INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			orgID, "linear", teamID, "irrelevant", projectKey, "native", epoch, nil, epoch); err != nil {
			t.Fatalf("seed team_project_ownership: %v", err)
		}
		if repoID == "" {
			return
		}
		if err := direct.Exec(ctx, `INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			orgID, "github", teamID, repoID, "acme/"+teamID, "exact", "native", uint8(1), uint16(1), int32(1), epoch, nil, epoch); err != nil {
			t.Fatalf("seed team_repo_ownership: %v", err)
		}
	}

	// Project rollup: the team's own band is fresh, the repo's own band is
	// known but stale -- the promoted severity is the worst band among the
	// FRESH rows only, executed against a real server.
	t.Run("project_rollup_mixed_fresh_and_stale_worst_fresh_band_wins", func(t *testing.T) {
		const orgID = "org-5952-project-mixed"
		repoID := "10000000-0000-0000-0000-000000000007"
		seedMinimalProject(orgID, "proj-5952-mixed", "MIXED1", "team-5952-mixed", repoID)
		fresh := recentHealthDay(1)
		stale := recentHealthDay(30)
		seedRisk(orgID, "team", "team-5952-mixed", "elevated", 0.55, fresh, fresh.Add(6*time.Hour))
		seedRisk(orgID, "repo", repoID, "high", 0.90, stale, stale.Add(6*time.Hour))
		fact := readHealth(orgID, projectSubject("linear", "proj-5952-mixed"), contextfabric.FactQuery{})
		if fact == nil {
			t.Fatal("facts = none, want a served project roll-up")
		}
		if got := fact.Fields["severity"].String; got == nil || *got != "elevated" {
			t.Fatalf("severity = %#v, want elevated (the team's fresh band -- the repo's known-but-stale 'high' must not win)", fact.Fields["severity"])
		}
		rows := fact.Fields["risk_breakdown"].Rows
		for _, row := range rows {
			scope := row.Fields["scope"].String
			if scope == nil {
				continue
			}
			if *scope == "repo" {
				if got := row.Fields["severity"].String; got == nil || *got != "unknown" {
					t.Fatalf("repo row severity = %#v, want unknown (its own band is stale)", row.Fields["severity"])
				}
			}
		}
	})

	// The project-scope counterpart of the repo-scope "latest day unknown,
	// known band 3 days earlier wins" case above: a team's own latest-day
	// row is unknown, but a real band was known a few days earlier --
	// row_number()'s known-row preference must pick that earlier row for
	// BOTH the risk_breakdown row AND the promoted project severity, never
	// the literal latest (unknown) day.
	t.Run("project_rollup_latest_day_unknown_known_band_earlier_wins", func(t *testing.T) {
		const orgID = "org-5952-project-recent-known"
		seedMinimalProject(orgID, "proj-5952-recent", "RECENT1", "team-5952-recent", "")
		known := recentHealthDay(3)
		latest := recentHealthDay(0)
		seedRisk(orgID, "team", "team-5952-recent", "high", 0.70, known, known.Add(6*time.Hour))
		seedRisk(orgID, "team", "team-5952-recent", "unknown", 0, latest, latest.Add(6*time.Hour))
		fact := readHealth(orgID, projectSubject("linear", "proj-5952-recent"), contextfabric.FactQuery{})
		if fact == nil {
			t.Fatal("facts = none, want a served project roll-up")
		}
		if got := fact.Fields["severity"].String; got == nil || *got != "high" {
			t.Fatalf("severity = %#v, want high (the known band 3 days earlier, not the literal latest unknown day)", fact.Fields["severity"])
		}
		if got := fact.Fields["severity_as_of"].String; got == nil || *got != known.Format("2006-01-02") {
			t.Fatalf("severity_as_of = %#v, want %s", fact.Fields["severity_as_of"], known.Format("2006-01-02"))
		}
		rows := fact.Fields["risk_breakdown"].Rows
		if len(rows) != 1 {
			t.Fatalf("risk_breakdown rows = %#v, want 1", rows)
		}
		if got := rows[0].Fields["severity"].String; got == nil || *got != "high" {
			t.Fatalf("risk_breakdown row severity = %#v, want high (the same known-preferring pick the aggregate used)", rows[0].Fields["severity"])
		}
	})

	// The everKnownRowCount split, proven against the REAL aggregate SQL
	// rather than a canned row: a project's ONLY reachable row carries a
	// real band, but it is stale -- severity must be absent with the STALE
	// reason, never the never-known one, which only holds if
	// countIf(raw_band > 0) is genuinely computed without the freshness
	// filter countIf(band > 0) applies.
	t.Run("project_rollup_known_band_stale_discloses_stale_reason_not_never_known", func(t *testing.T) {
		const orgID = "org-5952-project-stale-only"
		seedMinimalProject(orgID, "proj-5952-stale", "STALE1", "team-5952-stale", "")
		stale := recentHealthDay(30)
		seedRisk(orgID, "team", "team-5952-stale", "elevated", 0.55, stale, stale.Add(6*time.Hour))
		fact := readHealth(orgID, projectSubject("linear", "proj-5952-stale"), contextfabric.FactQuery{})
		if fact == nil {
			t.Fatal("facts = none, want a served project roll-up (the breakdown row is real)")
		}
		if _, ok := fact.Fields["severity"]; ok {
			t.Fatalf("fields = %#v, want severity absent", fact.Fields)
		}
		if got := fact.Fields["severity_unavailable_reason"].String; got == nil || *got != "known_severity_stale_beyond_freshness_window" {
			t.Fatalf("severity_unavailable_reason = %#v, want known_severity_stale_beyond_freshness_window (a real band existed, only stale -- never never-known)", fact.Fields["severity_unavailable_reason"])
		}
	})
}
