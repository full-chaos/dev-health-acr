package devhealthfacts_test

// Ownership is synced state: its valid_from is the sync stamp, not the start of
// ownership. A range window that ended before that stamp reads the same owned
// repositories as a trailing window. EXECUTED against a real ClickHouse.

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestPastRangeWindowReadsOwnershipAsSyncedAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	createCHAOS5930Tables(t, ctx, direct)
	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactInvestment)
	const orgID = "org-past-window"
	end := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	synced := end.Add(-10 * day)

	exec := func(statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	exec(`INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`, repoUUID("repo-p"), orgID, "acme/repo-p", "github", synced)
	exec(`INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		orgID, "github", "team-past", repoUUID("repo-p"), "acme/repo-p", "exact", "native", uint8(1), uint16(100), int32(0), synced, nil, synced)
	// A second team owns a repository that has no unit at all.
	exec(`INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`, repoUUID("repo-q"), orgID, "acme/repo-q", "github", synced)
	exec(`INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		orgID, "github", "team-quiet", repoUUID("repo-q"), "acme/repo-q", "exact", "native", uint8(1), uint16(100), int32(0), synced, nil, synced)
	seed := func(id string, from, to time.Time, effort float64) {
		t.Helper()
		exec(`INSERT INTO work_unit_investments (work_unit_id, from_ts, to_ts, effort_value, theme_distribution_json, subcategory_distribution_json, structural_evidence_json, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?)`,
			id, from, to, effort, map[string]float64{"feature_delivery": 1.0}, map[string]float64{},
			fmt.Sprintf(`{"issues":[],"prs":["%s#pr%d"]}`, repoUUID("repo-p"), prNumber(id)), to, orgID)
	}
	// Windows A = [end-180d, end-120d), B = [end-120d, end-60d), C = [end-60d, end).
	seed("in-a", end.Add(-170*day), end.Add(-160*day), 3)
	seed("in-b", end.Add(-100*day), end.Add(-95*day), 5)
	seed("in-c", end.Add(-20*day), end.Add(-15*day), 11)

	read := func(team string, start, stop time.Time) contextfabric.FactProviderResult {
		t.Helper()
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: &start, End: &stop},
			Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{teamSubject(team)},
		})
		if err != nil {
			t.Fatalf("ReadFacts: %v", err)
		}
		return result
	}
	total := func(result contextfabric.FactProviderResult) float64 {
		t.Helper()
		if len(result.Facts) != 1 {
			t.Fatalf("facts = %d, want 1 (reason %q)", len(result.Facts), result.Reason)
		}
		sum := 0.0
		for _, row := range result.Facts[0].Fields["theme_breakdown"].Table.Rows {
			sum += *row.Fields["weighted_effort"].Number
		}
		return sum
	}
	for i, want := range []float64{3, 5, 11} {
		start := end.Add(-time.Duration(180-60*i) * day)
		if got := total(read("team-past", start, start.Add(60*day))); math.Abs(got-want) > 1e-9 {
			t.Fatalf("window %d total = %v, want %v", i, got, want)
		}
	}
	if got := total(read("team-past", end.Add(-180*day), end)); math.Abs(got-19) > 1e-9 {
		t.Fatalf("trailing-shaped 180d total = %v, want 19", got)
	}

	// Empty windows name their cause.
	owned := read("team-quiet", end.Add(-60*day), end)
	if len(owned.Facts) != 0 || !strings.Contains(owned.Reason, "investment_team_no_unit_in_window") {
		t.Fatalf("owned repo without unit: facts=%d reason=%q, want investment_team_no_unit_in_window", len(owned.Facts), owned.Reason)
	}
	none := read("team-nobody", end.Add(-60*day), end)
	if len(none.Facts) != 0 || !strings.Contains(none.Reason, "investment_team_no_owned_repository") {
		t.Fatalf("team without ownership: facts=%d reason=%q, want investment_team_no_owned_repository", len(none.Facts), none.Reason)
	}
}
