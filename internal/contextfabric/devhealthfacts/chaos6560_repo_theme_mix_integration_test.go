package devhealthfacts_test

// CHAOS-6560: repository-scope investment mix, EXECUTED against a real
// ClickHouse. The PR-ref-share partition (uuid#prN and ghpr:/gitlab: refs
// deduplicated to one PR, unresolvable refs null-carrying, repo_id
// fallback) is join/ARRAY JOIN SQL a fakeClient cannot certify.

import (
	"context"
	"fmt"
	"math"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestRepositoryThemeMixAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	createCHAOS5930Tables(t, ctx, direct)
	providers := devhealthfacts.NewProviders(query)
	at := ts(2026, 9, 18, 0, 0, 0)
	const orgID = "org-repo-mix"

	for _, label := range []string{"repo-a", "repo-b"} {
		if err := direct.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`,
			repoUUID(label), orgID, "acme/"+label, "github", at); err != nil {
			t.Fatalf("seed repo: %v", err)
		}
	}
	seed := func(id string, repoLabel string, effort float64, themes map[string]float64, evidence string) {
		t.Helper()
		var repo any
		if repoLabel != "" {
			repo = repoUUID(repoLabel)
		}
		if err := direct.Exec(ctx,
			`INSERT INTO work_unit_investments (work_unit_id, from_ts, to_ts, repo_id, effort_value, theme_distribution_json, subcategory_distribution_json, structural_evidence_json, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?,?)`,
			id, at, at, repo, effort, themes, map[string]float64{}, evidence, at, orgID); err != nil {
			t.Fatalf("seed wu %s: %v", id, err)
		}
	}
	a, b := repoUUID("repo-a"), repoUUID("repo-b")
	// wu1: two DISTINCT PRs (A#1 cited twice, once per ref shape; B#2) -> 50/50.
	seed("wu1", "", 10, map[string]float64{"feature_delivery": 1.0},
		fmt.Sprintf(`{"issues":["ghpr:acme/repo-a#1","linear:X-1"],"prs":["%s#pr1","%s#pr2"]}`, a, b))
	// wu2: one resolvable PR + one unresolvable ref -> A gets 1/2, the rest
	// attaches to NO repository (null-carrying, never redistributed).
	seed("wu2", "", 10, map[string]float64{"feature_delivery": 0.5, "risk": 0.5},
		fmt.Sprintf(`{"issues":["ghpr:acme/nope#9"],"prs":["%s#pr3"]}`, a))
	// wu3: no PR ref, own repo_id -> B in full.
	seed("wu3", "repo-b", 4, map[string]float64{"maintenance": 1.0}, `{"issues":[],"prs":[]}`)
	// wu4: no PR ref, no repo_id -> reaches no repository.
	seed("wu4", "", 100, map[string]float64{"risk": 1.0}, `{"issues":[],"prs":[]}`)

	provider := findProvider(t, providers, contextfabric.FactInvestment)
	read := func(label string) contextfabric.CanonicalFact {
		t.Helper()
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
			Time:     contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			Kind:     contextfabric.FactInvestment,
			Subjects: []contextfabric.SubjectRef{{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:" + repoUUID(label), Label: label}},
		})
		if err != nil {
			t.Fatalf("ReadFacts: %v", err)
		}
		if len(result.Facts) != 1 {
			t.Fatalf("%s facts = %d, want 1", label, len(result.Facts))
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
	near := func(got, want float64, what string) {
		t.Helper()
		if math.Abs(got-want) > 1e-9 {
			t.Fatalf("%s = %v, want %v", what, got, want)
		}
	}
	factA, factB := read("repo-a"), read("repo-b")
	wa, wb := weighted(factA), weighted(factB)
	// A: wu1 5 feature + wu2 (10*1/2=5 -> 2.5 feature, 2.5 risk).
	near(wa["feature_delivery"], 7.5, "A feature")
	near(wa["risk"], 2.5, "A risk")
	// B: wu1 5 feature + wu3 4 maintenance.
	near(wb["feature_delivery"], 5, "B feature")
	near(wb["maintenance"], 4, "B maintenance")
	// Partition: attributed effort never exceeds the units' own total, and
	// wu2's unresolvable half plus wu4 are NOT redistributed.
	near(wa["feature_delivery"]+wa["risk"]+wb["feature_delivery"]+wb["maintenance"], 19, "attributed total (of 124 persisted)")
	if err := factA.Fields["theme_breakdown"].Table.Validate(); err != nil {
		t.Fatalf("table invalid: %v", err)
	}
	if got := *factA.Fields["work_unit_count"].Integer; got != 2 {
		t.Fatalf("A work_unit_count = %d, want 2", got)
	}
	if factA.Fields["mix_source"].String == nil || factA.Fields["attribution_basis"].String == nil {
		t.Fatalf("provenance fields missing: %#v", factA.Fields)
	}
}
