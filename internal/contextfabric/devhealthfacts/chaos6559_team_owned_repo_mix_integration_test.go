package devhealthfacts_test

// CHAOS-6559: a team's investment mix is the SUM of the mixes of the
// repositories it owns (team_repo_ownership), EXECUTED against a real
// ClickHouse. Invariant asserted: team table == sum of the owned
// repositories' own facts, per theme.

import (
	"context"
	"fmt"
	"math"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestTeamThemeMixIsTheSumOfOwnedRepositoriesAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	createCHAOS5930Tables(t, ctx, direct)
	providers := devhealthfacts.NewProviders(query)
	at := ts(2026, 9, 18, 0, 0, 0)
	const orgID = "org-team-owned-mix"

	for _, label := range []string{"repo-a", "repo-b", "repo-c"} {
		if err := direct.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`,
			repoUUID(label), orgID, "acme/"+label, "github", at); err != nil {
			t.Fatalf("seed repo: %v", err)
		}
	}
	own := func(teamID, label, source string) {
		t.Helper()
		if err := direct.Exec(ctx, `INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			orgID, "github", teamID, repoUUID(label), "acme/"+label, "exact", source, uint8(1), uint16(100), int32(0), at, nil, at); err != nil {
			t.Fatalf("seed ownership: %v", err)
		}
	}
	// team-1 owns repo-a through TWO sources and repo-b; team-2 owns repo-b
	// only (a shared repo counts in each owner); nobody owns repo-c.
	own("team-1", "repo-a", "native")
	own("team-1", "repo-a", "manual")
	own("team-1", "repo-b", "native")
	own("team-2", "repo-b", "native")
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

	provider := findProvider(t, providers, contextfabric.FactInvestment)
	read := func(subject contextfabric.SubjectRef) contextfabric.CanonicalFact {
		t.Helper()
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{subject},
		})
		if err != nil {
			t.Fatalf("ReadFacts: %v", err)
		}
		if len(result.Facts) != 1 {
			t.Fatalf("%s facts = %d, want 1", subject.CanonicalID, len(result.Facts))
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
	repo := func(label string) contextfabric.SubjectRef {
		return contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:" + repoUUID(label), Label: label}
	}
	team1 := read(teamSubject("team-1"))
	team2 := read(teamSubject("team-2"))
	wa, wb := weighted(read(repo("repo-a"))), weighted(read(repo("repo-b")))
	w1, w2 := weighted(team1), weighted(team2)
	for _, theme := range []string{"feature_delivery", "operational", "maintenance", "quality", "risk"} {
		if d := math.Abs(w1[theme] - (wa[theme] + wb[theme])); d > 1e-9 {
			t.Fatalf("team-1 %s = %v, want repo-a+repo-b = %v", theme, w1[theme], wa[theme]+wb[theme])
		}
		if d := math.Abs(w2[theme] - wb[theme]); d > 1e-9 {
			t.Fatalf("team-2 %s = %v, want repo-b = %v", theme, w2[theme], wb[theme])
		}
	}
	// Concrete numbers: repo-a = wu1/2 (5 feature) + wu2 (4 risk, 4 quality);
	// repo-b = wu1/2 (5 feature) + wu3 (6 maintenance). repo-c's operational
	// 50 is owned by nobody and reaches neither team.
	if w1["feature_delivery"] != 10 || w1["risk"] != 4 || w1["maintenance"] != 6 || w1["operational"] != 0 {
		t.Fatalf("team-1 weighted = %v", w1)
	}
	if got := *team1.Fields["owned_repository_count"].Integer; got != 2 {
		t.Fatalf("team-1 owned_repository_count = %d, want 2 (repo-a counted once across two sources)", got)
	}
	if err := team1.Fields["theme_breakdown"].Table.Validate(); err != nil {
		t.Fatalf("table invalid: %v", err)
	}
}
