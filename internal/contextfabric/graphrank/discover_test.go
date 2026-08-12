package graphrank

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// TestAdmitEdgesAssignsClosedVocabularyRelationshipCategory is the H4
// regression test (CHAOS-3752 rebase onto main's CHAOS-3755, which landed
// the closed driver-category vocabulary): main's H4 fix changed
// zepgraph.relationMeaning to return "relationship" for every graph-derived
// driver candidate, because the more descriptive strings it originally used
// ("dependency"/"pressure"/"signal") are not members of the closed
// ContextFabricDriverCategory enum and would make driver.Validate() reject
// EVERY graph-discovered driver outright once Category started being
// validated as a closed enum.
//
// The CHAOS-3752 graphrank extraction (this package) was branched before
// H4 landed, so its own copy of relationMeaning silently reintroduced the
// pre-H4 strings when this branch rebased onto main. No test in graphrank,
// zepgraph, or falkorgraph previously asserted a graph-discovered driver's
// Category value at all -- this test closes that gap directly against the
// shared AdmitEdges entry point every backend adapter calls, so neither
// backend can regress this again silently.
func TestAdmitEdgesAssignsClosedVocabularyRelationshipCategory(t *testing.T) {
	from := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_from", Label: "From Work"}
	to := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_to", Label: "To Work"}
	options := contextfabric.InvestigationOptions{
		MaxSubjectCandidates: 10, MaxCohortMembers: 10, MaxRelationshipPaths: 10,
		MaxDrivers: 10, MaxEvidenceRefs: 50, MaxSerializedBytes: 262144,
	}
	// One relation name per relationMeaning branch -- BLOCKS (principal),
	// CAUSES (contributing), INDICATES (symptom) -- plus a name matching no
	// branch (context, driver=false, excluded from Drivers) to prove the
	// closed category applies uniformly, not only to the branches that
	// happen to produce a driver.
	relations := []string{"BLOCKS", "CAUSES", "INDICATES"}
	edges := make([]ResolvedEdge, 0, len(relations))
	for i, name := range relations {
		edges = append(edges, ResolvedEdge{
			UUID: "edge_" + name, Name: name, Fact: "test fact",
			From: from, To: to, Attributes: map[string]interface{}{"evidence_refs": []string{"evidence_" + name}},
			CreatedAt: "2026-01-01T00:00:00Z",
		})
		_ = i
	}

	result := AdmitEdges("org-1", edges, options, func(contextfabric.SubjectRef) bool { return false })
	if len(result.Drivers) != len(relations) {
		t.Fatalf("AdmitEdges() produced %d drivers, want %d: %#v", len(result.Drivers), len(relations), result.Drivers)
	}
	for _, driver := range result.Drivers {
		if driver.Category != "relationship" {
			t.Fatalf("driver.Category = %q, want \"relationship\" (the closed-vocabulary category every graph-discovered driver must use)", driver.Category)
		}
		if err := driver.Validate(); err != nil {
			t.Fatalf("driver.Validate() error = %v, want a graph-discovered driver to validate against the closed category enum: %#v", err, driver)
		}
	}
}
