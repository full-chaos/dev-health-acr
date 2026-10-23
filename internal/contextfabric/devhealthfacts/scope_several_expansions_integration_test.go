package devhealthfacts_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

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
	fixedNow := func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
	registry, err := contextfabric.NewFactCapabilityRegistry(devhealthfacts.NewProviders(counted), contextfabric.FactRegistryOptions{Now: fixedNow, ScopeExpander: recordScopeSpans(counted, devhealthfacts.NewScopeExpander(counted))})
	if err != nil {
		t.Fatal(err)
	}
	principal := storage.Principal{OrgID: orgID, RepositoryScopes: []string{"acme/allowed"}}
	bundle, err := registry.ReadFacts(ctx, principal, severalKindsTeamRequest())
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	scopeLineOf(t, counted, 2)
	_, statements, chain := counted.(turnCountingClient).rec.scopeMetrics()
	if chain >= 4 {
		t.Fatalf("scope_chain = %d over %d scope statements, want < 4: the independent scope reads of one team turn run in series", chain, statements)
	}

	// The same turn read the serial way (one read at a time, no side by side
	// scope reads) must serve the same answer, byte for byte, and the same
	// scope.
	serialRegistry, err := contextfabric.NewFactCapabilityRegistry(devhealthfacts.NewProviders(query), contextfabric.FactRegistryOptions{Now: fixedNow, ReadFanout: 1, ScopeExpander: devhealthfacts.NewScopeExpander(query)})
	if err != nil {
		t.Fatal(err)
	}
	serialBundle, err := serialRegistry.ReadFacts(ctx, principal, severalKindsTeamRequest())
	if err != nil {
		t.Fatalf("serial ReadFacts: %v", err)
	}
	got, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	want, err := json.Marshal(serialBundle)
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Facts) == 0 {
		t.Fatal("the turn served no fact: an empty answer proves nothing about identity")
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("answer differs from the serial walk:\nside by side %s\nserial       %s", got, want)
	}
	if bundle.Scope == nil || serialBundle.Scope == nil || !reflect.DeepEqual(*bundle.Scope, *serialBundle.Scope) {
		t.Fatalf("scope differs from the serial walk:\nside by side %+v\nserial       %+v", bundle.Scope, serialBundle.Scope)
	}
}
