package graphrank

import (
	"reflect"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// TestMergeFactRequirementsOrderingParity is the cross-adapter parity
// assertion Codex's P2f ruling asked for ("add a cross-adapter parity
// assertion so backend choice can't change externally visible ordering").
// Both zepgraph and falkorgraph now call this single function from their
// DiscoverContext, so proving its ordering here once transitively proves it
// for every backend that uses it -- there is no per-backend copy left to
// drift out of sync with this test.
func TestMergeFactRequirementsOrderingParity(t *testing.T) {
	existing := []contextfabric.FactRequirement{{Kind: contextfabric.FactMetrics}, {Kind: contextfabric.FactBlockers}}
	got := MergeFactRequirements(existing, contextfabric.FactWorkload, contextfabric.FactHealth)
	want := []contextfabric.FactRequirement{
		{Kind: contextfabric.FactBlockers}, {Kind: contextfabric.FactHealth}, {Kind: contextfabric.FactMetrics}, {Kind: contextfabric.FactWorkload},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MergeFactRequirements() = %#v, want %#v (sorted by Kind)", got, want)
	}
}

func TestMergeFactRequirementsDedupsAlreadyPresentKinds(t *testing.T) {
	existing := []contextfabric.FactRequirement{{Kind: contextfabric.FactHealth}}
	got := MergeFactRequirements(existing, contextfabric.FactHealth, contextfabric.FactWorkload)
	want := []contextfabric.FactRequirement{{Kind: contextfabric.FactHealth}, {Kind: contextfabric.FactWorkload}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MergeFactRequirements() = %#v, want %#v (no duplicate FactHealth)", got, want)
	}
}
