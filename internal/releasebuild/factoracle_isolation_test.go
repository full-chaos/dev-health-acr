package releasebuild

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestFactoracleIsolation_ownRaceJobAndBudget pins the isolation of the O4
// oracle package. It seeds a ClickHouse store from the venue capture and
// replays 95 cases; it ran 100s in a shared race shard and was killed at that
// shard's 420s budget twice after the capture of the venue at ops 5c9a3d32. It
// is listed in isolated_packages AND dedicated_isolated_packages, run by its
// own ci.yml job, and given its own GOTEST_FACTORACLE_TIMEOUT wired into every
// per-package timeout lookup.
func TestFactoracleIsolation_ownRaceJobAndBudget(t *testing.T) {
	repoRoot := filepath.Join("..", "..")
	const pkg = `"github.com/full-chaos/dev-health-acr/internal/contextfabric/factoracle"`

	shard, err := os.ReadFile(filepath.Join(repoRoot, "scripts", "ci", "test-shard.sh"))
	if err != nil {
		t.Fatal(err)
	}
	shardSrc := string(shard)
	isolated, err := isolatedPackagesBlock(shardSrc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(isolated, pkg) {
		t.Fatalf("isolated_packages must list factoracle; block:\n%s", isolated)
	}
	start := strings.Index(shardSrc, "dedicated_isolated_packages=(")
	if start < 0 {
		t.Fatal("dedicated_isolated_packages=( not found in scripts/ci/test-shard.sh")
	}
	rest := shardSrc[start:]
	dedicated := rest[:strings.Index(rest, ")")]
	if !strings.Contains(dedicated, pkg) {
		t.Fatalf("dedicated_isolated_packages must list factoracle (it runs alone); block:\n%s", dedicated)
	}

	makefile, err := os.ReadFile(filepath.Join(repoRoot, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	mk := string(makefile)
	if budget := readTimeoutVar(t, mk, "GOTEST_FACTORACLE_TIMEOUT"); budget <= 420 {
		t.Fatalf("GOTEST_FACTORACLE_TIMEOUT=%ds must exceed the 420s shared-shard budget it was moved out of", budget)
	}
	for _, target := range []string{"test-split", "test-race-isolated", "isolated-timeout"} {
		recipe, err := targetRecipe(mk, target)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(recipe, "*/internal/contextfabric/factoracle)") ||
			!strings.Contains(recipe, "$(GOTEST_FACTORACLE_TIMEOUT)") {
			t.Fatalf("%s must select GOTEST_FACTORACLE_TIMEOUT for factoracle; recipe:\n%s", target, recipe)
		}
	}

	ci, err := os.ReadFile(filepath.Join(repoRoot, ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	ciSrc := string(ci)
	jobStart := strings.Index(ciSrc, "\n  race-factoracle:\n")
	if jobStart < 0 {
		t.Fatal("ci.yml must define a race-factoracle job")
	}
	block := ciSrc[jobStart+1:]
	if next := regexp.MustCompile(`(?m)^  [A-Za-z0-9_-]+:\s*$`).FindStringIndex(block[3:]); next != nil {
		block = block[:next[0]+3]
	}
	for _, want := range []string{
		"name: race (factoracle, isolated scope)",
		"needs: mirror-preflight",
		"TESTCONTAINERS_HUB_IMAGE_NAME_PREFIX",
		"isolated-dedicated factoracle",
		"over 85%",
	} {
		if !strings.Contains(block, want) {
			t.Fatalf("race-factoracle job must contain %q; block:\n%s", want, block)
		}
	}
	if !regexp.MustCompile(`(?m)^      - race-factoracle\s*$`).MatchString(ciSrc) {
		t.Fatal("aggregate verify job must list race-factoracle in its needs")
	}
}
