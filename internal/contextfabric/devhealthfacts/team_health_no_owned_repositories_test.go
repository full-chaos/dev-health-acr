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

func TestTeamHealthOwningNoRepositoryKeepsServedFactAndNamesCause(t *testing.T) {
	t.Parallel()
	result := readTeamHealth(t, &fakeClient{tables: []fakeTable{{match: healthScalarMatch, rows: [][]any{healthRow("CHAOS")}}}})
	if len(result.Facts) != 1 || result.State != contextfabric.SourceAvailable {
		t.Fatalf("state = %q facts = %d, want the served fact kept and available", result.State, len(result.Facts))
	}
	if !strings.Contains(result.Reason, "owns no repository") {
		t.Fatalf("reason = %q, want the cause beside the fact", result.Reason)
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
