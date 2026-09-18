package devhealthfacts_test

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-5952: health reads compounding_risk_daily's own worker emits
// severity='unknown' on any day review_latency_p90h is null (zero
// first-reviewed PRs that day), so the literal latest row can read
// "unknown" while a real band was known a few days earlier. These tests
// exercise the Go-side field emission the freshness classification drives
// -- readScope, the project risk_breakdown rows, and the project severity
// aggregate's everKnownRowCount split -- given the is_known/is_fresh flags
// a real query computes (health_project_rollups_integration_test.go and
// chaos4363_project_rollups_integration_test.go prove the SQL itself
// computes those flags correctly off a real day column and a real server
// clock; the fake client here replays canned rows, so it cannot prove
// that).

// TestHealthProviderRepoScopeServesKnownBandInsideWindow pins the core
// contract: the picked row is KNOWN and inside the freshness window, so its
// own band is served, severity_as_of names its day, and
// severity_freshness_window_days discloses the policy constant.
func TestHealthProviderRepoScopeServesKnownBandInsideWindow(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM compounding_risk_daily", rows: [][]any{
		healthRowFreshness("repo-1", "elevated", "2026-09-15", true, true),
	}}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactHealth)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactHealth, Subjects: []contextfabric.SubjectRef{repoSubject("repo-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	fact := result.Facts[0]
	if got := fact.Fields["severity"].String; got == nil || *got != "elevated" {
		t.Fatalf("severity = %#v, want elevated", fact.Fields["severity"])
	}
	if got := fact.Fields["severity_as_of"].String; got == nil || *got != "2026-09-15" {
		t.Fatalf("severity_as_of = %#v, want 2026-09-15", fact.Fields["severity_as_of"])
	}
	if got := fact.Fields["severity_freshness_window_days"].Integer; got == nil || *got != 14 {
		t.Fatalf("severity_freshness_window_days = %#v, want 14", fact.Fields["severity_freshness_window_days"])
	}
	if _, ok := fact.Fields["severity_unavailable_reason"]; ok {
		t.Fatalf("fields = %#v, want severity_unavailable_reason absent when a fresh known band is served", fact.Fields)
	}
}

// TestHealthProviderRepoScopeKnownButStaleServesUnknownWithReason pins the
// case a literal-latest-row read would have gotten wrong: the picked row
// DOES carry a real band, but it falls outside the freshness window, so it
// is served exactly like an unknown reading -- "unknown", with a reason
// that says WHY (distinct from never having a known band at all), and no
// severity_as_of (there is no current band to date).
func TestHealthProviderRepoScopeKnownButStaleServesUnknownWithReason(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM compounding_risk_daily", rows: [][]any{
		healthRowFreshness("repo-1", "high", "2026-08-01", true, false),
	}}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactHealth)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactHealth, Subjects: []contextfabric.SubjectRef{repoSubject("repo-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	fact := result.Facts[0]
	if got := fact.Fields["severity"].String; got == nil || *got != "unknown" {
		t.Fatalf("severity = %#v, want unknown (the known band is stale, never served as though current)", fact.Fields["severity"])
	}
	if got := fact.Fields["severity_unavailable_reason"].String; got == nil || *got != "known_severity_stale_beyond_freshness_window" {
		t.Fatalf("severity_unavailable_reason = %#v, want known_severity_stale_beyond_freshness_window", fact.Fields["severity_unavailable_reason"])
	}
	if _, ok := fact.Fields["severity_as_of"]; ok {
		t.Fatalf("fields = %#v, want severity_as_of absent -- there is no current band to date", fact.Fields)
	}
}

// TestHealthProviderRepoScopeNeverKnownServesUnknownWithExistingReason pins
// the OTHER unavailable reason: a scope that has never recorded a real
// band at all (not merely a stale one) reports the pre-existing
// no_known_severity_breakdown_rows reason, never the new stale one.
func TestHealthProviderRepoScopeNeverKnownServesUnknownWithExistingReason(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM compounding_risk_daily", rows: [][]any{
		healthRowFreshness("repo-1", "unknown", "2026-09-17", false, false),
	}}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactHealth)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactHealth, Subjects: []contextfabric.SubjectRef{repoSubject("repo-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	fact := result.Facts[0]
	if got := fact.Fields["severity"].String; got == nil || *got != "unknown" {
		t.Fatalf("severity = %#v, want unknown", fact.Fields["severity"])
	}
	if got := fact.Fields["severity_unavailable_reason"].String; got == nil || *got != "no_known_severity_breakdown_rows" {
		t.Fatalf("severity_unavailable_reason = %#v, want no_known_severity_breakdown_rows (never known, not merely stale)", fact.Fields["severity_unavailable_reason"])
	}
}

// TestHealthProviderProjectRollupMixedFreshAndStaleWorstFreshBandWins is
// the project-scope mixed case: the team's own band is FRESH and known,
// the repo's own band is KNOWN but STALE -- the promoted severity must be
// the worst band among the FRESH rows only, never let a stale reading
// participate in (or win) the max.
func TestHealthProviderProjectRollupMixedFreshAndStaleWorstFreshBandWins(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: healthProjectRollupMatch, rows: [][]any{
			healthProjectRollupRowFreshness("linear", "proj-1", "team", "team-1", "Team One", "elevated", 0.55, "2026-09-16", true, true),
			healthProjectRollupRowFreshness("linear", "proj-1", "repo", "repo-1", "full.chaos/svc", "high", 0.90, "2026-07-01", true, false),
		}},
		// The aggregate agrees: repo-1's "high" is known-but-stale (counted
		// in everKnown, never in known), so team-1's fresh "elevated" is the
		// only fresh reading and wins by default -- never "high".
		{match: healthSeverityMaxMatch, rows: [][]any{
			healthSeverityMaxRowFreshness("linear", "proj-1", 1, 2, 2, "team", "team-1", "elevated", "2026-09-16"),
		}},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactHealth)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactHealth, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	fact := result.Facts[0]
	if got := fact.Fields["severity"].String; got == nil || *got != "elevated" {
		t.Fatalf("severity = %#v, want elevated (the only FRESH band -- the stale 'high' repo row must not win)", fact.Fields["severity"])
	}
	if got := fact.Fields["severity_as_of"].String; got == nil || *got != "2026-09-16" {
		t.Fatalf("severity_as_of = %#v, want 2026-09-16", fact.Fields["severity_as_of"])
	}
	rows := fact.Fields["risk_breakdown"].Rows
	if len(rows) != 2 {
		t.Fatalf("risk_breakdown rows = %#v, want 2", rows)
	}
	var teamRow, repoRow map[string]contextfabric.FactValue
	for _, row := range rows {
		if scope := row.Fields["scope"].String; scope != nil && *scope == "team" {
			teamRow = row.Fields
		}
		if scope := row.Fields["scope"].String; scope != nil && *scope == "repo" {
			repoRow = row.Fields
		}
	}
	if teamRow == nil || repoRow == nil {
		t.Fatalf("rows = %#v, want one team row and one repo row", rows)
	}
	if got := teamRow["severity"].String; got == nil || *got != "elevated" {
		t.Fatalf("team row severity = %#v, want elevated", teamRow["severity"])
	}
	if got := repoRow["severity"].String; got == nil || *got != "unknown" {
		t.Fatalf("repo row severity = %#v, want unknown (its own known band is stale)", repoRow["severity"])
	}
	if got := repoRow["severity_unavailable_reason"].String; got == nil || *got != "known_severity_stale_beyond_freshness_window" {
		t.Fatalf("repo row severity_unavailable_reason = %#v, want known_severity_stale_beyond_freshness_window", repoRow["severity_unavailable_reason"])
	}
}

