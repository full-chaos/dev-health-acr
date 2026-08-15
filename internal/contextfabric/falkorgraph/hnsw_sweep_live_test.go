package falkorgraph

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// CHAOS-3832 T2 LIVE SWEEP -- runs the actual efConstruction/efRuntime sweep
// (hnsw_sweep.go) against a real graph and reports recall@K + latency + index
// build time per configuration. This is the §7 D3 live-probe machinery, kept
// runnable rather than a one-shot manual probe, so T4/a later rebuild-window
// sweep can reuse it.
//
//	ACR_TEST_HNSW_SWEEP_ADDR=host:port \
//	ACR_TEST_HNSW_SWEEP_GRAPH_KEY=<graph key> \
//	ACR_TEST_HNSW_SWEEP_EXPECTED_COPY_KEY=<same graph key, typed independently> \
//	ACR_TEST_HNSW_SWEEP_GRAPH_PREFIX=<the org's production graph prefix, e.g. acr-cf> \
//	ACR_TEST_HNSW_SWEEP_ORG_ID=<the org id this run is sweeping> \
//	ACR_TEST_HNSW_SWEEP_CONFIRM_COPY=1 \
//	  go test ./internal/contextfabric/falkorgraph -run TestLiveHNSWSweep -v
//
// SAFETY (hard requirement, not a convention): this test refuses to run
// unless ALL of the following hold:
//  1. ACR_TEST_HNSW_SWEEP_CONFIRM_COPY=1 is set.
//  2. isSweepTargetSafe (hnsw_sweep.go) accepts the graph key -- FAIL-CLOSED
//     against the org's ACTUAL production graph key, DERIVED at runtime via
//     the same graphKey() identity.go uses for every real read/write, never
//     a hardcoded list (Luna round-2 finding 1: a hardcoded denylist of one
//     known key still fails open for a different prefix or a different
//     org -- a derivation-based comparison cannot).
//
// recreateVectorIndexWithOptions drops and rebuilds the target's vector
// index repeatedly -- correctness-preserving (§7 D3) with a restore-on-
// failure fallback covering both a create error and a poll failure -- but
// never something to run against a graph an operator did not deliberately
// stand up as a scratch copy (e.g. via `GRAPH.COPY <live-org-key> <dst>` in
// redis-cli, per this lane's operating instructions). None of these env var
// names is a production ACR_CONTEXT_FABRIC_* name, so no ambient production
// configuration can ever satisfy this test by accident.
//
// The dimension and seed canonical IDs are also supplied at run time --
// ACR_TEST_HNSW_SWEEP_DIMENSION and a comma-separated
// ACR_TEST_HNSW_SWEEP_SEEDS -- rather than guessed, because a wrong dimension
// would either fail loudly (good) or, worse, silently build an index no
// stored vector matches.
func TestLiveHNSWSweep(t *testing.T) {
	address := os.Getenv("ACR_TEST_HNSW_SWEEP_ADDR")
	if address == "" {
		t.Skip("ACR_TEST_HNSW_SWEEP_ADDR is not set; this sweep measures against a live graph")
	}
	key := os.Getenv("ACR_TEST_HNSW_SWEEP_GRAPH_KEY")
	if key == "" {
		t.Skip("ACR_TEST_HNSW_SWEEP_GRAPH_KEY is not set")
	}
	if os.Getenv("ACR_TEST_HNSW_SWEEP_CONFIRM_COPY") != "1" {
		t.Fatal("ACR_TEST_HNSW_SWEEP_CONFIRM_COPY=1 is required: this test repeatedly drops and rebuilds " +
			"the target's vector index and must never run against a live organization graph")
	}
	// isSweepTargetSafe (hnsw_sweep.go) derives the org's production graph
	// key at runtime and refuses if the target equals it -- FAIL-CLOSED, no
	// list to maintain (Luna round-2 finding 1). ACR_TEST_HNSW_SWEEP_ORG_ID
	// and ACR_TEST_HNSW_SWEEP_GRAPH_PREFIX are both REQUIRED (skip, not a
	// silently-passed check, if either is missing) because an underivable
	// comparison must never be treated as a passed one.
	expectedCopyKey := os.Getenv("ACR_TEST_HNSW_SWEEP_EXPECTED_COPY_KEY")
	if expectedCopyKey == "" {
		t.Skip("ACR_TEST_HNSW_SWEEP_EXPECTED_COPY_KEY is not set (must independently restate ACR_TEST_HNSW_SWEEP_GRAPH_KEY)")
	}
	// SINGLE SOURCE FOR THE PREFIX (Luna round-3 finding 1): presence is
	// checked here, but the VALUE that reaches isSweepTargetSafe below is
	// read from graphConfig.GraphPrefix AFTER construction, never from this
	// (or any other) direct os.Getenv call. Round 2's fix read this env var
	// TWICE, independently -- once (wrongly hardcoded, ignoring this var
	// entirely) via hnswSweepLookup for graphConfig.GraphPrefix, and once
	// directly here for the safety derivation. Two reads of what is meant to
	// be the same fact can diverge; a config field read once cannot.
	if os.Getenv("ACR_TEST_HNSW_SWEEP_GRAPH_PREFIX") == "" {
		t.Skip("ACR_TEST_HNSW_SWEEP_GRAPH_PREFIX is not set (needed to derive and refuse the org's production graph key)")
	}
	orgID := os.Getenv("ACR_TEST_HNSW_SWEEP_ORG_ID")
	if orgID == "" {
		t.Skip("ACR_TEST_HNSW_SWEEP_ORG_ID is not set (needed to derive and refuse the org's production graph key)")
	}
	dimensionRaw := os.Getenv("ACR_TEST_HNSW_SWEEP_DIMENSION")
	if dimensionRaw == "" {
		t.Skip("ACR_TEST_HNSW_SWEEP_DIMENSION is not set")
	}
	dimension, err := strconv.Atoi(dimensionRaw)
	if err != nil {
		t.Fatalf("ACR_TEST_HNSW_SWEEP_DIMENSION = %q is not an integer: %v", dimensionRaw, err)
	}
	seedsRaw := os.Getenv("ACR_TEST_HNSW_SWEEP_SEEDS")
	if seedsRaw == "" {
		t.Skip("ACR_TEST_HNSW_SWEEP_SEEDS is not set (comma-separated canonical_id values already present in the target graph)")
	}
	var seeds []string
	for _, s := range strings.Split(seedsRaw, ",") {
		if s = strings.TrimSpace(s); s != "" {
			seeds = append(seeds, s)
		}
	}
	if len(seeds) == 0 {
		t.Skip("ACR_TEST_HNSW_SWEEP_SEEDS parsed to zero seeds")
	}

	graphConfig, err := ConfigFromEnv(hnswSweepLookup)
	if err != nil {
		t.Fatalf("graph configuration: %v", err)
	}
	// isSweepTargetSafe derives from graphConfig.GraphPrefix -- the field
	// hnswSweepLookup populated from ACR_TEST_HNSW_SWEEP_GRAPH_PREFIX, and
	// the SAME field production reads to derive a graph key (projection.go,
	// reader.go, via identity.go's graphKey()). Reading the already-
	// constructed config here rather than the environment a second time is
	// the fix itself (Luna round-3 finding 1): there is exactly one place
	// the prefix is decided, so there is nothing left for a second read to
	// diverge from.
	if safe, reason := isSweepTargetSafe(key, expectedCopyKey, graphConfig.GraphPrefix, orgID); !safe {
		t.Fatalf("refusing to run RunHNSWSweep's destructive drop/recreate cycle: %s", reason)
	}
	adapter, err := New(graphConfig)
	if err != nil {
		t.Fatalf("New(): %v", err)
	}

	// The reference build sits at the TOP of the swept range (spec §5 L2's
	// stated range: efConstruction 200/512, efRuntime 10/50/100/200) -- the
	// highest-fidelity setting this sweep tests stands in for "as close to
	// exact as this sweep's own range reaches" (this file's doc comment
	// explains why: no client-side vector decode exists yet to run a true
	// brute-force oracle here, see hnsw_sweep.go).
	reference := SweepBuildPoint{M: 16, EfConstruction: 512, EfRuntime: 200}
	points := []SweepBuildPoint{
		{M: 16, EfConstruction: 200, EfRuntime: 10}, // production default today
		{M: 16, EfConstruction: 200, EfRuntime: 50},
		{M: 16, EfConstruction: 200, EfRuntime: 100},
		{M: 16, EfConstruction: 200, EfRuntime: 200},
		{M: 16, EfConstruction: 512, EfRuntime: 10},
		{M: 16, EfConstruction: 512, EfRuntime: 50},
		{M: 16, EfConstruction: 512, EfRuntime: 100},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	t.Logf("CHAOS-3832 T2 live sweep: key=%q dimension=%d seeds=%d reference=%s", key, dimension, len(seeds), reference)
	// onResult logs EACH point as it completes -- a multi-point live sweep
	// takes minutes, and losing every earlier point's result to one later
	// point's rebuild timeout is a real failure mode this run already hit
	// once (docs/design/context-fabric-hnsw-sweep.md), not a hypothetical.
	//
	// Luna round-1 finding 1: this test must ITSELF reject partial coverage,
	// not merely display SkippedSeeds and move on -- a point that silently
	// dropped seeds reports a recall number computed over fewer queries than
	// the run claims, which is not the measurement this test exists to
	// produce. t.Errorf (not Fatalf) so every point still gets logged even
	// if an earlier one had partial coverage.
	logResult := func(r SweepResult) {
		t.Logf("%-40s recall@20=%.3f  buildTime=%v  p50=%v  p95=%v  queries=%d  skipped=%d",
			r.Point, r.RecallAtK, r.IndexBuildTime.Round(time.Millisecond),
			r.P50Latency, r.P95Latency, r.Queries, r.SkippedSeeds)
		if r.SkippedSeeds > 0 {
			t.Errorf("%s: %d of %d seeds were skipped -- this run does not have full coverage, its recall number is partial",
				r.Point, r.SkippedSeeds, r.Queries+r.SkippedSeeds)
		}
	}
	results, err := adapter.RunHNSWSweep(ctx, key, dimension, seeds, 20, reference, points, logResult)
	if err != nil {
		t.Fatalf("RunHNSWSweep: %v (completed %d/%d points before failing, all logged above)", err, len(results), len(points)+1)
	}

	// Restore the target's index to the reference/production-relevant state
	// is deliberately NOT done here -- the copy is scratch, and the
	// OPERATOR's own procedure (GRAPH.DELETE the copy) is the cleanup step,
	// matching this lane's live-graph-safety instructions.
}

// hnswSweepLookup mirrors benchmarkLookup's discipline (ambiguity_benchmark_
// live_test.go): every value comes from a dedicated ACR_TEST_HNSW_SWEEP_*
// name or a fixed test-only default, never a production ACR_CONTEXT_FABRIC_*
// name, so this test can never reach a production graph through ambient
// environment.
func hnswSweepLookup(key string) (string, bool) {
	value := func(name string) (string, bool) {
		v := os.Getenv(name)
		return v, v != ""
	}
	switch key {
	case EnvAddr:
		return value("ACR_TEST_HNSW_SWEEP_ADDR")
	case EnvPassword:
		return value("ACR_TEST_HNSW_SWEEP_PASSWORD")
	case EnvTLS:
		return "false", true
	case EnvAllowInsecure:
		return "true", true
	case EnvGraphPrefix:
		// SINGLE SOURCE (Luna round-3 finding 1): this MUST be the same
		// ACR_TEST_HNSW_SWEEP_GRAPH_PREFIX value the safety derivation in
		// TestLiveHNSWSweep reads (via the CONSTRUCTED graphConfig.GraphPrefix,
		// not a second direct env read). The pre-fix value here was a
		// hardcoded, unused placeholder -- completely disconnected from
		// whatever the operator set for the safety check, which is exactly
		// what let a typo'd or mismatched prefix source derive the WRONG
		// "production key" and pass a real one through. There is deliberately
		// no fallback default: an unset prefix must surface as a missing
		// value (TestLiveHNSWSweep skips before this is ever reached), never
		// silently resolve to a value nobody chose.
		return value("ACR_TEST_HNSW_SWEEP_GRAPH_PREFIX")
	case EnvRequestTimeout:
		// The default 30s (config.go) is tuned for a request-path query, not
		// an HNSW index build -- live-measured (CHAOS-3832 §7 D3 probe):
		// efConstruction=512 took ~46s to reach OPERATIONAL over 35,987
		// vectors. pollVectorIndexOperational's deadline is this value, so it
		// must comfortably exceed the slowest swept build. Config.validate
		// caps RequestTimeout at 2 minutes (a production safety bound this
		// test does not get to relax); a first full sweep run TIMED OUT at
		// 100s on a LATER efConstruction=512 build under host/Docker
		// contention (docs/design/context-fabric-hnsw-sweep.md) -- build
		// latency is a distribution under contention, not a fixed number, and
		// 100s undershot its tail once already. 115s is the practical
		// ceiling; a build that needs longer belongs in a dedicated
		// rebuild-window run, not this request-path config knob (see T2's
		// sequencing note: a larger efConstruction sweep can ride inside a T3
		// rebuild window).
		if v, ok := value("ACR_TEST_HNSW_SWEEP_REQUEST_TIMEOUT"); ok {
			return v, true
		}
		return "115s", true
	default:
		return "", false
	}
}

// Luna round-3 finding 1's closure test: hnswSweepLookup's EnvGraphPrefix
// case is the ONLY place ACR_TEST_HNSW_SWEEP_GRAPH_PREFIX may reach the
// adapter's config -- graphConfig.GraphPrefix (what TestLiveHNSWSweep now
// feeds to isSweepTargetSafe) must equal EXACTLY what the operator declared,
// never a hardcoded stub that ignores it. This is the wiring-level proof;
// TestIsSweepTargetSafeAcceptsTheRealProductionKeyWhenDerivedFromAWrongPrefixSource
// below proves the CONSEQUENCE of getting this wrong.
func TestHNSWSweepLookupGraphPrefixIsTheSingleSourceOfTruth(t *testing.T) {
	t.Setenv("ACR_TEST_HNSW_SWEEP_GRAPH_PREFIX", "acr-cf")
	cfg, err := ConfigFromEnv(hnswSweepLookup)
	if err != nil {
		t.Fatalf("ConfigFromEnv: %v", err)
	}
	if cfg.GraphPrefix != "acr-cf" {
		t.Fatalf("graphConfig.GraphPrefix = %q, want the operator-declared ACR_TEST_HNSW_SWEEP_GRAPH_PREFIX value %q -- "+
			"a divergent source here is exactly what let the round-2 safety derivation compare against the WRONG "+
			"production key (Luna round-3 finding 1)", cfg.GraphPrefix, "acr-cf")
	}
}

// The consequence, demonstrated directly against isSweepTargetSafe's own
// semantics with no revert needed: deriving from a prefix source that has
// diverged from what production actually uses -- exactly what
// hnswSweepLookup's pre-fix hardcoded stub did, and equally what an
// UNVALIDATED, independently-typed prefix env var would do on a typo --
// lets the REAL production key through; deriving from the correct,
// single-sourced prefix rejects it.
func TestIsSweepTargetSafeAcceptsTheRealProductionKeyWhenDerivedFromAWrongPrefixSource(t *testing.T) {
	orgID := "70d529e0-3c06-4597-8480-794fd02328b6"
	realPrefix := "acr-cf"
	wrongPrefixSource := "acr-cf-hnsw-sweep-unused" // hnswSweepLookup's pre-fix hardcoded stub, verbatim.
	productionKey := graphKey(realPrefix, orgID)

	// The exact round-3 exploit: the operator (by mistake) sets BOTH target
	// and expectedCopyKey to the REAL production key.
	wronglySafe, _ := isSweepTargetSafe(productionKey, productionKey, wrongPrefixSource, orgID)
	if !wronglySafe {
		t.Fatal("setup invariant broken: deriving from the wrong prefix source must reproduce the round-3 vulnerability (accepting the real production key)")
	}

	correctlyUnsafe, reason := isSweepTargetSafe(productionKey, productionKey, realPrefix, orgID)
	if correctlyUnsafe {
		t.Fatal("deriving from the correct, single-sourced prefix must reject the real production key")
	}
	if reason == "" {
		t.Fatal("expected a non-empty rejection reason")
	}
}
