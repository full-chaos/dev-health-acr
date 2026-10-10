package devhealthfacts_test

// The organization's units listing, EXECUTED against a real ClickHouse on the
// organization fixture of investment_organization_integration_test.go: three
// repositories (repo-b shared by two teams), one unit naming a repository that
// does not exist and one unit with no reference at all. The expected numbers are
// worked out by hand from the seeded rows: unit shares wu1 5+5, wu2 8, wu3 6,
// wu4 50 (74 resolved), wu5 10 and wu6 10 (20 unattributed), 94 in all.

import (
	"context"
	"fmt"
	"math"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestOrganizationUnitsPageTiesToTheOrganizationFactAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	createCHAOS5930Tables(t, ctx, direct)
	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactInvestment)
	at := ts(2026, 9, 18, 0, 0, 0)
	const orgID = "org-organization-units"

	for _, label := range []string{"repo-a", "repo-b", "repo-c"} {
		if err := direct.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`,
			repoUUID(label), orgID, "acme/"+label, "github", at); err != nil {
			t.Fatalf("seed repo: %v", err)
		}
	}
	seed := func(id string, themes map[string]float64, effort float64, evidence string) {
		t.Helper()
		if err := direct.Exec(ctx,
			`INSERT INTO work_unit_investments (work_unit_id, from_ts, to_ts, effort_value, theme_distribution_json, subcategory_distribution_json, structural_evidence_json, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?)`,
			id, at, at, effort, themes, map[string]float64{}, evidence, at, orgID); err != nil {
			t.Fatalf("seed wu %s: %v", id, err)
		}
	}
	a, b, c := repoUUID("repo-a"), repoUUID("repo-b"), repoUUID("repo-c")
	seed("wu1", map[string]float64{"feature_delivery": 1.0}, 10, fmt.Sprintf(`{"issues":[],"prs":["%s#pr1","%s#pr2"]}`, a, b))
	seed("wu2", map[string]float64{"risk": 0.5, "quality": 0.5}, 8, fmt.Sprintf(`{"issues":[],"prs":["%s#pr3"]}`, a))
	seed("wu3", map[string]float64{"maintenance": 1.0}, 6, fmt.Sprintf(`{"issues":[],"prs":["%s#pr4"]}`, b))
	seed("wu4", map[string]float64{"operational": 1.0}, 50, fmt.Sprintf(`{"issues":[],"prs":["%s#pr5"]}`, c))
	seed("wu5", map[string]float64{"maintenance": 1.0}, 10, `{"issues":["ghpr:acme/not-synced#7"],"prs":[]}`)
	seed("wu6", map[string]float64{"risk": 1.0}, 10, `{"issues":[],"prs":[]}`)

	read := func(request contextfabric.InvestmentUnitsRequest) contextfabric.FactProviderResult {
		t.Helper()
		result, err := provider.ReadFacts(contextfabric.WithInvestmentUnits(ctx, request), storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactInvestment,
			Subjects: []contextfabric.SubjectRef{organizationSubject(orgID)},
		})
		if err != nil {
			t.Fatalf("ReadFacts: %v", err)
		}
		return result
	}

	type unitKey struct{ unit, repo string }
	shares := map[unitKey]float64{}
	unattributed := map[string]float64{}
	var order []string
	cursor := (*contextfabric.InvestmentUnitsCursor)(nil)
	for page := 0; page < 10; page++ {
		result := read(contextfabric.InvestmentUnitsRequest{Max: 3, Cursor: cursor})
		pages := unitFactsOf(result, contextfabric.InvestmentUnitPageKind)
		if len(pages) != 1 {
			t.Fatalf("page %d: %d page facts, want 1", page, len(pages))
		}
		fields := pages[0].Fields
		if got := *fields["scope_unit_rows"].Integer; got != 7 {
			t.Fatalf("page %d scope_unit_rows = %d, want 7", page, got)
		}
		if got := *fields["scope_unattributed_rows"].Integer; got != 2 {
			t.Fatalf("page %d scope_unattributed_rows = %d, want 2", page, got)
		}
		if got := *fields["scope_unattributed_total"].Number; math.Abs(got-20) > 1e-9 {
			t.Fatalf("page %d scope_unattributed_total = %v, want 20", page, got)
		}
		if got := *fields["scope_share_total"].Number; math.Abs(got-94) > 1e-9 {
			t.Fatalf("page %d scope_share_total = %v, want 94 (resolved 74 + unattributed 20)", page, got)
		}
		for _, row := range unitFactsOf(result, contextfabric.InvestmentUnitKind) {
			id := *row.Fields["work_unit_id"].String
			share := *row.Fields["share_in_scope"].Number
			order = append(order, id)
			if repo, has := row.Fields["repository_id"]; has {
				shares[unitKey{id, *repo.String}] = share
				if basis := *row.Fields["unit_attribution_basis"].String; basis == contextfabric.InvestmentUnitAttributionUnattributed {
					t.Fatalf("a repository row %s carries the unattributed basis", id)
				}
				continue
			}
			if basis := *row.Fields["unit_attribution_basis"].String; basis != contextfabric.InvestmentUnitAttributionUnattributed {
				t.Fatalf("unit %s without a repository has basis %q", id, basis)
			}
			unattributed[id] += share
		}
		next := pages[0].Fields["next_cursor"]
		if next.String == nil {
			break
		}
		decoded, err := contextfabric.DecodeInvestmentUnitsCursor(*next.String)
		if err != nil {
			t.Fatalf("page %d next_cursor: %v", page, err)
		}
		cursor = &decoded
	}

	if len(shares) != 5 || len(unattributed) != 2 {
		t.Fatalf("rows: %d repository rows %v and %d unattributed %v, want 5 and 2", len(shares), shares, len(unattributed), unattributed)
	}
	wantResolved := map[unitKey]float64{
		{"wu1", a}: 5, {"wu1", b}: 5, {"wu2", a}: 8, {"wu3", b}: 6, {"wu4", c}: 50,
	}
	resolvedSum := 0.0
	for key, want := range wantResolved {
		if got, ok := shares[key]; !ok || math.Abs(got-want) > 1e-9 {
			t.Errorf("share of %v = %v (served %v), want %v", key, got, ok, want)
		}
		resolvedSum += shares[key]
	}
	if math.Abs(unattributed["wu5"]-10) > 1e-9 || math.Abs(unattributed["wu6"]-10) > 1e-9 {
		t.Errorf("unattributed shares = %v, want wu5 10 and wu6 10", unattributed)
	}
	// The listing ties to the organization fact: resolved 74 is the mix's total
	// weight and 20 of 94 is its unattributed_effort_share.
	org := read(contextfabric.InvestmentUnitsRequest{Max: 1})
	var mix *contextfabric.CanonicalFact
	for i := range org.Facts {
		if _, ok := org.Facts[i].Fields["unattributed_effort_share"]; ok {
			mix = &org.Facts[i]
		}
	}
	if mix == nil {
		t.Fatalf("the organization fact is missing beside the page: %#v", org.Facts)
	}
	if got, want := *mix.Fields["unattributed_effort_share"].Number, 20.0/94.0; math.Abs(got-want) > 1e-9 {
		t.Errorf("unattributed_effort_share = %v, want %v", got, want)
	}
	if math.Abs(resolvedSum-74) > 1e-9 {
		t.Errorf("resolved rows sum to %v, want 74", resolvedSum)
	}
	// Share descending, then work unit ascending: the page order is the keyset
	// order, unattributed rows (10 each) among the repository rows by share.
	if want := []string{"wu4", "wu5", "wu6", "wu2", "wu3", "wu1", "wu1"}; fmt.Sprint(order) != fmt.Sprint(want) {
		t.Errorf("row order = %v, want %v", order, want)
	}
}
