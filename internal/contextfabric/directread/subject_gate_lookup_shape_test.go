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

// countingInactiveTeams records every keyed inactive-team lookup and the
// number of subjects in it.
type countingInactiveTeams struct {
	*inactiveTeams
	batches []int
}

func (c *countingInactiveTeams) InactiveTeams(ctx context.Context, principal storage.Principal, binding contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([]InactiveTeam, error) {
	c.batches = append(c.batches, len(subjects))
	return c.inactiveTeams.InactiveTeams(ctx, principal, binding, subjects)
}

type lookupShape struct {
	authorize, reach int
	inactive         []int
}

func shapeOf(graph *fakeGraph, inactive *countingInactiveTeams) lookupShape {
	return lookupShape{authorize: graph.authCalls, reach: graph.reachCalls, inactive: slices.Clone(inactive.batches)}
}

var absentTeam = subject(contractsv1.ContextFabricSubjectTeam, "team:never-existed")

func TestSubjectGateAbsentAndDeniedTeamsRunTheSameGraphSteps(t *testing.T) {
	for _, tc := range []struct {
		name      string
		principal storage.Principal
	}{{"restricted", restrictedToA()}} {
		t.Run(tc.name, func(t *testing.T) {
			run := func(refs ...contextfabric.SubjectRef) (lookupShape, Authorization) {
				graph, inactive := inactiveFixture()
				counting := &countingInactiveTeams{inactiveTeams: inactive}
				// teamU is denied to the restricted caller.
				_, decision := NewSubjectGate(struct {
					*fakeGraph
					*countingInactiveTeams
				}{graph, counting}, nil).Authorize(requestContext(), tc.principal, refs)
				return shapeOf(graph, counting), decision
			}
			denied := teamU
			deniedShape, deniedDecision := run(denied)
			absentShape, absentDecision := run(absentTeam)
			if deniedDecision.Outcomes[0].Outcome != SubjectDenied || absentDecision.Outcomes[0].Outcome != SubjectAbsent {
				t.Fatalf("setup: denied=%v absent=%v", deniedDecision.Outcomes[0].Outcome, absentDecision.Outcomes[0].Outcome)
			}
			if deniedShape.authorize != absentShape.authorize || deniedShape.reach != absentShape.reach || !slices.Equal(deniedShape.inactive, absentShape.inactive) {
				t.Fatalf("denied steps %+v differ from absent steps %+v", deniedShape, absentShape)
			}
			if len(absentShape.inactive) != 1 {
				t.Fatalf("want one keyed inactive lookup, got %+v", absentShape)
			}
			// Mixed request: still one batch covering both.
			mixed, _ := run(denied, absentTeam)
			if len(mixed.inactive) != 1 || mixed.inactive[0] != 2 {
				t.Fatalf("mixed request must use one batch of 2, got %+v", mixed)
			}
		})
	}
}

func TestSubjectGateDeniedTeamStaysDeniedAndNeverInactive(t *testing.T) {
	graph, inactive := inactiveFixture()
	counting := &countingInactiveTeams{inactiveTeams: inactive}
	// teamU also has an inactive node the caller may read; the denied
	// active node wins and the lookup result is not used.
	inactive.nodes[graphrank.SubjectKey(teamU)] = repos("acme/a")
	inactive.twin[teamU.CanonicalID] = teamNew.CanonicalID
	_, decision := NewSubjectGate(struct {
		*fakeGraph
		*countingInactiveTeams
	}{graph, counting}, nil).Authorize(requestContext(), restrictedToA(), []contextfabric.SubjectRef{teamU})
	got := decision.Outcomes[0]
	if got.Outcome != SubjectDenied || got.Inactive || got.ActiveTwinID != "" || decision.InactiveTeamCount != 0 {
		t.Fatalf("denied team changed: %+v count=%d", got, decision.InactiveTeamCount)
	}
}

func TestReadFactsAbsentAndDeniedTeamsServeOneShapeAndRunOneSteps(t *testing.T) {
	read := func(ref contextfabric.SubjectRef) (FactsResponse, lookupShape) {
		graph, inactive := inactiveFixture()
		counting := &countingInactiveTeams{inactiveTeams: inactive}
		provider := &stubProvider{capability: healthLikeCapability(), read: func(contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
			t.Error("provider read for a refused team")
			return contextfabric.FactProviderResult{State: contextfabric.SourceAvailable}, nil
		}}
		reader := newTestFactsReader(t, struct {
			*fakeGraph
			*countingInactiveTeams
		}{graph, counting}, provider)
		response, err := reader.Read(requestContext(), restrictedToA(), FactsRequest{Kinds: []string{"health"}, Subjects: []RequestSubject{{Kind: "team", CanonicalID: ref.CanonicalID}}})
		if err != nil {
			t.Fatal(err)
		}
		return response, shapeOf(graph, counting)
	}
	deniedResp, deniedShape := read(teamU)
	absentResp, absentShape := read(absentTeam)
	if deniedResp.Status != absentResp.Status || len(deniedResp.Request.SubjectsRefused) != 1 || len(absentResp.Request.SubjectsRefused) != 1 {
		t.Fatalf("shapes differ: %+v vs %+v", deniedResp, absentResp)
	}
	d, a := deniedResp.Request.SubjectsRefused[0], absentResp.Request.SubjectsRefused[0]
	if d.Answer != FactsRefusalDeniedOrNotFound || a.Answer != d.Answer || d.ActiveCanonicalID != a.ActiveCanonicalID || d.Kind != a.Kind {
		t.Fatalf("refusal differs: %+v vs %+v", d, a)
	}
	if deniedShape.authorize != absentShape.authorize || deniedShape.reach != absentShape.reach || !slices.Equal(deniedShape.inactive, absentShape.inactive) {
		t.Fatalf("denied steps %+v differ from absent steps %+v", deniedShape, absentShape)
	}
}

func TestReadRelationshipsAbsentAndDeniedTeamsServeOneShapeAndRunOneSteps(t *testing.T) {
	read := func(ref contextfabric.SubjectRef) (RelationshipsResponse, lookupShape) {
		graph, inactive := inactiveFixture()
		counting := &countingInactiveTeams{inactiveTeams: inactive}
		edges := &fakeEdgeGraph{fakeGraph: graph}
		guard := struct {
			*fakeEdgeGraph
			*countingInactiveTeams
		}{edges, counting}
		reader, err := NewRelationshipsReader(NewSubjectGate(guard, nil), guard, &relRecorder{}, testCursorKeyring())
		if err != nil {
			t.Fatal(err)
		}
		response, err := reader.Read(relCtx("shape"), restrictedToA(), RelationshipsRequest{Subject: RelationshipsSubject{Kind: "team", CanonicalID: ref.CanonicalID}})
		if err != nil {
			t.Fatal(err)
		}
		return response, shapeOf(graph, counting)
	}
	deniedResp, deniedShape := read(teamU)
	absentResp, absentShape := read(absentTeam)
	if deniedResp.Status != absentResp.Status || deniedResp.Reason != RelationshipsRefusalDeniedOrNotFound || absentResp.Reason != deniedResp.Reason || len(deniedResp.Edges) != 0 || len(absentResp.Edges) != 0 {
		t.Fatalf("shapes differ: %+v vs %+v", deniedResp, absentResp)
	}
	if deniedShape.authorize != absentShape.authorize || deniedShape.reach != absentShape.reach || !slices.Equal(deniedShape.inactive, absentShape.inactive) {
		t.Fatalf("denied steps %+v differ from absent steps %+v", deniedShape, absentShape)
	}
}
