//go:build !race

package sourceload

import (
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/testsupport/repopath"
)

// Not under -race, like emitscan's real-tree cells: the two whole-closure loads need ~7.5 GB there.

const modulePath = "github.com/full-chaos/dev-health-acr/"

// TestPackagesMatchPackagesLoadOnTheRealRoots is the differential oracle: the roots the five consumers load, loaded the
// old way (go list -export, compiles the closure) and through Packages(), field by field over the whole closure.
func TestPackagesMatchPackagesLoadOnTheRealRoots(t *testing.T) {
	roots := []string{
		"internal/contextfabric/...", "internal/api/...", "internal/contracts/v1/...", "internal/mcp/...",
		"internal/contextfabric/testdata/emissionprobe",
		"cmd/operationpolicy", "internal/runtime/hosted", "internal/contextfabric/directread",
		"internal/contextfabric/factoracle", "internal/contextfabric/falkorgraph", "internal/contextfabric/sourcerow",
		"internal/mcp/guide/guidegen",
	}
	var patterns []string
	for _, r := range roots {
		patterns = append(patterns, modulePath+r)
	}
	oldRoots, gotRoots := loadBoth(t, repopath.Root(t), patterns)
	if len(oldRoots) < 20 {
		t.Fatalf("the oracle loaded %d roots: it measured too little to mean anything", len(oldRoots))
	}
	if diffs := diffClosures(oldRoots, gotRoots); len(diffs) != 0 {
		t.Fatalf("Packages() differs from packages.Load in %d place(s):\n%s", len(diffs), strings.Join(diffs, "\n"))
	}
}