// TestHealthProviderProjectRollupAllKnownBandsStaleDisclosesStaleReason is
// the project-level split queryProjectHealthSeverityMax's everKnownRowCount
// exists for: every reachable row DID carry a real band at some point, but
// none is inside the window -- distinct from having never carried one, so
// the reason must be the NEW stale value, never the pre-existing
// never-known one.
func TestHealthProviderProjectRollupAllKnownBandsStaleDisclosesStaleReason(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: healthProjectRollupMatch, rows: [][]any{
			healthProjectRollupRowFreshness("linear", "proj-1", "team", "team-1", "Team One", "low", 0.10, "2026-07-01", true, false),
		}},
		{match: healthSeverityMaxMatch, rows: [][]any{
			healthSeverityMaxRowFreshness("linear", "proj-1", 0, 1, 1, "", "", "", ""),
		}},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactHealth)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactHealth, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	fact := result.Facts[0]
	if _, ok := fact.Fields["severity"]; ok {
		t.Fatalf("fields = %#v, want severity absent", fact.Fields)
	}
	if got := fact.Fields["severity_unavailable_reason"].String; got == nil || *got != "known_severity_stale_beyond_freshness_window" {
		t.Fatalf("severity_unavailable_reason = %#v, want known_severity_stale_beyond_freshness_window (a real band existed, just not within the window)", fact.Fields["severity_unavailable_reason"])
	}
	if got := fact.Fields["severity_freshness_window_days"].Integer; got == nil || *got != 14 {
		t.Fatalf("severity_freshness_window_days = %#v, want 14", fact.Fields["severity_freshness_window_days"])
	}
}

// TestHealthProviderProjectRollupNeverKnownDisclosesExistingReason is the
// AllUnknown test's freshness-era companion: everKnownRowCount is ALSO
// zero (never known, not merely stale), so the reason must stay the
// pre-existing no_known_severity_breakdown_rows value.
func TestHealthProviderProjectRollupNeverKnownDisclosesExistingReason(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: healthProjectRollupMatch, rows: [][]any{
			healthProjectRollupRowFreshness("linear", "proj-1", "team", "team-1", "Team One", "unknown", 0.10, "2026-09-17", false, false),
		}},
		{match: healthSeverityMaxMatch, rows: [][]any{
			healthSeverityMaxRowFreshness("linear", "proj-1", 0, 0, 1, "", "", "", ""),
		}},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactHealth)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactHealth, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	fact := result.Facts[0]
	if got := fact.Fields["severity_unavailable_reason"].String; got == nil || *got != "no_known_severity_breakdown_rows" {
		t.Fatalf("severity_unavailable_reason = %#v, want no_known_severity_breakdown_rows", fact.Fields["severity_unavailable_reason"])
	}
}
