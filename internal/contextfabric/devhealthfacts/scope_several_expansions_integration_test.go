package devhealthfacts_test

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/chfixture"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// severalKindsTeamRequirements is what a team status question asks for together:
// its pull requests, its CI runs and its blockers. Each has an enabled team
// scope policy, so one Resolve expands more than one fact kind from the one team
// root.
var severalKindsTeamRequirements = []contextfabric.FactRequirement{
	{Kind: contextfabric.FactPullRequests},
	{Kind: contextfabric.FactContinuousIntegration},
	{Kind: contextfabric.FactBlockers},
}

func severalKindsTeamRequest() contextfabric.CanonicalFactRequest {
	return contextfabric.CanonicalFactRequest{
		Question:          contextfabric.InterpretedQuestion{TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}},
		Subjects:          []contextfabric.SubjectRef{teamSubject(servedPRTeamID)},
		Requirements:      severalKindsTeamRequirements,
		PullRequestStatus: contextfabric.PullRequestStatusOpen,
		PullRequestFacts:  true,
	}
}

type failingQueryClient struct{}

func (failingQueryClient) Query(context.Context, string, []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	return nil, errors.New("no database in this probe")
}

// kindRecordingExpander answers every expansion with nothing and keeps the
// requirement kind of each call.
type kindRecordingExpander struct {
	mu    sync.Mutex
	kinds []string
}

func (e *kindRecordingExpander) ExpandFactScope(_ context.Context, request contextfabric.FactScopeExpansionRequest) (contextfabric.FactScopeExpansionResult, error) {
	e.mu.Lock()
	e.kinds = append(e.kinds, string(request.RequirementKind))
	e.mu.Unlock()
	return contextfabric.FactScopeExpansionResult{}, nil
}

// The request set of the hosted turn really makes several kinds expand from the
// one team root. It needs no database: the expander is a recorder and every
// provider read fails, which is after the scope phase this counts.
func TestSeveralKindsTeamRequestExpandsMoreThanOneKind(t *testing.T) {
	expander := &kindRecordingExpander{}
	registry, err := contextfabric.NewFactCapabilityRegistry(devhealthfacts.NewProviders(failingQueryClient{}), contextfabric.FactRegistryOptions{ScopeExpander: expander})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = registry.ReadFacts(context.Background(), storage.Principal{OrgID: sharedTestOrgID(t), RepositoryScopes: []string{"acme/allowed"}}, severalKindsTeamRequest())
	sort.Strings(expander.kinds)
	t.Logf("expansions by kind: %s", strings.Join(expander.kinds, ","))
	if len(expander.kinds) < 2 {
		t.Fatalf("the request set expands %d kind(s) %v, want at least 2", len(expander.kinds), expander.kinds)
	}
}

// A team turn that makes three fact kinds expand from one root, read through
// the registry with the real expander and the statement counter, as the hosted
// runtime wires it. It logs the same turn_statements and scope_phase lines as
// the one-expansion turns and fails unless at least two kinds expanded, so a
// silent one or zero cannot pass for a measurement.
func TestTurnScopeStatementsOfATeamTurnThatExpandsSeveralKinds(t *testing.T) {
	chfixture.RequireContainers(t)
	ctx := context.Background()
	orgID := sharedTestOrgID(t)
	seedServedPRTeam(t, ctx, orgID)
	query, direct := sharedClickHouseFixture(t)
	for _, statement := range devhealthschema.DDL("git_pull_requests", "ci_pipeline_runs", "work_items", "work_item_dependencies", "repos", "team_repo_ownership") {
		if err := direct.Exec(ctx, statement); err != nil && !strings.Contains(err.Error(), "already exists") {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	counted := countTurnStatements(t, query)
	registry, err := contextfabric.NewFactCapabilityRegistry(devhealthfacts.NewProviders(counted), contextfabric.FactRegistryOptions{ScopeExpander: recordScopeSpans(counted, devhealthfacts.NewScopeExpander(counted))})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.ReadFacts(ctx, storage.Principal{OrgID: orgID, RepositoryScopes: []string{"acme/allowed"}}, severalKindsTeamRequest()); err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	scopeLineOf(t, counted, 2)
}
