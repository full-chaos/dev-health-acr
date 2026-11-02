package devhealthfacts_test

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// chaos12580_repository_theme_breakdown_test.go pins a team investment fact's
// declared detail table repository_theme_breakdown (CHAOS-12580): one row per
// (repository, theme) with effort, from the same reader rows as theme_breakdown,
// each repository labelled with its handle. It reads only the provider's public
// result, so it compiles on main, where it fails: the fact has no such table.

// repositoryNamesTable answers the repository word read of the table.
func repositoryNamesTable(teamID string) fakeTable {
	return fakeTable{match: "any(repo) AS name", rows: [][]any{{"repo-" + teamID, "acme/repo-" + teamID}}}
}

func twoRepositoryTeamTables() []fakeTable {
	mix := func(repo string, themes map[string]float64) []any {
		return []any{uint8(0), repo, themes, 0.0, uint64(2), []uint64{1, 2}, []uint64{}, clockSpanStart}
	}
	return []fakeTable{
		watermarkTable(),
		{match: "FROM team_repo_ownership", rows: [][]any{{"T", "repo-A"}, {"T", "repo-B"}}},
		{match: "any(repo) AS name", rows: [][]any{{"repo-A", "acme/api"}, {"repo-B", "acme/web"}}},
		{match: "FROM work_unit_investments", rows: [][]any{
			mix("repo-A", map[string]float64{"feature_delivery": 6, "quality": 2}),
			mix("repo-B", map[string]float64{"feature_delivery": 1.5, "operational": 0.5}),
		}},
	}
}

func readTeamInvestment(t *testing.T, client *fakeClient, teams ...string) []contextfabric.CanonicalFact {
	t.Helper()
	subjects := make([]contextfabric.SubjectRef, len(teams))
	for i, team := range teams {
		subjects[i] = teamSubject(team)
	}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactInvestment)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactInvestment, Subjects: subjects,
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	return result.Facts
}

func cellString(t *testing.T, row contextfabric.FactValueRow, column string) string {
	t.Helper()
	value := row.Fields[column].String
	if value == nil {
		t.Fatalf("row %v: column %s is not a string", row.Fields, column)
	}
	return *value
}

func cellNumber(t *testing.T, row contextfabric.FactValueRow, column string) float64 {
	t.Helper()
	value := row.Fields[column].Number
	if value == nil {
		t.Fatalf("row %v: column %s is not a number", row.Fields, column)
	}
	return *value
}

