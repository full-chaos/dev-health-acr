package devhealthfacts_test

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func readTeamHealth(t *testing.T, client *fakeClient) contextfabric.FactProviderResult {
	t.Helper()
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactHealth)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactHealth, Subjects: []contextfabric.SubjectRef{teamSubject("CHAOS")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	return result
}

func TestTeamHealthOwningNoRepositoryIsNotApplicable(t *testing.T) {
	t.Parallel()
	result := readTeamHealth(t, &fakeClient{tables: []fakeTable{{match: "FROM compounding_risk_daily", rows: nil}}})
	if len(result.Facts) != 0 || result.State != contextfabric.SourceNotApplicable {
		t.Fatalf("state = %q facts = %d, want not_applicable with no facts", result.State, len(result.Facts))
	}
	if !strings.Contains(result.Reason, "owns no repository") {
		t.Fatalf("reason = %q, want the no-owned-repository cause", result.Reason)
	}
}

func TestTeamHealthOwningRepositoryWithoutRiskRowsStaysNoData(t *testing.T) {
	t.Parallel()
	result := readTeamHealth(t, &fakeClient{tables: []fakeTable{
		{match: "team_repo_ownership", rows: [][]any{{"CHAOS", "repo-1", "org/repo-1"}}},
		{match: "FROM compounding_risk_daily", rows: nil},
	}})
	if result.State != contextfabric.SourceNoData {
		t.Fatalf("state = %q, want no_data for a team that owns a repository but has no risk rows", result.State)
	}
}

func TestTeamHealthServedFactWithoutOwnershipKeepsStateAndAddsNoCause(t *testing.T) {
	t.Parallel()
	result := readTeamHealth(t, &fakeClient{tables: []fakeTable{{match: healthScalarMatch, rows: [][]any{healthRow("CHAOS")}}}})
	if len(result.Facts) != 1 || result.State != contextfabric.SourceAvailable {
		t.Fatalf("state = %q facts = %d, want the served fact kept and available", result.State, len(result.Facts))
	}
	if strings.Contains(result.Reason, "own no repository") || strings.Contains(result.Reason, "owns no repository") {
		t.Fatalf("reason = %q, a team that holds a fact is not named as owning nothing", result.Reason)
	}
}

func readTeamKind(t *testing.T, client *fakeClient, kind contextfabric.FactKind) contextfabric.FactProviderResult {
	t.Helper()
	provider := findProvider(t, devhealthfacts.NewProviders(client), kind)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: kind, Subjects: []contextfabric.SubjectRef{teamSubject("CHAOS")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	return result
}

func TestTeamDeficienciesOwningNoRepositoryIsNotApplicable(t *testing.T) {
	t.Parallel()
	result := readTeamKind(t, &fakeClient{}, contextfabric.FactOperationalDeficiencies)
	if len(result.Facts) != 0 || result.State != contextfabric.SourceNotApplicable || !strings.Contains(result.Reason, "owns no repository") {
		t.Fatalf("state = %q reason = %q facts = %d, want not_applicable with the no-owned-repository cause", result.State, result.Reason, len(result.Facts))
	}
}

func TestTeamDeficienciesOwningRepositoryWithoutRowsStaysNoData(t *testing.T) {
	t.Parallel()
	result := readTeamKind(t, &fakeClient{tables: []fakeTable{{match: "team_repo_ownership", rows: [][]any{{"CHAOS", "repo-1", "org/repo-1"}}}}}, contextfabric.FactOperationalDeficiencies)
	if result.State != contextfabric.SourceNoData {
		t.Fatalf("state = %q, want no_data", result.State)
	}
}

func TestTeamInvestmentOwningNoRepositoryIsNotApplicable(t *testing.T) {
	t.Parallel()
	result := readTeamKind(t, &fakeClient{}, contextfabric.FactInvestment)
	if len(result.Facts) != 0 || result.State != contextfabric.SourceNotApplicable {
		t.Fatalf("state = %q facts = %d, want not_applicable and no fact", result.State, len(result.Facts))
	}
	for _, want := range []string{"owns no repository", "investment_team_no_owned_repository"} {
		if !strings.Contains(result.Reason, want) {
			t.Errorf("reason = %q, want %q", result.Reason, want)
		}
	}
}

// teamOwningClient seeds one owned repository for the team in the shape the
// kind's ownership read scans, so a zero-row read is a team that owns
// something and holds no rows.
func teamOwningClient(kind contextfabric.FactKind, teamID string) *fakeClient {
	switch kind {
	case contextfabric.FactInvestment:
		return &fakeClient{tables: []fakeTable{{match: "team_repo_ownership", rows: [][]any{{teamID, "repo-1"}}}}}
	case contextfabric.FactOperationalDeficiencies, contextfabric.FactHealth:
		return &fakeClient{tables: []fakeTable{{match: "team_repo_ownership", rows: [][]any{{teamID, "repo-1", "org/repo-1"}}}}}
	}
	return &fakeClient{}
}

func readTeamsKind(t *testing.T, client *fakeClient, kind contextfabric.FactKind, teams ...string) contextfabric.FactProviderResult {
	t.Helper()
	subjects := make([]contextfabric.SubjectRef, 0, len(teams))
	for _, team := range teams {
		subjects = append(subjects, teamSubject(team))
	}
	provider := findProvider(t, devhealthfacts.NewProviders(client), kind)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: kind, Subjects: subjects,
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	return result
}

func TestMixedOwnershipDecidesTheStatePerTeam(t *testing.T) {
	t.Parallel()
	for _, kind := range []contextfabric.FactKind{contextfabric.FactHealth, contextfabric.FactOperationalDeficiencies, contextfabric.FactInvestment} {
		t.Run(string(kind), func(t *testing.T) {
			t.Parallel()
			mixed := readTeamsKind(t, teamOwningClient(kind, "OWNER"), kind, "OWNER", "NOTHING")
			if mixed.State != contextfabric.SourceNoData || len(mixed.Facts) != 0 {
				t.Fatalf("owner without rows beside a team that owns nothing: state = %q facts = %d, want no_data", mixed.State, len(mixed.Facts))
			}
			if !strings.Contains(mixed.RequestNote, "1 of 2 requested teams own no repository") {
				t.Fatalf("request note = %q, want the team that owns nothing named beside the owner's state", mixed.RequestNote)
			}
			if strings.Contains(mixed.Reason, "own no repository") {
				t.Fatalf("reason = %q, a kind reason true of one team only must not ride on every subject", mixed.Reason)
			}
			if got := mixed.SubjectReasons[contextfabric.FactSubjectKey(teamSubject("NOTHING"))]; !strings.Contains(got, "team owns no repository") {
				t.Fatalf("own reason of the team that owns nothing = %q", got)
			}
			if got, has := mixed.SubjectReasons[contextfabric.FactSubjectKey(teamSubject("OWNER"))]; has && strings.Contains(got, "owns no repository") {
				t.Fatalf("the owner carries an own reason %q", got)
			}
			none := readTeamsKind(t, &fakeClient{}, kind, "NOTHING", "ALSO-NOTHING")
			if none.State != contextfabric.SourceNotApplicable || !strings.Contains(none.Reason, "owns no repository") {
				t.Fatalf("every team owns nothing: state = %q reason = %q, want not_applicable with the cause", none.State, none.Reason)
			}
		})
	}
}
