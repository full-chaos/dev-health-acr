package releasebuild

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestUnitCoverageShard_appliesExtendedTimeoutToIsolatedPackages pins the
// CHAOS-6220 fix. internal/contextfabric/devhealthfacts (and any future
// isolated package, see scripts/ci/test-shard.sh's isolated_packages) needs
// ~660-690s wall time to run clean under the plain (non -race) suite, but
// `go test`'s -timeout defaults to 10m0s (600s) -- zero margin. Before this
// fix that zero-margin default was silently in effect in the CI `unit` job
// (ci.yml, `unit (shard N of 4)`): it shards with `--with-isolated`, which
// folds isolated packages back into the round-robin `make test-coverage`
// invocation "for coverage completeness", but that invocation never applied
// any extended timeout -- so whichever shard drew devhealthfacts panicked at
// exactly 600s (CHAOS-6220 evidence: main aee26cf8 FAILs at 600.284s with
// this exact command, PASSES clean at 667.544s under -timeout 15m).
// `make test-split` (the local/Release plain-suite path) already isolated
// each package into its own invocation with its own override -- only the
// CI unit job's combined invocation was missing one.
//
// Both the shared mechanism (test-coverage's -timeout plumbing, the
// isolated-timeout lookup) and the CI job's use of it are pinned here by
// parsing the generated Makefile/ci.yml text, not by running the suite -- a
// passing run proves nothing about whether an override is silently missing
// again in the future.
func TestUnitCoverageShard_appliesExtendedTimeoutToIsolatedPackages(t *testing.T) {
	repoRoot := filepath.Join("..", "..")

	makefile, err := os.ReadFile(filepath.Join(repoRoot, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	makefileSrc := string(makefile)

	// Margin rule: the isolated budget must be >= 1.25x the CHAOS-6220
	// measured devhealthfacts wall time (690s).
	const measuredDevhealthfactsSeconds = 690.0
	isolatedTimeout := readTimeoutVar(t, makefileSrc, "GOTEST_ISOLATED_TIMEOUT")
	if float64(isolatedTimeout) < 1.25*measuredDevhealthfactsSeconds {
		t.Fatalf("GOTEST_ISOLATED_TIMEOUT=%ds is below 1.25x the CHAOS-6220 measured devhealthfacts wall time (%.1fs)", isolatedTimeout, measuredDevhealthfactsSeconds)
	}

	// test-coverage must actually accept a -timeout override -- without
	// this, no caller (test-split or the CI unit job) can raise the budget
	// for a shard that draws an isolated package.
	testCoverageRecipe, err := targetRecipe(makefileSrc, "test-coverage")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(testCoverageRecipe, "-timeout $(GOTEST_PLAIN_TIMEOUT)") {
		t.Fatalf("test-coverage must pass -timeout $(GOTEST_PLAIN_TIMEOUT) to go test; recipe:\n%s", testCoverageRecipe)
	}

	// test-split (the plain-suite path) must still isolate each isolated
	// package into its own invocation under its own override.
	testSplitRecipe, err := targetRecipe(makefileSrc, "test-split")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(testSplitRecipe, "for pkg in") ||
		!strings.Contains(testSplitRecipe, "scripts/ci/test-shard.sh isolated") {
		t.Fatalf("test-split must loop over scripts/ci/test-shard.sh's isolated packages individually, not run them inside the combined shard; recipe:\n%s", testSplitRecipe)
	}
	if !strings.Contains(testSplitRecipe, "GOTEST_PLAIN_TIMEOUT=") {
		t.Fatalf("test-split's isolated loop must override GOTEST_PLAIN_TIMEOUT per package; recipe:\n%s", testSplitRecipe)
	}

	// isolated-timeout is the single source of truth both test-split above
	// and the CI unit job below read the per-package budget from, so the
	// two paths can never silently settle on different budgets for the
	// same package.
	isolatedTimeoutRecipe, err := targetRecipe(makefileSrc, "isolated-timeout")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(isolatedTimeoutRecipe, "GOTEST_CONTEXTFABRIC_TIMEOUT") ||
		!strings.Contains(isolatedTimeoutRecipe, "GOTEST_ISOLATED_TIMEOUT") {
		t.Fatalf("isolated-timeout must select between GOTEST_CONTEXTFABRIC_TIMEOUT and GOTEST_ISOLATED_TIMEOUT by package name; recipe:\n%s", isolatedTimeoutRecipe)
	}

	// The CI `unit` job is the path CHAOS-6220 actually broke: it must
	// detect when its shard drew an isolated package and raise
	// GOTEST_PLAIN_TIMEOUT for that invocation ONLY -- the gate-cost rule
	// is never to raise the default for every package -- reading the
	// budget from isolated-timeout above rather than a second hard-coded
	// copy that could silently drift from it.
	ciYAML, err := os.ReadFile(filepath.Join(repoRoot, ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	unitStep := unitCoverageStep(t, string(ciYAML))
	if !strings.Contains(unitStep, "test-shard.sh --with-isolated") {
		t.Fatalf("unit job step must still shard with --with-isolated; step:\n%s", unitStep)
	}
	if !strings.Contains(unitStep, "scripts/ci/test-shard.sh --print-isolated-packages") {
		t.Fatalf("unit job step must check the shard's packages against scripts/ci/test-shard.sh --print-isolated-packages to detect one sharing this shard -- NOT the bare `isolated` subcommand, which scripts/ci/test-workflow-contract.sh's check_isolated_devhealthschema_job reads as \"this job runs the isolated set for real\" and would misclassify/double-count the unit job; step:\n%s", unitStep)
	}
	if !strings.Contains(unitStep, "make -s isolated-timeout") {
		t.Fatalf("unit job step must read the per-package budget from `make isolated-timeout`, not a second hard-coded copy; step:\n%s", unitStep)
	}
	if !strings.Contains(unitStep, `GOTEST_PLAIN_TIMEOUT="$isolated_timeout"`) {
		t.Fatalf("unit job step must pass the looked-up budget as GOTEST_PLAIN_TIMEOUT to make test-coverage; step:\n%s", unitStep)
	}
	if !strings.Contains(unitStep, `if [ -n "$isolated_timeout" ]`) || !strings.Contains(unitStep, "else") {
		t.Fatalf("unit job step must apply the override conditionally -- only when the shard drew an isolated package -- keeping an unconditional branch for shards that draw none; step:\n%s", unitStep)
	}

	// CHAOS-6220 own trap: a bare `test-shard.sh isolated` (or
	// `isolated-<suffix>`) call anywhere in the unit job's block is read by
	// scripts/ci/test-workflow-contract.sh's check_isolated_devhealthschema_job
	// as "this job RUNS the isolated set for real" -- it would misclassify
	// `unit` alongside race-devhealthschema/race-devhealthfacts, double-count
	// it into that checker's coverage union, and silently defeat its negative
	// control for a real dedicated job going missing (proven while building
	// this fix: `bash scripts/ci/test-workflow-contract.sh` failed exactly
	// this way with the bare form, and passed identically to an untouched
	// origin/main control once switched to the flag below).
	bareIsolatedCall := regexp.MustCompile(`test-shard\.sh[[:space:]]+isolated(-[a-z]+)?\b`)
	if bareIsolatedCall.MatchString(unitStep) {
		t.Fatalf("unit job step must not call a bare `isolated`/`isolated-<suffix>` test-shard.sh subcommand -- use --print-isolated-packages instead, see scripts/ci/test-shard.sh's own comment on that flag; step:\n%s", unitStep)
	}

	// scripts/ci/test-shard.sh must actually define --print-isolated-packages
	// as the same listing as `isolated`, not a second hand-maintained list.
	shardScript, err := os.ReadFile(filepath.Join(repoRoot, "scripts", "ci", "test-shard.sh"))
	if err != nil {
		t.Fatal(err)
	}
	shardSrc := string(shardScript)
	flagIdx := strings.Index(shardSrc, `"$1" = "--print-isolated-packages"`)
	if flagIdx < 0 {
		t.Fatalf(`scripts/ci/test-shard.sh must define a --print-isolated-packages flag`)
	}
	flagBlock := shardSrc[flagIdx:]
	if end := strings.Index(flagBlock, "fi"); end >= 0 {
		flagBlock = flagBlock[:end]
	}
	if !strings.Contains(flagBlock, `"${isolated_packages[*]}"`) {
		t.Fatalf("--print-isolated-packages must print isolated_packages itself, not a separate hand-maintained list; block:\n%s", flagBlock)
	}
}

// unitCoverageStep extracts the "Run Go tests with coverage and JUnit
// reporting" step's body from the `unit` job in ci.yml, through the next
// step header.
func unitCoverageStep(t *testing.T, ciYAML string) string {
	t.Helper()
	idx := strings.Index(ciYAML, "Run Go tests with coverage and JUnit reporting")
	if idx < 0 {
		t.Fatalf(`ci.yml must contain a "Run Go tests with coverage and JUnit reporting" step`)
	}
	rest := ciYAML[idx:]
	end := strings.Index(rest, "\n      - name:")
	if end < 0 {
		t.Fatalf("could not find the end of the coverage step in ci.yml")
	}
	return rest[:end]
}
