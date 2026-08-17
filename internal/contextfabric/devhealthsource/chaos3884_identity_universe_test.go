package devhealthsource

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// TestIdentityUniverseCoversExactlyTheAliasLookupScopedKinds cross-checks
// devhealthsource's OWN source-table coverage (identityUniverseKinds)
// against graphrank's counting-scope registry (isAliasLookupScopedKind, via
// its exported mirror) -- the two registries live in different packages by
// design and must never silently drift apart. Covers the count AND that
// every kind actually queried is in scope.
func TestIdentityUniverseCoversExactlyTheAliasLookupScopedKinds(t *testing.T) {
	t.Parallel()
	want := []contractsv1.ContextFabricSubjectKind{
		contractsv1.ContextFabricSubjectRepository,
		contractsv1.ContextFabricSubjectProject,
		contractsv1.ContextFabricSubjectTeam,
		contractsv1.ContextFabricSubjectWorkItem,
	}
	for _, kind := range want {
		if !graphrank.IsAliasLookupScopedKind(kind) {
			t.Errorf("graphrank.IsAliasLookupScopedKind(%q) = false, want true -- devhealthsource's IdentityUniverse queries this kind", kind)
		}
	}
	if len(identityUniverseKinds) != len(want) {
		t.Fatalf("identityUniverseKinds has %d entries, want %d (one per graphrank-scoped kind) -- a registry drifted", len(identityUniverseKinds), len(want))
	}
}
