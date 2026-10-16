package devhealthfacts_test

// The investment mix weighs every work unit by its issue size and names its
// measure and sizing coverage, EXECUTED against a real ClickHouse. The
// fixture holds one unit per way a unit reaches a repository (a direct pull
// request reference, a github issue lookup, a gitlab lookup, the stored
// repository of a unit with no reference, a unit split over two repositories),
// sized and unsized, so the weight, the coverage and the distinct-unit count of
// a team are worked out by hand and no provider is special-cased. Every
// expected number below is hand-computed from the seeded rows.

import (
	"context"
	"fmt"
	"math"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestInvestmentMixIsWeighedByIssueSizeAndNamesItsCoverageAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	createCHAOS5930Tables(t, ctx, direct)
	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactInvestment)
	at := ts(2026, 9, 18, 0, 0, 0)
	const orgID = "org-issue-size"

	repo := func(label, name, providerName string) {
		t.Helper()
		if err := direct.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`,
			repoUUID(label), orgID, name, providerName, at); err != nil {
			t.Fatalf("seed repo: %v", err)
		}
	}
	repo("repo-a", "acme/repo-a", "github")
	repo("repo-g", "grp/repo-g", "gitlab")
	repo("repo-c", "acme/repo-c", "github")
	for _, label := range []string{"repo-a", "repo-g"} {
		if err := direct.Exec(ctx, `INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			orgID, "github", "team-1", repoUUID(label), label, "exact", "native", uint8(1), uint16(100), int32(0), at, nil, at); err != nil {
			t.Fatalf("seed ownership: %v", err)
		}
	}
	a, g, c := repoUUID("repo-a"), repoUUID("repo-g"), repoUUID("repo-c")
	seedUnit := func(id string, themes map[string]float64, storedRepo, evidence string) {
		t.Helper()
		var repoID any
		if storedRepo != "" {
			repoID = repoUUID(storedRepo)
		}
		if err := direct.Exec(ctx,
			`INSERT INTO work_unit_investments (work_unit_id, from_ts, to_ts, repo_id, effort_value, theme_distribution_json, subcategory_distribution_json, structural_evidence_json, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?,?)`,
			id, at, at, repoID, 123456.0, themes, map[string]float64{}, evidence, at, orgID); err != nil {
			t.Fatalf("seed wu %s: %v", id, err)
		}
	}
	seedSize := func(id string, size float64, computedAt int) {
		t.Helper()
		if err := direct.Exec(ctx, `INSERT INTO work_unit_issue_size (org_id, work_unit_id, issue_size, computed_at) VALUES (?,?,?,?)`,
			orgID, id, size, ts(2026, 9, computedAt, 0, 0, 0)); err != nil {
			t.Fatalf("seed size %s: %v", id, err)
		}
	}
	seedUnit("u-direct", map[string]float64{"feature_delivery": 1}, "", fmt.Sprintf(`{"issues":[],"prs":["%s#pr1"]}`, a))
	seedUnit("u-ghlookup", map[string]float64{"operational": 1}, "", `{"issues":["ghpr:acme/repo-a#5"],"prs":[]}`)
	seedUnit("u-gllookup", map[string]float64{"maintenance": 1}, "", `{"issues":["gitlab:grp/repo-g!4"],"prs":[]}`)
	seedUnit("u-fallback", map[string]float64{"risk": 1}, "repo-a", `{"issues":[],"prs":[]}`)
	seedUnit("u-unsized", map[string]float64{"quality": 1}, "", fmt.Sprintf(`{"issues":[],"prs":["%s#pr2"]}`, a))
	seedUnit("u-split", map[string]float64{"risk": 1}, "", fmt.Sprintf(`{"issues":[],"prs":["%s#pr3","%s#pr9"]}`, a, g))
	seedUnit("u-other", map[string]float64{"maintenance": 1}, "", fmt.Sprintf(`{"issues":[],"prs":["%s#pr1"]}`, c))
	// u-direct has an older size that must lose to the newer one; u-unsized
	// has a stored size of 0, which means unsized.
	seedSize("u-direct", 99, 10)
	seedSize("u-direct", 2, 17)
	seedSize("u-ghlookup", 3, 17)
	seedSize("u-gllookup", 5, 17)
	seedSize("u-fallback", 7, 17)
	seedSize("u-unsized", 0, 17)
	seedSize("u-split", 4, 17)
	seedSize("u-other", 10, 17)

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
	near := func(what string, got, want float64) {
		t.Helper()
		if math.Abs(got-want) > 1e-9 {
			t.Errorf("%s = %v, want %v", what, got, want)
		}
	}
	weighted := func(f contextfabric.CanonicalFact) map[string]float64 {
		out := map[string]float64{}
		for _, row := range f.Fields["theme_breakdown"].Table.Rows {
			out[*row.Fields["theme"].String] = *row.Fields["weighted_effort"].Number
		}
		return out
	}
	check := func(name string, fact contextfabric.CanonicalFact, wantWeights map[string]float64, denominator float64, units, sized int64) {
		t.Helper()
		if got := *fact.Fields["measure"].String; got != "ISSUE_SIZE" {
			t.Errorf("%s: measure = %q", name, got)
		}
		near(name+" denominator", *fact.Fields["denominator"].Number, denominator)
		got := weighted(fact)
		for theme, w := range wantWeights {
			near(fmt.Sprintf("%s %s weighted", name, theme), got[theme], w)
			near(fmt.Sprintf("%s theme share %s", name, theme), *fact.Fields["theme_"+theme].Number, w/denominator)
		}
		near(name+" sizing_coverage", *fact.Fields["sizing_coverage"].Number, float64(sized)/float64(units))
		if got := *fact.Fields["unsized_unit_count"].Integer; got != units-sized {
			t.Errorf("%s: unsized_unit_count = %d, want %d", name, got, units-sized)
		}
		if note, has := fact.Fields["sizing_note"]; has {
			t.Errorf("%s: %d of %d units are sized, yet sizing_note = %q", name, sized, units, *note.String)
		}
	}

	// repo-a: direct 2 (feature), github lookup 3 (operational), fallback 7 +
	// half of the split unit 4 = 2 (risk 9), unsized 1 (quality). 15; 5 units,
	// 4 sized (the unsized one counts 1 and is not sized).
	repoA := read(repositorySubject("repo-a"))
	check("repo-a", repoA, map[string]float64{"feature_delivery": 2, "operational": 3, "risk": 9, "quality": 1}, 15, 5, 4)
	if got := *repoA.Fields["work_unit_count"].Integer; got != 5 {
		t.Errorf("repo-a work_unit_count = %d, want 5", got)
	}
	// repo-g: gitlab lookup 5 (maintenance) + the other half of the split, 2.
	check("repo-g", read(repositorySubject("repo-g")), map[string]float64{"maintenance": 5, "risk": 2}, 7, 2, 2)
	// team-1 owns repo-a and repo-g: the split unit reaches both and counts ONCE
	// in the coverage: 6 distinct units, 5 sized; the weights are the sum of
	// the two repositories (22).
	check("team-1", read(teamSubject("team-1")),
		map[string]float64{"feature_delivery": 2, "operational": 3, "maintenance": 5, "risk": 11, "quality": 1}, 22, 6, 5)
	// The organization adds repo-c's sized unit: 7 units, 6 sized, 32.
	check("organization", read(organizationSubject(orgID)),
		map[string]float64{"feature_delivery": 2, "operational": 3, "maintenance": 15, "risk": 11, "quality": 1}, 32, 7, 6)

	// The listing weighs the same way: each row's share is its PR-ref share of
	// the unit's size, and the page names the coverage of its whole scope.
	result, err := provider.ReadFacts(contextfabric.WithInvestmentUnits(ctx, contextfabric.InvestmentUnitsRequest{Max: 50}),
		storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{teamSubject("team-1")},
		})
	if err != nil {
		t.Fatalf("units: %v", err)
	}
	sizes := map[string]float64{}
	var page *contextfabric.CanonicalFact
	shareTotal := 0.0
	for i := range result.Facts {
		f := result.Facts[i]
		switch kind := f.Fields["unit_kind"]; {
		case kind.String == nil:
		case *kind.String == contextfabric.InvestmentUnitPageKind:
			page = &result.Facts[i]
		case *kind.String == contextfabric.InvestmentUnitKind:
			sizes[*f.Fields["work_unit_id"].String] = *f.Fields["unit_issue_size"].Number
			shareTotal += *f.Fields["share_in_scope"].Number
		}
	}
	if page == nil {
		t.Fatalf("no page fact in %d facts", len(result.Facts))
	}
	for id, want := range map[string]float64{"u-direct": 2, "u-ghlookup": 3, "u-gllookup": 5, "u-fallback": 7, "u-unsized": 0, "u-split": 4} {
		if got, ok := sizes[id]; !ok || got != want {
			t.Errorf("unit %s: unit_issue_size = %v (served %v), want %v", id, got, ok, want)
		}
	}
	near("units share total", shareTotal, 22)
	near("page denominator", *page.Fields["denominator"].Number, 22)
	near("page sizing_coverage", *page.Fields["sizing_coverage"].Number, 5.0/6.0)
	if got := *page.Fields["unsized_unit_count"].Integer; got != 1 {
		t.Errorf("page unsized_unit_count = %d, want 1", got)
	}
}

