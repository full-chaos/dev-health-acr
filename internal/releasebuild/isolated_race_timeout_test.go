package releasebuild

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestReleaseVerify_isolatesContextfabricWithItsOwnRaceTimeout pins the
// CHAOS-5572 fix for the Release workflow's "Verify source and dependencies"
// step (`make verify` -> test-race-split -> test-race-shared +
// test-race-isolated): internal/contextfabric (the top-level package, not a
// subpackage) must be excluded from test-race-shared's round-robin shard
// split the same way internal/contextfabric/devhealthschema already is, and
// test-race-isolated must run it under its own explicit -race timeout
// distinct from GOTEST_ISOLATED_TIMEOUT (devhealthschema's budget), so a
// future retune of one package's budget can never silently move the other's.
//
// Before CHAOS-5572, internal/contextfabric shared test-race-shared's single
// unsharded `go test` invocation with ~90 other packages under
// GOTEST_TIMEOUT=420s. Its own -race cost measured flat across the two shas
// that broke the Release build (274-276s solo on a 64-core box, both shas;
// 247.735s / 229.427s on ci.yml's own real hosted race-matrix shard, also
// flat) -- the failure was rent paid on the whole shared bucket's package
// count growing (CHAOS-5494/#498 added ~973 lines and many new tests to
// cmd/acr-projector and internal/config, both still in that bucket), the
// same mechanism CHAOS-3974 already fixed once for devhealthschema.
func TestReleaseVerify_isolatesContextfabricWithItsOwnRaceTimeout(t *testing.T) {
	repoRoot := filepath.Join("..", "..")
	contextfabricPkg := `"github.com/full-chaos/dev-health-acr/internal/contextfabric"`

	shardScript, err := os.ReadFile(filepath.Join(repoRoot, "scripts", "ci", "test-shard.sh"))
	if err != nil {
		t.Fatal(err)
	}
	shardSrc := string(shardScript)

	isolatedBlock, err := isolatedPackagesBlock(shardSrc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(isolatedBlock, contextfabricPkg) {
		t.Fatalf("scripts/ci/test-shard.sh isolated_packages must list internal/contextfabric (the top-level package) so test-race-shared's round-robin excludes it; isolated_packages block:\n%s", isolatedBlock)
	}
	// Guard against a substring false-positive: the exact top-level package
	// path must appear as an array element on its own, not merely as a
	// prefix of ".../internal/contextfabric/devhealthschema" or a sibling
	// subpackage.
	exactEntry := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(contextfabricPkg) + `\s*$`)
	if !exactEntry.MatchString(isolatedBlock) {
		t.Fatalf("internal/contextfabric must be its own isolated_packages array element, not a substring match; isolated_packages block:\n%s", isolatedBlock)
	}

	makefile, err := os.ReadFile(filepath.Join(repoRoot, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(makefile)

	contextfabricTimeout := readTimeoutVar(t, content, "GOTEST_CONTEXTFABRIC_TIMEOUT")
	isolatedTimeout := readTimeoutVar(t, content, "GOTEST_ISOLATED_TIMEOUT")
	if contextfabricTimeout == isolatedTimeout {
		t.Fatalf("GOTEST_CONTEXTFABRIC_TIMEOUT (%ds) must be its own budget, not incidentally equal to GOTEST_ISOLATED_TIMEOUT (%ds) -- devhealthschema's and internal/contextfabric's growth stories are unrelated (see the comment above GOTEST_CONTEXTFABRIC_TIMEOUT)", contextfabricTimeout, isolatedTimeout)
	}
	// CHAOS-5572 TEST-EVIDENCE: measured -race wall time on the hosted
	// runner class (ci.yml's own race-matrix shard, not bigboy) was
	// 247.735s at e7e48b5c and 229.427s at 8299ec38. The standing rule
	// (cf-lane-rules) is budget >= 3x measured.
	const measuredHostedRaceSeconds = 247.735
	if float64(contextfabricTimeout) < 3*measuredHostedRaceSeconds {
		t.Fatalf("GOTEST_CONTEXTFABRIC_TIMEOUT=%ds is below 3x the measured hosted -race wall time (%.3fs); the standing rule requires budget >= 3x measured", contextfabricTimeout, measuredHostedRaceSeconds)
	}

	// test-race-isolated must select the per-package timeout by branching on
	// the package name, not apply one flat GOTEST_TIMEOUT to every isolated
	// package in a single `go test pkg1 pkg2` call -- go test runs multiple
	// listed packages concurrently by default, which would reintroduce the
	// same contention this fix removes, just between the isolated packages
	// themselves instead of across the whole shared bucket.
	recipe, err := targetRecipe(content, "test-race-isolated")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(recipe, "for pkg in") {
		t.Fatalf("test-race-isolated must run each isolated package as its own `go test` invocation (a per-package loop), not one combined multi-package call that go test would run concurrently; recipe:\n%s", recipe)
	}
	if !strings.Contains(recipe, "GOTEST_CONTEXTFABRIC_TIMEOUT") {
		t.Fatalf("test-race-isolated must select GOTEST_CONTEXTFABRIC_TIMEOUT for internal/contextfabric specifically; recipe:\n%s", recipe)
	}
}

// isolatedPackagesBlock extracts the body of the isolated_packages bash
// array from scripts/ci/test-shard.sh, from its opening `isolated_packages=(`
// through the closing `)`.
func isolatedPackagesBlock(src string) (string, error) {
	start := strings.Index(src, "isolated_packages=(")
	if start < 0 {
		return "", errNotFound("isolated_packages=( in scripts/ci/test-shard.sh")
	}
	rest := src[start:]
	end := strings.Index(rest, ")")
	if end < 0 {
		return "", errNotFound("closing ) for isolated_packages in scripts/ci/test-shard.sh")
	}
	return rest[:end], nil
}

// targetRecipe extracts a Makefile target's recipe body: from the target's
// own header line (`name:` at column 0) through the line before the next
// column-0, non-comment, non-blank line.
func targetRecipe(makefile, target string) (string, error) {
	lines := strings.Split(makefile, "\n")
	header := target + ":"
	var out []string
	grabbing := false
	for _, line := range lines {
		if strings.HasPrefix(line, header) {
			grabbing = true
			out = append(out, line)
			continue
		}
		if grabbing {
			if line == "" || strings.HasPrefix(line, "\t") || strings.HasPrefix(line, "#") {
				out = append(out, line)
				continue
			}
			break
		}
	}
	if !grabbing {
		return "", errNotFound(target + ": target in Makefile")
	}
	return strings.Join(out, "\n"), nil
}

func readTimeoutVar(t *testing.T, makefile, name string) int {
	t.Helper()
	re := regexp.MustCompile(regexp.QuoteMeta(name) + `\s*\?=\s*(\d+)s`)
	m := re.FindStringSubmatch(makefile)
	if m == nil {
		t.Fatalf("Makefile must define %s ?= <N>s", name)
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatalf("%s value %q is not an integer second count: %v", name, m[1], err)
	}
	return n
}

type errNotFound string

func (e errNotFound) Error() string { return "not found: " + string(e) }

// TestDevhealthsourceIsolation_ownRaceJobAndBudget pins the CHAOS-6342 fix.
// internal/contextfabric/devhealthsource is HEAVY (one shared testcontainer
// for the whole package) and measured 318.8s wall under -race on bigboy
// (32-core, uncontended); sharing a round-robin shard's 420s budget with
// other packages timed "race (shard 1 of 4)" out. It is isolated exactly the
// way devhealthfacts was (CHAOS-5977): listed in isolated_packages AND
// dedicated_isolated_packages, run by its own ci.yml job, and given its own
// GOTEST_DEVHEALTHSOURCE_TIMEOUT wired into every per-package timeout lookup.
func TestDevhealthsourceIsolation_ownRaceJobAndBudget(t *testing.T) {
	repoRoot := filepath.Join("..", "..")
	const pkg = `"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"`

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
		t.Fatalf("isolated_packages must list devhealthsource; block:\n%s", isolated)
	}
	start := strings.Index(shardSrc, "dedicated_isolated_packages=(")
	if start < 0 {
		t.Fatal("dedicated_isolated_packages=( not found in scripts/ci/test-shard.sh")
	}
	rest := shardSrc[start:]
	dedicated := rest[:strings.Index(rest, ")")]
	if !strings.Contains(dedicated, pkg) {
		t.Fatalf("dedicated_isolated_packages must list devhealthsource (it is HEAVY and must run alone); block:\n%s", dedicated)
	}

	makefile, err := os.ReadFile(filepath.Join(repoRoot, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	mk := string(makefile)
	// Margin rule: budget >= 1.25x the measured 318.8s bigboy wall time.
	const measuredDevhealthsourceSeconds = 318.8
	budget := readTimeoutVar(t, mk, "GOTEST_DEVHEALTHSOURCE_TIMEOUT")
	if float64(budget) < 1.25*measuredDevhealthsourceSeconds {
		t.Fatalf("GOTEST_DEVHEALTHSOURCE_TIMEOUT=%ds is below 1.25x the measured devhealthsource -race wall time (%.1fs)", budget, measuredDevhealthsourceSeconds)
	}
	for _, target := range []string{"test-split", "test-race-isolated", "isolated-timeout"} {
		recipe, err := targetRecipe(mk, target)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(recipe, "*/internal/contextfabric/devhealthsource)") ||
			!strings.Contains(recipe, "$(GOTEST_DEVHEALTHSOURCE_TIMEOUT)") {
			t.Fatalf("%s must select GOTEST_DEVHEALTHSOURCE_TIMEOUT for devhealthsource; recipe:\n%s", target, recipe)
		}
	}

	ci, err := os.ReadFile(filepath.Join(repoRoot, ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	ciSrc := string(ci)
	jobStart := strings.Index(ciSrc, "\n  race-devhealthsource:\n")
	if jobStart < 0 {
		t.Fatal("ci.yml must define a race-devhealthsource job")
	}
	block := ciSrc[jobStart+1:]
	if next := regexp.MustCompile(`(?m)^  [A-Za-z0-9_-]+:\s*$`).FindStringIndex(block[3:]); next != nil {
		block = block[:next[0]+3]
	}
	for _, want := range []string{
		"name: race (devhealthsource, isolated scope)",
		"needs: mirror-preflight",
		"TESTCONTAINERS_HUB_IMAGE_NAME_PREFIX",
		"isolated-dedicated devhealthsource",
		"CHAOS-6342",
	} {
		if !strings.Contains(block, want) {
			t.Fatalf("race-devhealthsource job must contain %q; block:\n%s", want, block)
		}
	}
	if !regexp.MustCompile(`(?m)^      - race-devhealthsource\s*$`).MatchString(ciSrc) {
		t.Fatal("aggregate verify job must list race-devhealthsource in its needs")
	}
}
