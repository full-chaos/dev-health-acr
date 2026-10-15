package falkorgraph

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// A question that asks for the project's identity names its OWNED_BY_TEAM edge:
// with more own edges than the path budget, a BLOCKS edge, and an owner id that
// sorts after every other id, the owner edge is still admitted, and first. The
// subject is a project, which the identity fact provider does not serve
// (repositories and work items only): the class reads the question's fact
// requirements, never which subjects a provider serves.
func TestOwnershipQuestionAdmitsTheOwnerEdgeBeyondTheCap(t *testing.T) {
	const paths = 25
	project := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project:checkout", Label: "checkout"}
	request := committedRequest(paths, project)
	request.Interpretation.FactRequirements = []contextfabric.FactRequirement{{Kind: contextfabric.FactIdentity}}
	result, err := newFakeAdapter(t, ownerFixtureFake("relationship.v2:zz:owner", "relationship.v2:zy:blocks", 40)).DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	got := admittedRelationships(t, result)
	if len(got) < 2 || got[0] != "relationship.v2:zz:owner" || got[1] != "relationship.v2:zy:blocks" {
		t.Fatalf("admitted %v, want the owner edge first, then the BLOCKS edge", got)
	}
	if len(result.DriverCandidates) != 1 {
		t.Fatalf("driver candidates %d, want 1: the BLOCKS edge makes a driver, the owner edge makes none", len(result.DriverCandidates))
	}
}

// A question that does not ask for identity does not lift the owner edge: the
// BLOCKS edge the answer gate can use stays first, and a blockers question puts
// it first as a named edge.
func TestOwnerEdgeIsNotLiftedForAQuestionThatDoesNotNameIt(t *testing.T) {
	project := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project:checkout", Label: "checkout"}
	for _, requirements := range [][]contextfabric.FactRequirement{nil, {{Kind: contextfabric.FactBlockers}}} {
		request := committedRequest(25, project)
		request.Interpretation.FactRequirements = requirements
		result, err := newFakeAdapter(t, ownerFixtureFake("relationship.v2:0:owner", "relationship.v2:zz:blocks", 40)).DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, request)
		if err != nil {
			t.Fatalf("DiscoverContext() error = %v", err)
		}
		got := admittedRelationships(t, result)
		if len(got) == 0 || got[0] != "relationship.v2:zz:blocks" {
			t.Fatalf("requirements %v: admitted %v, want the BLOCKS edge first", requirements, got)
		}
	}
}

// An edge goes to the origin that reaches it in the fewest hops, also past the
// second hop of a deeper walk: A reaches the edge y-z at its third hop, B at its
// second, so it is B's, though A's walk read it.
func TestEdgeGoesToTheOriginThatReachesItInTheFewestHops(t *testing.T) {
	ref := func(id string) contextfabric.SubjectRef {
		return contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: id}
	}
	a := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project:a"}
	b := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project:b"}
	edges := []graphrank.ResolvedEdge{
		{UUID: "a-x", From: a, To: ref("x")},
		{UUID: "x-y", From: ref("x"), To: ref("y")},
		{UUID: "y-z", From: ref("y"), To: ref("z")},
		{UUID: "b-z", From: b, To: ref("z")},
	}
	keyA, keyB := graphrank.SubjectKey(a), graphrank.SubjectKey(b)
	origin := map[string]string{"a-x": keyA, "x-y": keyA, "y-z": keyA, "b-z": keyB}
	own := attributeEdgesByFewestHops(edges, origin, []string{keyA, keyB})
	want := map[string]string{"a-x": keyA, "x-y": keyA, "y-z": keyB, "b-z": keyB}
	for id, key := range want {
		if origin[id] != key {
			t.Fatalf("edge %s: origin %s, want %s", id, origin[id], key)
		}
	}
	if !own["a-x"] || !own["b-z"] || own["x-y"] || own["y-z"] {
		t.Fatalf("own %v, want only a-x and b-z", own)
	}
}