// With fewer than half of the units sized the mix says so, in the words ops
// uses; a mix with no sized unit still counts every unit 1.
func TestInvestmentMixSaysWhenItIsMostlyUnsizedAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	createCHAOS5930Tables(t, ctx, direct)
	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactInvestment)
	at := ts(2026, 9, 18, 0, 0, 0)
	const orgID = "org-issue-size-note"
	if err := direct.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`, repoUUID("repo-a"), orgID, "acme/repo-a", "github", at); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	for i := 1; i <= 4; i++ {
		if err := direct.Exec(ctx,
			`INSERT INTO work_unit_investments (work_unit_id, from_ts, to_ts, effort_value, theme_distribution_json, subcategory_distribution_json, structural_evidence_json, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?)`,
			fmt.Sprintf("n-%d", i), at, at, 1.0, map[string]float64{"feature_delivery": 1}, map[string]float64{}, fmt.Sprintf(`{"issues":[],"prs":["%s#pr%d"]}`, repoUUID("repo-a"), i), at, orgID); err != nil {
			t.Fatalf("seed wu: %v", err)
		}
	}
	size := func(id string, issueSize float64) {
		t.Helper()
		if err := direct.Exec(ctx, `INSERT INTO work_unit_issue_size (org_id, work_unit_id, issue_size, computed_at) VALUES (?,?,?,?)`, orgID, id, issueSize, at); err != nil {
			t.Fatalf("seed size: %v", err)
		}
	}
	size("n-1", 5)
	read := func() contextfabric.CanonicalFact {
		t.Helper()
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{repositorySubject("repo-a")},
		})
		if err != nil || len(result.Facts) != 1 {
			t.Fatalf("err=%v facts=%d", err, len(result.Facts))
		}
		return result.Facts[0]
	}
	fact := read()
	if got, want := *fact.Fields["sizing_note"].String, "Mostly unsized: 1 of 4 work units have an issue size. The other 3 have no size, so each counts as 1 work unit."; got != want {
		t.Errorf("sizing_note = %q, want %q", got, want)
	}
	if got := *fact.Fields["sizing_coverage"].Number; got != 0.25 {
		t.Errorf("sizing_coverage = %v, want 0.25", got)
	}
	// 5 + 1 + 1 + 1 over the one theme: the denominator is the weights' sum.
	if got := *fact.Fields["denominator"].Number; got != 8 {
		t.Errorf("denominator = %v, want 8", got)
	}
	// A second sized unit makes exactly half: no note.
	size("n-2", 1)
	if note, has := read().Fields["sizing_note"]; has {
		t.Errorf("2 of 4 units sized, yet sizing_note = %q", *note.String)
	}
}
