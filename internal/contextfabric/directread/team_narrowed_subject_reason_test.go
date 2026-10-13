package directread

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

type emptyClickHouseClient struct{}

func (emptyClickHouseClient) Query(context.Context, string, []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	return emptyRows{}, nil
}

type emptyRows struct{}

func (emptyRows) Next() bool        { return false }
func (emptyRows) Scan(...any) error { return nil }
func (emptyRows) Err() error        { return nil }
func (emptyRows) Close() error      { return nil }

func TestNarrowedAwayRepositoryIsNeverToldTheTeamOwnsNoRepository(t *testing.T) {
	var provider contextfabric.FactProvider
	for _, candidate := range devhealthfacts.NewProviders(emptyClickHouseClient{}) {
		if candidate.Capability().Kind == contextfabric.FactOperationalDeficiencies {
			provider = candidate
		}
	}
	if provider == nil {
		t.Fatal("no operational_deficiencies provider")
	}
	team := subject(contractsv1.ContextFabricSubjectTeam, "team:none")
	response, err := newTestFactsReader(t, teamGraph(team), provider).Read(requestContext(), unrestrictedA(), FactsRequest{
		Kinds:    []string{"operational_deficiencies"},
		Subjects: []RequestSubject{{Kind: "team", CanonicalID: team.CanonicalID}, {Kind: "repository", CanonicalID: repoA.CanonicalID}},
	})
	if err != nil {
		t.Fatal(err)
	}
	repoRow := coverageOf(t, response, "operational_deficiencies", repoA)
	if strings.Contains(repoRow.Reason, "owns no repository") || strings.Contains(repoRow.Reason, "own no repository") {
		t.Errorf("repository row told a team sentence: %q", repoRow.Reason)
	}
	if repoRow.Outcome != OutcomeNotApplicable {
		t.Errorf("repository row outcome = %q, want not_applicable", repoRow.Outcome)
	}
	teamRow := coverageOf(t, response, "operational_deficiencies", team)
	if !strings.Contains(teamRow.Reason, "owns no repository") {
		t.Errorf("team row reason = %q, want its own", teamRow.Reason)
	}
}
