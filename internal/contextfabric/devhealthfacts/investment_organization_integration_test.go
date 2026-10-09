package devhealthfacts_test

// The organization's investment mix counts every repository once, EXECUTED
// against a real ClickHouse. Fixture: three repositories, repo-b owned by two
// teams, one unit whose only reference matches no repository. The expected
// numbers are worked out by hand from the seeded rows below.

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

func TestOrganizationThemeMixCountsASharedRepositoryOnceAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	createCHAOS5930Tables(t, ctx, direct)
	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactInvestment)
	at := ts(2026, 9, 18, 0, 0, 0)
	const orgID = "org-organization-mix"

	for _, label := range []string{"repo-a", "repo-b", "repo-c"} {
		if err := direct.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`,
			repoUUID(label), orgID, "acme/"+label, "github", at); err != nil {
			t.Fatalf("seed repo: %v", err)
		}
	}
	own := func(teamID, label string) {
		t.Helper()
		if err := direct.Exec(ctx, `INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			orgID, "github", teamID, repoUUID(label), "acme/"+label, "exact", "native", uint8(1), uint16(100), int32(0), at, nil, at); err != nil {
			t.Fatalf("seed ownership: %v", err)
		}
	}
	// repo-b is owned by BOTH teams.
	own("team-1", "repo-a")
	own("team-1", "repo-b")
	own("team-2", "repo-b")
	own("team-2", "repo-c")
	seed := func(id string, themes map[string]float64, effort float64, evidence string) {
		t.Helper()
		if err := direct.Exec(ctx,
			`INSERT INTO work_unit_investments (work_unit_id, from_ts, to_ts, effort_value, theme_distribution_json, subcategory_distribution_json, structural_evidence_json, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?)`,
			id, at, at, effort, themes, map[string]float64{}, evidence, at, orgID); err != nil {
			t.Fatalf("seed wu %s: %v", id, err)
		}
	}
	a, b, c := repoUUID("repo-a"), repoUUID("repo-b"), repoUUID("repo-c")
	// wu1 splits 5/5 over repo-a and repo-b; wu2 is all repo-a; wu3 all
	// repo-b; wu4 all repo-c; wu5 names a repository that does not exist.
	seed("wu1", map[string]float64{"feature_delivery": 1.0}, 10, fmt.Sprintf(`{"issues":[],"prs":["%s#pr1","%s#pr2"]}`, a, b))
	seed("wu2", map[string]float64{"risk": 0.5, "quality": 0.5}, 8, fmt.Sprintf(`{"issues":[],"prs":["%s#pr3"]}`, a))
	seed("wu3", map[string]float64{"maintenance": 1.0}, 6, fmt.Sprintf(`{"issues":[],"prs":["%s#pr4"]}`, b))
	seed("wu4", map[string]float64{"operational": 1.0}, 50, fmt.Sprintf(`{"issues":[],"prs":["%s#pr5"]}`, c))
	seed("wu5", map[string]float64{"maintenance": 1.0}, 10, `{"issues":["ghpr:acme/not-synced#7"],"prs":[]}`)
	// wu6 carries no PR reference and no repository: its effort is disclosed,
	// not placed in any theme share.
	seed("wu6", map[string]float64{"risk": 1.0}, 10, `{"issues":[],"prs":[]}`)
	// A superseded row of wu4 (older computed_at, other effort) must not count.
	if err := direct.Exec(ctx,
		`INSERT INTO work_unit_investments (work_unit_id, from_ts, to_ts, effort_value, theme_distribution_json, subcategory_distribution_json, structural_evidence_json, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?)`,
		"wu4", at, at, 999.0, map[string]float64{"risk": 1.0}, map[string]float64{}, fmt.Sprintf(`{"issues":[],"prs":["%s#pr5"]}`, c), ts(2026, 9, 17, 0, 0, 0), orgID); err != nil {
		t.Fatalf("seed stale wu4: %v", err)
	}

	read := func(subject contextfabric.SubjectRef) contextfabric.CanonicalFact {
		t.Helper()
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{subject},
		})
		if err != nil || len(result.Facts) != 1 {
			t.Fatalf("%s: err=%v facts=%d reason=%q", subject.CanonicalID, err, len(result.Facts), result.Reason)
		}
		return result.Facts[0]
	}
	weighted := func(f contextfabric.CanonicalFact) map[string]float64 {
		out := map[string]float64{}
		for _, row := range f.Fields["theme_breakdown"].Table.Rows {
			out[*row.Fields["theme"].String] = *row.Fields["weighted_effort"].Number
		}
		return out
	}

	org := read(organizationSubject(orgID))
	// Each repository once: feature 10, risk 4, quality 4, maintenance 6,
	// operational 50 = 74. The unresolved unit (10) and the no-reference unit (10) are not in the shares.
	want := map[string]float64{"feature_delivery": 10, "risk": 4, "quality": 4, "maintenance": 6, "operational": 50}
	got := weighted(org)
	for theme, w := range want {
		if math.Abs(got[theme]-w) > 1e-9 {
			t.Fatalf("org %s = %v, want %v (all: %v)", theme, got[theme], w, got)
		}
	}
	if share := *org.Fields["theme_operational"].Number; math.Abs(share-50.0/74.0) > 1e-9 {
		t.Fatalf("theme_operational = %v, want %v", share, 50.0/74.0)
	}
	if n := *org.Fields["repositories_in_scope"].Integer; n != 3 {
		t.Fatalf("repositories_in_scope = %d, want 3", n)
	}
	if s := *org.Fields["scope"].String; s != "organization" {
		t.Fatalf("scope = %q", s)
	}
	if u := *org.Fields["unattributed_effort_share"].Number; math.Abs(u-20.0/94.0) > 1e-9 {
		t.Fatalf("unattributed_effort_share = %v, want %v", u, 20.0/94.0)
	}

	// The sum of the two team facts counts repo-b twice: it is NOT the org.
	team1, team2 := weighted(read(teamSubject("team-1"))), weighted(read(teamSubject("team-2")))
	teamSum := 0.0
	for _, theme := range []string{"feature_delivery", "operational", "maintenance", "quality", "risk"} {
		teamSum += team1[theme] + team2[theme]
	}
	if math.Abs(teamSum-85) > 1e-9 {
		t.Fatalf("team facts sum = %v, want 85 (repo-b counted in both teams)", teamSum)
	}
	if math.Abs(teamSum-74) < 1e-9 {
		t.Fatalf("team facts sum equals the org total; the fixture no longer shows the double count")
	}
	if team1["feature_delivery"]+team2["feature_delivery"] == got["feature_delivery"] {
		t.Fatalf("summed team feature effort equals the org's; the shared repository is not double counted in the fixture")
	}
}

