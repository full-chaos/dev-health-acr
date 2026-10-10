package directread

import (
	"context"
	"slices"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

var (
	teamOld     = subject(contractsv1.ContextFabricSubjectTeam, "team:old")
	teamOldNone = subject(contractsv1.ContextFabricSubjectTeam, "team:old-none")
	teamOldB    = subject(contractsv1.ContextFabricSubjectTeam, "team:old-b")
	teamNew     = subject(contractsv1.ContextFabricSubjectTeam, "team:new")
	teamOldWild = subject(contractsv1.ContextFabricSubjectTeam, "team:old-wild")
)

// inactiveTeams answers InactiveTeams with the REAL shared node predicate over
// inactive nodes the graph holds, as falkorgraph.Adapter does.
type inactiveTeams struct {
	nodes map[string]map[string]interface{}
	twin  map[string]string
}

func (i *inactiveTeams) InactiveTeams(_ context.Context, principal storage.Principal, _ contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([]InactiveTeam, error) {
	out := make([]InactiveTeam, len(subjects))
	for index, ref := range subjects {
		attributes, ok := i.nodes[graphrank.SubjectKey(ref)]
		if !ok {
			continue
		}
		decided := graphrank.AuthorizeStoredSubjectNodes(principal, []contextfabric.SubjectRef{ref}, map[string][]graphrank.CandidateNode{graphrank.SubjectKey(ref): {{Attributes: attributes}}})
		if decided[0] == contextfabric.StoredSubjectAdmitted {
			out[index] = InactiveTeam{Inactive: true, ActiveTwinID: i.twin[ref.CanonicalID]}
		}
	}
	return out, nil
}

type inactiveFactsGraph struct {
	*fakeGraph
	*inactiveTeams
}

type inactiveEdgeGraph struct {
	*fakeEdgeGraph
	*inactiveTeams
}

func inactiveFixture() (*fakeGraph, *inactiveTeams) {
	graph := graphOfOrgA()
	graph.nodes[graphrank.SubjectKey(teamNew)] = repos("acme/a", "acme/b")
	graph.reach[graphrank.SubjectKey(teamNew)] = []string{"acme/a", "acme/b"}
	graph.reach[graphrank.SubjectKey(teamOld)] = []string{"acme/a"}
	graph.reach[graphrank.SubjectKey(teamOldB)] = []string{"acme/b"}
	graph.reach[graphrank.SubjectKey(teamOldWild)] = []string{"acme/b"}
	return graph, &inactiveTeams{
		nodes: map[string]map[string]interface{}{
			graphrank.SubjectKey(teamOld):     repos("acme/a", "acme/b"),
			graphrank.SubjectKey(teamOldNone): repos("acme/a", "acme/b"),
			graphrank.SubjectKey(teamOldB):    repos("acme/b"),
			graphrank.SubjectKey(teamOldWild): repos("acme/a"),
			graphrank.SubjectKey(teamT):       repos("acme/a", "acme/b"),
		},
		twin: map[string]string{teamOld.CanonicalID: teamNew.CanonicalID, teamOldB.CanonicalID: teamNew.CanonicalID},
	}
}

func TestInactiveTeamGateAnswersOnlyAfterTheCallerMayReadTheTeam(t *testing.T) {
	graph, inactive := inactiveFixture()
	gate := NewSubjectGate(inactiveFactsGraph{graph, inactive}, nil)
	ctx := requestContext()
	for _, tc := range []struct {
		name      string
		principal storage.Principal
		ref       contextfabric.SubjectRef
		inactive  bool
		twin      string
	}{
		{"unrestricted names the twin", unrestricted, teamOld, true, "team:new"},
		{"unrestricted with no twin", unrestricted, teamOldNone, true, ""},
		{"restricted team reaches the grant", restrictedToA(), teamOld, true, "team:new"},
		{"restricted team outside the grant is absent", restrictedToA(), teamOldB, false, ""},
		{"restricted team the node predicate admits but ownership does not reach", restrictedToA(), teamOldWild, false, ""},
		{"a team the gate admits is never turned into inactive", unrestricted, teamT, false, ""},
		{"a team the gate denies stays denied", restrictedToA(), teamU, false, ""},
		{"an id that does not exist", unrestricted, subject(contractsv1.ContextFabricSubjectTeam, "team:guess"), false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, decision := gate.Authorize(ctx, tc.principal, []contextfabric.SubjectRef{tc.ref})
			got := decision.Outcomes[0]
			if got.Inactive != tc.inactive || got.ActiveTwinID != tc.twin {
				t.Fatalf("inactive=%v twin=%q, want inactive=%v twin=%q", got.Inactive, got.ActiveTwinID, tc.inactive, tc.twin)
			}
			if got.Outcome == SubjectAdmitted && tc.ref != teamT {
				t.Fatalf("an inactive team was admitted: %+v", got)
			}
		})
	}
}

func TestInactiveTeamTwinOutsideTheGrantIsNotNamed(t *testing.T) {
	graph, inactive := inactiveFixture()
	graph.reach[graphrank.SubjectKey(teamNew)] = []string{"acme/b"}
	gate := NewSubjectGate(inactiveFactsGraph{graph, inactive}, nil)
	_, decision := gate.Authorize(requestContext(), restrictedToA(), []contextfabric.SubjectRef{teamOld})
	got := decision.Outcomes[0]
	if !got.Inactive || got.ActiveTwinID != "" {
		t.Fatalf("inactive=%v twin=%q, want inactive with no twin named", got.Inactive, got.ActiveTwinID)
	}
}

func TestReadFactsAnswersTeamInactiveForAnExplicitInactiveTeam(t *testing.T) {
	graph, inactive := inactiveFixture()
	provider := &stubProvider{capability: healthLikeCapability(), read: func(query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
		t.Errorf("provider read for an inactive team: %v", query.Subjects)
		return contextfabric.FactProviderResult{State: contextfabric.SourceAvailable}, nil
	}}
	reader := newTestFactsReader(t, inactiveFactsGraph{graph, inactive}, provider)
	response, err := reader.Read(requestContext(), unrestricted, FactsRequest{Kinds: []string{"health"}, Subjects: []RequestSubject{{Kind: "team", CanonicalID: teamOld.CanonicalID}, {Kind: "team", CanonicalID: "team:guess"}}})
	if err != nil {
		t.Fatal(err)
	}
	want := []RefusedSubject{
		{Kind: "team", CanonicalID: "team:old", Answer: FactsRefusalTeamInactive, ActiveCanonicalID: "team:new"},
		{Kind: "team", CanonicalID: "team:guess", Answer: FactsRefusalDeniedOrNotFound},
	}
	if response.Status != StatusDenied || !slices.Equal(response.Request.SubjectsRefused, want) {
		t.Fatalf("status %q refused %+v, want denied with %+v", response.Status, response.Request.SubjectsRefused, want)
	}
	if len(response.Facts) != 0 {
		t.Fatalf("facts of an inactive team were served: %+v", response.Facts)
	}
}

func TestReadRelationshipsAnswersTeamInactiveForAnExplicitInactiveTeam(t *testing.T) {
	graph, inactive := inactiveFixture()
	edges := &fakeEdgeGraph{fakeGraph: graph}
	edges.edges = []EdgeCandidate{edgeBetween("e1", "OWNED_BY_TEAM", repoA, teamOld, nil)}
	guard := inactiveEdgeGraph{edges, inactive}
	recorder := &relRecorder{}
	reader, err := NewRelationshipsReader(NewSubjectGate(guard, nil), guard, recorder, testCursorKeyring())
	if err != nil {
		t.Fatal(err)
	}
	response, err := reader.Read(relCtx("inactive"), unrestricted, RelationshipsRequest{Subject: RelationshipsSubject{Kind: "team", CanonicalID: teamOld.CanonicalID}})
	if err != nil || response.Status != RelationshipsDenied || response.Reason != FactsRefusalTeamInactive || response.ActiveCanonicalID != "team:new" || len(response.Edges) != 0 {
		t.Fatalf("%v %+v", err, response)
	}
	if edges.pageCalls != 0 {
		t.Fatal("an inactive team's edges were read")
	}
	response, err = reader.Read(relCtx("absent"), unrestricted, RelationshipsRequest{Subject: RelationshipsSubject{Kind: "team", CanonicalID: "team:guess"}})
	if err != nil || response.Reason != RelationshipsRefusalDeniedOrNotFound || response.ActiveCanonicalID != "" {
		t.Fatalf("%v %+v", err, response)
	}
}
