package hosted

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/evidenceref"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/sourcerow"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type noQueryClient struct{}

func (noQueryClient) Query(context.Context, string, []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	return nil, errors.New("not queried in this test")
}

// CHAOS-6180: Open composes the source-row resolver on the runtime's own
// ClickHouse client, and leaves it nil (never a typed nil) without one.
func TestOpenComposesSourceRowsOnTheClickHouseClient(t *testing.T) {
	events := []string{}
	request := testBuildRequest(t, &events, "")
	runtime, err := open(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	if runtime.Dependencies.Runtime.SourceRows != nil {
		t.Fatalf("SourceRows = %T without a ClickHouse client", runtime.Dependencies.Runtime.SourceRows)
	}

	request = testBuildRequest(t, &events, "")
	openClickHouse := request.factories.openClickHouse
	request.factories.openClickHouse = func(ctx context.Context, open clickHouseOpenRequest) (clickHouseComponents, error) {
		components, err := openClickHouse(ctx, open)
		components.queryClient = noQueryClient{}
		return components, err
	}
	runtime, err = open(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	if _, ok := runtime.Dependencies.Runtime.SourceRows.(*sourcerow.Resolver); !ok {
		t.Fatalf("SourceRows = %T, want *sourcerow.Resolver", runtime.Dependencies.Runtime.SourceRows)
	}
}

// countingGraph is a GraphAuthority that finds no subject and counts calls.
type countingGraph struct{ calls int }

func (g *countingGraph) ResolveInvestigationBinding(context.Context, storage.Principal) (contextfabric.ResolvedGraphBinding, error) {
	g.calls++
	return contextfabric.ResolvedGraphBinding{}, nil
}

func (g *countingGraph) AuthorizeStoredSubjects(_ context.Context, _ storage.Principal, _ contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([]contextfabric.StoredSubjectOutcome, error) {
	g.calls++
	outcomes := make([]contextfabric.StoredSubjectOutcome, len(subjects))
	for index := range outcomes {
		outcomes[index] = contextfabric.StoredSubjectAbsent
	}
	return outcomes, nil
}

func (g *countingGraph) OwnershipReachedRepositories(context.Context, storage.Principal, contextfabric.ResolvedGraphBinding, []contextfabric.SubjectRef) ([][]string, error) {
	g.calls++
	return nil, nil
}

// CHAOS-7227: buildSourceRows hands the direct data tools' subject gate to the
// resolver as the ownership authority for team and project refs; without a
// gate those refs stay on the persisted record.
func TestBuildSourceRowsThreadsTheSubjectGate(t *testing.T) {
	principal := storage.Principal{OrgID: "org_1"}
	graph := &countingGraph{}
	resolver := buildSourceRows(noQueryClient{}, nil, nil, directread.NewSubjectGate(graph, nil), nil)
	if resolver == nil {
		t.Fatal("no resolver composed")
	}
	if _, decision := resolver.ResolveSourceRow(context.Background(), principal, "team", "team-a"); decision.Reason != contextfabric.SourceRowNoRow || graph.calls == 0 {
		t.Fatalf("with a gate: %+v, graph calls %d", decision, graph.calls)
	}
	resolver = buildSourceRows(noQueryClient{}, nil, nil, nil, nil)
	if _, decision := resolver.ResolveSourceRow(context.Background(), principal, "team", "team-a"); decision.Reason != contextfabric.SourceRowBackendAbsent {
		t.Fatalf("without a gate: %+v", decision)
	}
}

// The production composition always carries the repo-less work item decision:
// without it a Linear work item (zero repository UUID) is decided by its empty
// slug and never served.
func TestBuildSourceRowsComposesTheRepoLessAdmitter(t *testing.T) {
	resolver, ok := buildSourceRows(noQueryClient{}, nil, nil, nil, nil).(*sourcerow.Resolver)
	if !ok || !resolver.HasRepoLessAdmitter() {
		t.Fatalf("buildSourceRows composed %T without the repo-less admitter", resolver)
	}
}

type recordingClient struct{ statements []string }

func (c *recordingClient) Query(_ context.Context, statement string, _ []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	c.statements = append(c.statements, statement)
	return nil, errors.New("recorded, not executed")
}

// The production composition (buildSourceRows) reads a project-team.v2 ref
// through the projector's own ownership group statement, for a caller whose
// repository list is "*", and reads nothing for a restricted caller.
func TestBuildSourceRowsReadsProjectTeamThroughTheProjectorsGroup(t *testing.T) {
	ref, ok := evidenceref.Mint(contractsv1.ContextFabricEvidenceEntityProjectTeamV2, "gitlab", "p1", "t1", "native")
	if !ok {
		t.Fatal("mint")
	}
	id := strings.TrimPrefix(ref, contractsv1.ContextFabricEvidenceRefPrefix+string(contractsv1.ContextFabricEvidenceEntityProjectTeamV2)+":")
	client := &recordingClient{}
	resolver := buildSourceRows(client, nil, nil, nil, nil)
	_, decision := resolver.ResolveSourceRow(context.Background(), storage.Principal{OrgID: "org_1"}, string(contractsv1.ContextFabricEvidenceEntityProjectTeamV2), id)
	if decision.Reason != contextfabric.SourceRowUnavailable || len(client.statements) != 1 {
		t.Fatalf("unrestricted: %+v, statements %d", decision, len(client.statements))
	}
	if want := devhealthsource.ProjectTeamSourceRowQuery().Statement; !strings.Contains(client.statements[0], want) {
		t.Fatalf("statement is not the projector's group read:\n%s", client.statements[0])
	}
	client.statements = nil
	_, decision = resolver.ResolveSourceRow(context.Background(), storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"acme/api"}}, string(contractsv1.ContextFabricEvidenceEntityProjectTeamV2), id)
	if decision.Reason != contextfabric.SourceRowKindOnRecord || len(client.statements) != 0 {
		t.Fatalf("restricted: %+v, statements %d", decision, len(client.statements))
	}
}

type workItemScanner struct {
	rows [][]string
	at   int
}

func (s *workItemScanner) Next() bool   { s.at++; return s.at <= len(s.rows) }
func (s *workItemScanner) Err() error   { return nil }
func (s *workItemScanner) Close() error { return nil }
func (s *workItemScanner) Scan(dest ...any) error {
	for index, value := range s.rows[s.at-1] {
		*(dest[index].(*string)) = value
	}
	return nil
}

// workItemClient finds the work item in one repository, and fails the row read
// so a test sees whether the row statement was reached.
type workItemClient struct{ statements []string }

func (c *workItemClient) Query(_ context.Context, statement string, _ []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	c.statements = append(c.statements, statement)
	if strings.Contains(statement, "evidence_locator") {
		return nil, errors.New("row read reached")
	}
	return &workItemScanner{rows: [][]string{{"20000000-0000-4000-8000-000000000002", "acme/api"}}}, nil
}

type teamGraph struct {
	outcome contextfabric.StoredSubjectOutcome
	reach   []string
}

func (g *teamGraph) ResolveInvestigationBinding(context.Context, storage.Principal) (contextfabric.ResolvedGraphBinding, error) {
	return contextfabric.ResolvedGraphBinding{}, nil
}

func (g *teamGraph) AuthorizeStoredSubjects(_ context.Context, _ storage.Principal, _ contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([]contextfabric.StoredSubjectOutcome, error) {
	outcomes := make([]contextfabric.StoredSubjectOutcome, len(subjects))
	for index := range outcomes {
		outcomes[index] = g.outcome
	}
	return outcomes, nil
}

func (g *teamGraph) OwnershipReachedRepositories(_ context.Context, _ storage.Principal, _ contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([][]string, error) {
	reached := make([][]string, len(subjects))
	for index := range reached {
		reached[index] = g.reach
	}
	return reached, nil
}

// The production composition decides a work-item-team.v2 team through the real
// directread.SubjectGate: a restricted caller whose repository reaches the team
// by ownership gets to the row read; one whose repositories do not reach it
// does not.
func TestBuildSourceRowsDecidesTheWorkItemTeamThroughTheSubjectGate(t *testing.T) {
	kind := contractsv1.ContextFabricEvidenceEntityWorkItemTeamV2
	ref, ok := evidenceref.Mint(kind, "00000000-0000-0000-0000-000000000000", "jira:ABC-1", "ari:cloud:identity::team/1", "native_team")
	if !ok {
		t.Fatal("mint")
	}
	id := strings.TrimPrefix(ref, contractsv1.ContextFabricEvidenceRefPrefix+string(kind)+":")
	principal := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"acme/api"}}
	for _, tc := range []struct {
		name    string
		graph   *teamGraph
		reached bool
	}{
		{"team owned through the granted repository", &teamGraph{outcome: contextfabric.StoredSubjectAdmitted, reach: []string{"acme/api"}}, true},
		{"team reached only through another repository", &teamGraph{outcome: contextfabric.StoredSubjectAdmitted, reach: []string{"other/secret"}}, false},
		{"team denied by the graph", &teamGraph{outcome: contextfabric.StoredSubjectDenied}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &workItemClient{}
			resolver := buildSourceRows(client, nil, nil, directread.NewSubjectGate(tc.graph, nil), nil)
			_, decision := resolver.ResolveSourceRow(context.Background(), principal, string(kind), id)
			rowRead := false
			for _, statement := range client.statements {
				rowRead = rowRead || strings.Contains(statement, "evidence_locator")
			}
			if rowRead != tc.reached {
				t.Fatalf("row statement reached %v, want %v (%+v)", rowRead, tc.reached, decision)
			}
			if tc.reached && decision.Reason != contextfabric.SourceRowUnavailable {
				t.Fatalf("%+v", decision)
			}
			if !tc.reached && decision.Reason != contextfabric.SourceRowNoRow {
				t.Fatalf("%+v", decision)
			}
		})
	}
}
