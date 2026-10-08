package devhealthfacts_test

// The work units behind an investment allocation, EXECUTED against a real
// ClickHouse. Invariants: over every page of the listing, the weighted sum of
// the unit theme probabilities equals the mix the same read serves; a page cut
// is disclosed and the cursor continues it with every row exactly once; a unit
// reference that names no repository is counted, never dropped.

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestInvestmentUnitsMatchTheMixAndPageAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	createCHAOS5930Tables(t, ctx, direct)
	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactInvestment)
	at := ts(2026, 9, 18, 0, 0, 0)
	const orgID = "org-investment-units"

	for _, label := range []string{"repo-a", "repo-b", "repo-c"} {
		if err := direct.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`,
			repoUUID(label), orgID, "acme/"+label, "github", at); err != nil {
			t.Fatalf("seed repo: %v", err)
		}
	}
	for _, label := range []string{"repo-a", "repo-b"} {
		if err := direct.Exec(ctx, `INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			orgID, "github", "team-1", repoUUID(label), "acme/"+label, "exact", "native", uint8(1), uint16(100), int32(0), at, nil, at); err != nil {
			t.Fatalf("seed ownership: %v", err)
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
	seed("wu5", map[string]float64{"feature_delivery": 0.25, "quality": 0.75}, 4, fmt.Sprintf(`{"issues":["ghpr:acme/missing#7"],"prs":["%s#pr9"]}`, a))

	team := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:team-1", Label: "team-1"}
	read := func(req *contextfabric.InvestmentUnitsRequest) contextfabric.FactProviderResult {
		t.Helper()
		readCtx := ctx
		if req != nil {
			readCtx = contextfabric.WithInvestmentUnits(ctx, *req)
		}
		result, err := provider.ReadFacts(readCtx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{team},
		})
		if err != nil {
			t.Fatalf("ReadFacts: %v", err)
		}
		return result
	}
	kindOf := func(f contextfabric.CanonicalFact) string {
		if v, ok := f.Fields["unit_kind"]; ok && v.String != nil {
			return *v.String
		}
		return ""
	}

	plain := read(nil)
	if len(plain.Facts) != 1 || kindOf(plain.Facts[0]) != "" {
		t.Fatalf("a read without units must serve the mix fact only, got %d facts", len(plain.Facts))
	}
	mix := map[string]float64{}
	for _, row := range plain.Facts[0].Fields["theme_breakdown"].Table.Rows {
		mix[*row.Fields["theme"].String] = *row.Fields["weighted_effort"].Number
	}

	type unit struct{ id, repo string }
	seen := map[unit]bool{}
	weightedTheme := map[string]float64{}
	var shareSum float64
	cursor := (*contextfabric.InvestmentUnitsCursor)(nil)
	pages := 0
	for {
		pages++
		result := read(&contextfabric.InvestmentUnitsRequest{Max: 2, Cursor: cursor})
		var page *contextfabric.CanonicalFact
		rows, mixFacts := 0, 0
		for i := range result.Facts {
			f := result.Facts[i]
			switch kindOf(f) {
			case contextfabric.InvestmentUnitPageKind:
				page = &result.Facts[i]
			case contextfabric.InvestmentUnitKind:
				rows++
				key := unit{*f.Fields["work_unit_id"].String, *f.Fields["repository_id"].String}
				if seen[key] {
					t.Fatalf("unit row %v served twice across pages", key)
				}
				seen[key] = true
				share := *f.Fields["share_in_scope"].Number
				shareSum += share
				for _, theme := range []string{"feature_delivery", "operational", "maintenance", "quality", "risk"} {
					weightedTheme[theme] += share * *f.Fields["unit_theme_"+theme].Number
				}
				if len(f.EvidenceRefIDs) == 0 || !strings.HasPrefix(f.EvidenceRefIDs[0], "acr:v1:repository:") {
					t.Fatalf("unit row %v carries no repository ref: %v", key, f.EvidenceRefIDs)
				}
				if key.id == "wu1" && key.repo == a {
					want := "acr:v1:pull-request:" + a + ":1"
					if len(f.EvidenceRefIDs) != 2 || f.EvidenceRefIDs[1] != want {
						t.Fatalf("wu1@a refs = %v, want the repository ref and %s", f.EvidenceRefIDs, want)
					}
					if math.Abs(share-5) > 1e-9 {
						t.Fatalf("wu1@a share = %v, want 5 (1 of 2 refs of effort 10)", share)
					}
				}
				if key.id == "wu5" {
					if n := *f.Fields["unit_refs_unresolved"].Integer; n != 1 {
						t.Fatalf("wu5 unit_refs_unresolved = %d, want 1", n)
					}
					if got := *f.Fields["unit_unresolved_refs"].String; got != "ghpr:acme/missing#7" {
						t.Fatalf("wu5 unresolved handle = %q", got)
					}
					if math.Abs(share-2) > 1e-9 {
						t.Fatalf("wu5 share = %v, want 2 (1 of 2 refs of effort 4)", share)
					}
				}
			case "":
				mixFacts++
			default:
				t.Fatalf("unexpected fact in a units read: %v", f.Fields)
			}
		}
		if page == nil {
			t.Fatalf("page %d has no work_unit_page fact", pages)
		}
		if mixFacts != 1 {
			t.Fatalf("page %d serves %d mix facts, want the one mix beside the listing", pages, mixFacts)
		}
		next, more := page.Fields["next_cursor"]
		if more != result.Truncated {
			t.Fatalf("page %d: next_cursor present = %v but Truncated = %v", pages, more, result.Truncated)
		}
		if more && !strings.Contains(result.Reason, "units_page_cut") {
			t.Fatalf("page %d is cut but the reason does not say so: %q", pages, result.Reason)
		}
		if rows > 2 {
			t.Fatalf("page %d has %d rows, max_units was 2", pages, rows)
		}
		if !more {
			break
		}
		decoded, err := contextfabric.DecodeInvestmentUnitsCursor(*next.String)
		if err != nil {
			t.Fatalf("next_cursor does not decode: %v", err)
		}
		cursor = &decoded
		if pages > 10 {
			t.Fatal("paging does not terminate")
		}
	}
	if pages < 2 {
		t.Fatalf("pages = %d, want a cut first page", pages)
	}
	wantRows := []unit{{"wu1", a}, {"wu1", b}, {"wu2", a}, {"wu3", b}, {"wu5", a}}
	var gotRows []string
	for k := range seen {
		gotRows = append(gotRows, k.id+"@"+k.repo)
	}
	sort.Strings(gotRows)
	var want []string
	for _, k := range wantRows {
		want = append(want, k.id+"@"+k.repo)
	}
	sort.Strings(want)
	if strings.Join(gotRows, ",") != strings.Join(want, ",") {
		t.Fatalf("unit rows over all pages = %v, want %v (repo-c is owned by nobody)", gotRows, want)
	}
	for theme, effort := range mix {
		if d := math.Abs(weightedTheme[theme] - effort); d > 1e-9 {
			t.Fatalf("sum(share_in_scope * unit_theme_%s) = %v, want the mix weighted_effort %v", theme, weightedTheme[theme], effort)
		}
	}
	var mixTotal float64
	for _, e := range mix {
		mixTotal += e
	}
	if math.Abs(shareSum-mixTotal) > 1e-9 {
		t.Fatalf("sum(share_in_scope) = %v, want the mix total %v", shareSum, mixTotal)
	}
}