func TestCHAOS12580ATeamFactServesItsEffortByRepositoryAndTheme(t *testing.T) {
	t.Parallel()
	facts := readTeamInvestment(t, &fakeClient{tables: twoRepositoryTeamTables()}, "T")
	if len(facts) != 1 {
		t.Fatalf("facts = %d, want 1", len(facts))
	}
	value, ok := facts[0].Fields[contractsv1.ContextFabricRenderRepositoryFlowTableField]
	if !ok || value.Table == nil {
		t.Fatalf("the team fact carries no %s table: %v", contractsv1.ContextFabricRenderRepositoryFlowTableField, facts[0].Fields)
	}
	table := value.Table
	if !table.Detail || table.Shape != contextfabric.FactTableBreakdown {
		t.Fatalf("table detail=%v shape=%q, want a declared detail breakdown", table.Detail, table.Shape)
	}
	type link struct {
		repo, word, theme string
		effort            float64
	}
	var got []link
	for _, row := range value.Rows {
		got = append(got, link{cellString(t, row, "repository"), cellString(t, row, "repository_label"), cellString(t, row, "theme"), cellNumber(t, row, "weighted_effort")})
	}
	want := []link{
		{"repository:repo-A", "acme/api", "feature_delivery", 6},
		{"repository:repo-A", "acme/api", "quality", 2},
		{"repository:repo-B", "acme/web", "feature_delivery", 1.5},
		{"repository:repo-B", "acme/web", "operational", 0.5},
	}
	if len(got) != len(want) {
		t.Fatalf("rows = %+v, want %+v (no zero link)", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("row %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	// The oracle: per theme the rows add up to the theme_breakdown value, and
	// each row's theme_weighted_effort IS that value.
	served := map[string]float64{}
	for _, row := range facts[0].Fields["theme_breakdown"].Rows {
		served[cellString(t, row, "theme")] = cellNumber(t, row, "weighted_effort")
	}
	sums := map[string]float64{}
	for _, row := range value.Rows {
		theme := cellString(t, row, "theme")
		sums[theme] += cellNumber(t, row, "weighted_effort")
		if total := cellNumber(t, row, "theme_weighted_effort"); total != served[theme] {
			t.Errorf("theme %s: theme_weighted_effort %v, theme_breakdown %v", theme, total, served[theme])
		}
	}
	for theme, whole := range served {
		if math.Abs(sums[theme]-whole) > contractsv1.ContextFabricRenderShareSumTolerance*math.Max(math.Abs(sums[theme]), math.Abs(whole)) {
			t.Errorf("theme %s: rows sum to %v, theme_breakdown serves %v", theme, sums[theme], whole)
		}
	}
}

// A many-team read leaves the per-repository tables out, as it does the
// others: the rows grow with every team's repositories.
func TestCHAOS12580AManyTeamReadCarriesNoRepositoryTable(t *testing.T) {
	t.Parallel()
	tables := twoRepositoryTeamTables()
	tables[1] = fakeTable{match: "FROM team_repo_ownership", rows: [][]any{{"T", "repo-A"}, {"U", "repo-B"}}}
	for _, fact := range readTeamInvestment(t, &fakeClient{tables: tables}, "T", "U") {
		if _, ok := fact.Fields[contractsv1.ContextFabricRenderRepositoryFlowTableField]; ok {
			t.Fatalf("%s: a two-team read carries the repository table", fact.Subject.CanonicalID)
		}
	}
}

// A repository with no name gives no table: its word would be an id. The
// fact keeps theme_breakdown.
func TestCHAOS12580ARepositoryWithNoNameWithholdsTheTable(t *testing.T) {
	logs := captureSlog(t)
	tables := twoRepositoryTeamTables()
	tables[2] = fakeTable{match: "any(repo) AS name", rows: [][]any{{"repo-A", "acme/api"}}}
	facts := readTeamInvestment(t, &fakeClient{tables: tables}, "T")
	if _, ok := facts[0].Fields[contractsv1.ContextFabricRenderRepositoryFlowTableField]; ok {
		t.Fatal("the table was served with a repository that has no name")
	}
	if _, ok := facts[0].Fields["theme_breakdown"]; !ok {
		t.Fatal("theme_breakdown left with the withheld table")
	}
	if line := logs.String(); !strings.Contains(line, "devhealthfacts.repository_theme_breakdown_withheld") ||
		!strings.Contains(line, "reason=repository_without_name ") || !strings.Contains(line, "repository=repo-B ") && !strings.HasSuffix(strings.TrimSpace(line), "repository=repo-B") {
		t.Fatalf("no withheld line naming the reason and the repository: %s", line)
	}
}

// Codex r1 on #1962: a team past 64 repositories with effort in every theme
// holds more links than the table's 320-row bound. The table is withheld and
// logged; the team fact stays valid and keeps theme_breakdown.
func TestCHAOS12580ATeamPastTheRowBoundKeepsAValidFactWithoutTheTable(t *testing.T) {
	logs := captureSlog(t)
	const repositories = 65
	themes := map[string]float64{"feature_delivery": 1, "operational": 1, "maintenance": 1, "quality": 1, "risk": 1}
	var owned, names, mix [][]any
	for i := 0; i < repositories; i++ {
		repo := fmt.Sprintf("repo-%02d", i)
		owned = append(owned, []any{"T", repo})
		names = append(names, []any{repo, "acme/" + repo})
		mix = append(mix, []any{uint8(0), repo, themes, 0.0, uint64(1), []uint64{uint64(i + 1)}, []uint64{}, clockSpanStart})
	}
	client := &fakeClient{tables: []fakeTable{
		watermarkTable(),
		{match: "FROM team_repo_ownership", rows: owned},
		{match: "any(repo) AS name", rows: names},
		{match: "FROM work_unit_investments", rows: mix},
	}}
	facts := readTeamInvestment(t, client, "T")
	if len(facts) != 1 {
		t.Fatalf("facts = %d, want the team fact", len(facts))
	}
	// Every field of the fact holds its own bounds (the registry refuses the
	// whole fact for one field past them).
	for name, value := range facts[0].Fields {
		if err := value.Validate(); err != nil {
			t.Fatalf("field %s does not validate: %v", name, err)
		}
	}
	if _, ok := facts[0].Fields[contractsv1.ContextFabricRenderRepositoryFlowTableField]; ok {
		t.Fatal("a 325-link table was served past its 320-row bound")
	}
	if _, ok := facts[0].Fields["theme_breakdown"]; !ok {
		t.Fatal("theme_breakdown left with the withheld table")
	}
	if line := logs.String(); !strings.Contains(line, "reason=rows_over_bound ") {
		t.Fatalf("no withheld line for the row bound: %s", line)
	}
}