// A unit outside the requested window must not enter the organization's
// shares (the stored-span rows ride the same statement), while the window that
// starts before the earliest stored unit still says so.
func TestOrganizationThemeMixKeepsOutOfWindowUnitsOutOfTheSharesAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	createCHAOS5930Tables(t, ctx, direct)
	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactInvestment)
	const orgID = "org-organization-window"
	inWindow, early := ts(2026, 9, 18, 0, 0, 0), ts(2026, 2, 1, 0, 0, 0)
	if err := direct.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`,
		repoUUID("repo-w"), orgID, "acme/repo-w", "github", inWindow); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	seed := func(id string, at time.Time, themes map[string]float64, effort float64, pr string) {
		t.Helper()
		if err := direct.Exec(ctx,
			`INSERT INTO work_unit_investments (work_unit_id, from_ts, to_ts, effort_value, theme_distribution_json, subcategory_distribution_json, structural_evidence_json, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?)`,
			id, at, at, effort, themes, map[string]float64{}, fmt.Sprintf(`{"issues":[],"prs":["%s#%s"]}`, repoUUID("repo-w"), pr), inWindow, orgID); err != nil {
			t.Fatalf("seed wu %s: %v", id, err)
		}
	}
	seed("wu-in", inWindow, map[string]float64{"feature_delivery": 1.0}, 10, "pr1")
	seed("wu-old", early, map[string]float64{"operational": 1.0}, 90, "pr2")
	start, end := ts(2026, 9, 1, 0, 0, 0), ts(2026, 9, 30, 0, 0, 0)
	read := func(start time.Time) contextfabric.FactProviderResult {
		t.Helper()
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: &start, End: &end},
			Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{organizationSubject(orgID)},
		})
		if err != nil || len(result.Facts) != 1 {
			t.Fatalf("read: err=%v facts=%d reason=%q", err, len(result.Facts), result.Reason)
		}
		return result
	}
	inside := read(start)
	if got := *inside.Facts[0].Fields["theme_feature_delivery"].Number; math.Abs(got-1) > 1e-9 {
		t.Fatalf("theme_feature_delivery = %v, want 1 (the early unit is outside the window)", got)
	}
	if strings.Contains(inside.Reason, "investment_window_beyond_stored_history") {
		t.Fatalf("a window inside the stored history carries a span limitation: %q", inside.Reason)
	}
	before := read(ts(2026, 1, 1, 0, 0, 0))
	if !strings.Contains(before.Reason, "investment_window_beyond_stored_history") {
		t.Fatalf("a window that starts before the earliest stored unit names no span: %q", before.Reason)
	}
}
