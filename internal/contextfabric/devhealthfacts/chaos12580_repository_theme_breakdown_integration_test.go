package devhealthfacts_test

import (
	"context"
	"math"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The oracle pair of CHAOS-12580, executed against a real ClickHouse: a team
// that owns repo-a and reaches repo-r only through its project's linked pull
// requests. The ownership edges alone name repo-a; the reader's rows hold
// repo-r too, for its linked pull requests only, and per theme the rows add up
// to the team's own theme_breakdown.
func TestCHAOS12580RepositoryRowsAddUpToTheTeamsThemesAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	createCHAOS5930Tables(t, ctx, direct)
	for _, statement := range devhealthschema.DDL("work_graph_issue_pr", "git_pull_requests") {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	const orgID = "org-repository-theme-breakdown"
	seedProjectLinkedOrg(t, ctx, direct, orgID)

	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactInvestment)
	result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID, RepositoryScopes: []string{"*"}}, contextfabric.FactQuery{
		Time: investmentAllHistory(), Kind: contextfabric.FactInvestment,
		Subjects: []contextfabric.SubjectRef{teamSubject("team-both")},
	})
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	fact := factFor(t, result, teamSubject("team-both"))
	value, ok := fact.Fields[contractsv1.ContextFabricRenderRepositoryFlowTableField]
	if !ok || value.Table == nil || !value.Table.Detail {
		t.Fatalf("team-both carries no declared detail %s table: %v", contractsv1.ContextFabricRenderRepositoryFlowTableField, fact.Fields)
	}
	type link struct{ repo, word, theme string }
	got := map[link]float64{}
	sums := map[string]float64{}
	for _, row := range value.Rows {
		key := link{*row.Fields["repository"].String, *row.Fields["repository_label"].String, *row.Fields["theme"].String}
		got[key] = *row.Fields["weighted_effort"].Number
		sums[key.theme] += got[key]
	}
	// repo-a whole: wu1's half (feature) and wu4 (operational). repo-r through
	// its linked pull requests only: #2 (wu1's half, feature) and #3 (wu2's
	// half, risk); not #20 and not #4, which no owned project's issue links.
	a, r := "repository:"+repoUUID("repo-a"), "repository:"+repoUUID("repo-r")
	want := map[link]float64{
		{a, "acme/repo-a", "feature_delivery"}: 0.5,
		{a, "acme/repo-a", "operational"}:      1,
		{r, "acme/repo-r", "feature_delivery"}: 0.5,
		{r, "acme/repo-r", "risk"}:             0.5,
	}
	if len(got) != len(want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
	for key, effort := range want {
		if math.Abs(got[key]-effort) > 1e-9 {
			t.Fatalf("row %+v = %v, want %v (rows %v)", key, got[key], effort, got)
		}
	}
	// The ownership edges alone name repo-a only: repo-r is a row only because
	// the reader counts the team's linked pull requests.
	wantInt(t, fact.Fields, "owned_repository_count", 1)
	for _, row := range fact.Fields["theme_breakdown"].Table.Rows {
		theme, whole := *row.Fields["theme"].String, *row.Fields["weighted_effort"].Number
		if math.Abs(sums[theme]-whole) > contractsv1.ContextFabricRenderShareSumTolerance*math.Max(math.Abs(sums[theme]), math.Abs(whole)) {
			t.Errorf("theme %s: rows sum to %v, theme_breakdown serves %v", theme, sums[theme], whole)
		}
	}
}
