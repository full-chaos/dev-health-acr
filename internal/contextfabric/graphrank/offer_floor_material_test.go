package graphrank

import (
	"context"
	"reflect"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestCombineStructureOfferMaterialFoldsTheSubjectFloorAcrossMembers: the
// combined floor is refused when ANY member refused (not the last, not all)
// and carries every member's searched kinds in member order.
func TestCombineStructureOfferMaterialFoldsTheSubjectFloorAcrossMembers(t *testing.T) {
	t.Parallel()
	member := func(refused bool, kinds ...string) contextfabric.StructureOfferMaterial {
		return contextfabric.StructureOfferMaterial{SubjectFloor: contextfabric.OfferFloorOutcome{Refused: refused, SearchedKinds: kinds}}
	}
	got := combineStructureOfferMaterial(member(false, "a"), member(true, "b"), member(false, "c")).SubjectFloor
	if !got.Refused || !reflect.DeepEqual(got.SearchedKinds, []string{"a", "b", "c"}) {
		t.Fatalf("combined floor = %+v, want refused with kinds [a b c]", got)
	}
	if none := combineStructureOfferMaterial(member(false, "a"), member(false)).SubjectFloor; none.Refused || !reflect.DeepEqual(none.SearchedKinds, []string{"a"}) {
		t.Fatalf("no member refused: %+v", none)
	}
	if first := combineStructureOfferMaterial(member(true), member(false)).SubjectFloor; !first.Refused {
		t.Fatalf("a refusal from one member must survive a later non-refusing member: %+v", first)
	}
}

// TestTheSubjectFloorIsClearedByACommittedSubject: a weak neighbour is refused
// but the resolution committed a subject, so there is no subject-not-found.
func TestTheSubjectFloorIsClearedByACommittedSubject(t *testing.T) {
	t.Parallel()
	backend := &fakeGraphBackend{searchResults: map[string][]CandidateNode{"outage": {
		candidateNode(contextfabric.SubjectTeam, "team:a", "Outage A", 0.55, "*"),
		candidateNode(contextfabric.SubjectTeam, "team:b", "Outage B", 0.45, "*"),
	}}}
	deps := backend.deps()
	deps.CommitGatePolicy = lowLoneGate()
	resolution, material, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted("outage"), deps, nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Committed) != 1 || resolution.Committed[0].CanonicalID != "team:a" {
		t.Fatalf("committed = %v, want team:a (fixture)", resolution.Committed)
	}
	if material.SubjectFloor.Refused || len(material.SubjectFloor.SearchedKinds) != 0 {
		t.Fatalf("SubjectFloor = %+v, want the zero outcome when a subject committed", material.SubjectFloor)
	}
}

// TestTheSubjectFloorIsClearedByAnOfferFromTheMaterial: the pool holds only a
// weak candidate, yet the request's own expected kinds give the material
// offers, so the floor outcome must not claim there is nothing to offer.
func TestTheSubjectFloorIsClearedByAnOfferFromTheMaterial(t *testing.T) {
	t.Parallel()
	backend := &fakeGraphBackend{searchResults: map[string][]CandidateNode{"outage": {
		candidateNode(contextfabric.SubjectTeam, "team:a", "Outage A", 0.5, "*"),
	}}}
	request := testRequest()
	request.ExpectedKinds = []contextfabric.SubjectKind{contextfabric.SubjectTeam, contextfabric.SubjectProject}
	resolution, material, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted("outage"), backend.deps(), nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	offers := len(material.KindOptions) + len(material.AnchorOptions) + len(material.HandleOptions) + len(material.CandidateOptions)
	if len(resolution.Candidates) != 0 || len(resolution.Committed) != 0 || offers == 0 {
		t.Fatalf("fixture: candidates=%v committed=%v offers=%d, want no candidates, none committed, some material offer", resolution.Candidates, resolution.Committed, offers)
	}
	if material.SubjectFloor.Refused {
		t.Fatalf("SubjectFloor = %+v, want cleared by the material's own offers", material.SubjectFloor)
	}
}
