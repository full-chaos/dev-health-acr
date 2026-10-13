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

type ownershipClickHouseClient struct{ owner string }

func (c ownershipClickHouseClient) Query(_ context.Context, statement string, _ []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	if strings.Contains(statement, "team_repo_ownership") {
		return &ownershipRows{owner: c.owner, left: 1}, nil
	}
	return emptyRows{}, nil
}

type ownershipRows struct {
	owner string
	left  int
}

func (r *ownershipRows) Next() bool {
	if r.left == 0 {
		return false
	}
	r.left--
	return true
}

func (r *ownershipRows) Scan(dest ...any) error {
	values := []string{r.owner, "repo-1", "org/repo-1"}
	for i, d := range dest {
		if p, ok := d.(*string); ok && i < len(values) {
			*p = values[i]
		}
	}
	return nil
}
func (*ownershipRows) Err() error   { return nil }
func (*ownershipRows) Close() error { return nil }

func deficienciesProvider(t *testing.T, client contextpacket.ClickHouseQueryClient) contextfabric.FactProvider {
	t.Helper()
	for _, candidate := range devhealthfacts.NewProviders(client) {
		if candidate.Capability().Kind == contextfabric.FactOperationalDeficiencies {
			return candidate
		}
	}
	t.Fatal("no operational_deficiencies provider")
	return nil
}

func TestTeamOwningNoRepositoryReadsNotApplicableBesideAnOwnerThatWasNeverEvaluated(t *testing.T) {
	owner := subject(contractsv1.ContextFabricSubjectTeam, "team:owner")
	empty := subject(contractsv1.ContextFabricSubjectTeam, "team:empty")
	provider := deficienciesProvider(t, ownershipClickHouseClient{owner: "owner"})
	response, err := newTestFactsReader(t, teamGraph(owner, empty), provider).Read(requestContext(), unrestrictedA(), FactsRequest{
		Kinds:    []string{"operational_deficiencies"},
		Subjects: []RequestSubject{{Kind: "team", CanonicalID: owner.CanonicalID}, {Kind: "team", CanonicalID: empty.CanonicalID}},
	})
	if err != nil {
		t.Fatal(err)
	}
	ownerRow := coverageOf(t, response, "operational_deficiencies", owner)
	emptyRow := coverageOf(t, response, "operational_deficiencies", empty)
	if ownerRow.Outcome != OutcomeReadNoFact || ownerRow.ProviderState != "never_evaluated" {
		t.Errorf("owner row = %q / %q, want read_no_fact / never_evaluated", ownerRow.Outcome, ownerRow.ProviderState)
	}
	if emptyRow.Outcome != OutcomeNotApplicable {
		t.Errorf("empty team outcome = %q, want not_applicable", emptyRow.Outcome)
	}
	if emptyRow.ProviderState != "never_evaluated" || !strings.Contains(emptyRow.Reason, "owns no repository") {
		t.Errorf("empty team provider_state/reason = %q / %q", emptyRow.ProviderState, emptyRow.Reason)
	}
}

func TestTeamOwningNoRepositoryThatScopeExpansionReachesItemsKeepsTheDerivedOutcome(t *testing.T) {
	team := subject(contractsv1.ContextFabricSubjectTeam, "team:empty")
	provider := perSubjectProvider(contextfabric.FactPullRequests, contractsv1.ContextFabricSubjectPullRequest, contractsv1.ContextFabricSubjectTeam)
	provider.read = func(query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
		return contextfabric.FactProviderResult{
			State:                 contextfabric.SourceAvailable,
			NotApplicableSubjects: map[string]struct{}{contextfabric.FactSubjectKey(team): {}},
			SubjectReasons:        map[string]string{contextfabric.FactSubjectKey(team): "the team owns no repository in synced ownership"},
		}, nil
	}
	expander := &scriptedExpander{expand: expandTo(labelled(prA, "acme/a#532"))}
	reader := directExpandingReader(t, teamGraph(team), expander, nil, provider)
	response, err := reader.Read(requestContext(), unrestrictedA(), FactsRequest{Kinds: []string{"pull_requests"}, Subjects: []RequestSubject{{Kind: "team", CanonicalID: team.CanonicalID}}})
	if err != nil {
		t.Fatal(err)
	}
	row := coverageOf(t, response, "pull_requests", team)
	if len(expander.requests) == 0 {
		t.Fatal("scope expansion never ran")
	}
	if row.Outcome != OutcomeReadNoFact {
		t.Errorf("outcome = %q, want read_no_fact: the expansion reached pull requests, so the kind applies to this team", row.Outcome)
	}
}
