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
//	ACR_TEST_HNSW_SWEEP_ORG_ID=<the org id this run is sweeping> \
//	ACR_TEST_HNSW_SWEEP_CONFIRM_COPY=1 \
//	ACR_CONTEXT_FABRIC_FALKOR_GRAPH_PREFIX=<the REAL production graph prefix, e.g. acr-cf> \
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
// CLASS CLOSURE (Luna round-3, addendum: three rounds broke this same gate --
// round 1 a substring heuristic, round 2 a hardcoded denylist, round 3 a
// hardcoded/parallel PREFIX SOURCE feeding an otherwise-correct derivation).
// The defect class is "the gate's notion of the protected key is derived
// from anything other than what production itself reads." Closing it needs
// EVERY input the derivation touches to be traced to its production source:
//
//   - orgID: no static production source exists to diverge from -- a
//     production Principal's org id is per-REQUEST, never configured
//     ambient state (there is no "ACR_CONTEXT_FABRIC_ORG_ID" to read). It is
//     inherently sweep-declared; nothing to single-source it against.
//   - graphPrefix: DOES have exactly one static production source --
//     `ACR_CONTEXT_FABRIC_FALKOR_GRAPH_PREFIX` (EnvGraphPrefix), which is
//     what `cmd/acr-projector/runtime.go` and `internal/runtime/hosted/
//     open.go` both hand to `falkorgraph.ConfigFromEnv(os.LookupEnv)` --
//     `os.LookupEnv` is a DIRECT passthrough, no test-side translation layer
//     in production. So hnswSweepLookup's EnvGraphPrefix case ALSO reads
//     that literal name directly (below) -- not a dedicated ACR_TEST_*
//     variable at all. This is a deliberate, narrow exception to "every
//     input is a dedicated ACR_TEST_* name": that discipline exists to stop
//     ADDR/PASSWORD/credentials from ever reaching a real endpoint by
//     accident; GraphPrefix carries no connection or credential risk (it is
//     a string folded into a hash for a REFUSAL check), and reading anything
//     OTHER than the real value here is what created round 3's gap. There is
//     now no second variable this value could be typed into and diverge
//     from.
//   - expectedCopyKey: a confirmation mechanism (state the target twice),
//     not a shadow of any production value -- nothing to diverge from.
//
// With graphPrefix single-sourced from production's own variable and orgID/
// expectedCopyKey having no production analog to diverge from, no
// misconfiguration of a SWEEP-SPECIFIC input can redirect isSweepTargetSafe's
// derivation: graphPrefix is the value production itself would use, or the
// run is skipped (empty is refused, never defaulted).
//
// recreateVectorIndexWithOptions drops and rebuilds the target's vector
// index repeatedly -- correctness-preserving (§7 D3) with a restore-on-
// failure fallback covering both a create error and a poll failure -- but
// never something to run against a graph an operator did not deliberately
// stand up as a scratch copy (e.g. via `GRAPH.COPY <live-org-key> <dst>` in
// redis-cli, per this lane's operating instructions).
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
	// list to maintain (Luna round-2 finding 1).
	expectedCopyKey := os.Getenv("ACR_TEST_HNSW_SWEEP_EXPECTED_COPY_KEY")
	if expectedCopyKey == "" {
		t.Skip("ACR_TEST_HNSW_SWEEP_EXPECTED_COPY_KEY is not set (must independently restate ACR_TEST_HNSW_SWEEP_GRAPH_KEY)")
	}
	orgID := os.Getenv("ACR_TEST_HNSW_SWEEP_ORG_ID")
	if orgID == "" {
		t.Skip("ACR_TEST_HNSW_SWEEP_ORG_ID is not set (needed to derive and refuse the org's production graph key)")
	}
	// CLASS CLOSURE (Luna round-3 addendum, see the file doc comment): the
	// prefix is read from EnvGraphPrefix -- ACR_CONTEXT_FABRIC_FALKOR_GRAPH_PREFIX,
	// production's OWN variable, the exact one os.LookupEnv hands to
	// falkorgraph.ConfigFromEnv in cmd/acr-projector and internal/runtime/
	// hosted -- never a dedicated ACR_TEST_* name. There is no second
	// variable for this value to be typed into and diverge from. Required
	// (skip, not a silently-passed check) because an underivable comparison
	// must never be treated as a passed one.
	if os.Getenv(EnvGraphPrefix) == "" {
		t.Skip("ACR_CONTEXT_FABRIC_FALKOR_GRAPH_PREFIX is not set (production's own prefix variable -- needed to derive and refuse the org's production graph key)")
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
	// isSweepTargetSafe derives from graphConfig.GraphPrefix -- populated by
	// hnswSweepLookup from EnvGraphPrefix, production's OWN variable (see the
	// file doc comment's class-closure argument). Reading the CONSTRUCTED
	// config here, rather than the environment a second time, means there is
	// exactly one place this value is decided.
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
// live_test.go) for every CONNECTION/CREDENTIAL input: ADDR and PASSWORD
// come from a dedicated ACR_TEST_HNSW_SWEEP_* name, never a production
// ACR_CONTEXT_FABRIC_* one, so this test can never reach a REAL production
// endpoint through ambient environment.
//
// EnvGraphPrefix is the deliberate, narrow exception (Luna round-3 addendum
// class-closure argument, see the file doc comment): it is not a connection
// or credential input, it is a value the safety GATE needs to equal
// production's own, so it is read from production's OWN variable name
// directly -- the identical one os.LookupEnv hands to
// falkorgraph.ConfigFromEnv in cmd/acr-projector/runtime.go and
// internal/runtime/hosted/open.go. There is no ACR_TEST_HNSW_SWEEP_* prefix
// variable at all; nothing exists for that single fact to diverge from.
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
		return value(EnvGraphPrefix) // production's own variable, read directly -- see doc comment above.
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

// Luna round-3 addendum's class-closure test: hnswSweepLookup's
// EnvGraphPrefix case must read PRODUCTION'S OWN variable
// (ACR_CONTEXT_FABRIC_FALKOR_GRAPH_PREFIX) directly -- not a dedicated
// ACR_TEST_HNSW_SWEEP_* name that could itself be typo'd or left stale
// relative to what production actually reads. graphConfig.GraphPrefix (what
// TestLiveHNSWSweep feeds to isSweepTargetSafe) must equal EXACTLY the
// value production's own os.LookupEnv(EnvGraphPrefix) would return -- proven
// here by setting THAT literal variable and checking it round-trips. This is
// the wiring-level proof; TestIsSweepTargetSafeAcceptsTheRealProductionKeyWhenDerivedFromAWrongPrefixSource
// below proves the CONSEQUENCE of a divergent source.
func TestHNSWSweepLookupGraphPrefixReadsProductionsOwnVariableDirectly(t *testing.T) {
	t.Setenv(EnvGraphPrefix, "acr-cf")
	cfg, err := ConfigFromEnv(hnswSweepLookup)
	if err != nil {
		t.Fatalf("ConfigFromEnv: %v", err)
	}
	if cfg.GraphPrefix != "acr-cf" {
		t.Fatalf("graphConfig.GraphPrefix = %q, want production's own %s value %q -- "+
			"any indirection here (a dedicated test variable, a hardcoded stub) is exactly what let the "+
			"round-2/round-3 safety derivation compare against the WRONG production key", cfg.GraphPrefix, EnvGraphPrefix, "acr-cf")
	}
}

// The consequence, demonstrated directly against isSweepTargetSafe's own
// semantics with no revert needed: WHY the class-closure argument above
// requires production's own variable rather than any indirection at all --
// deriving from a prefix source that has diverged from what production
// actually uses (round 2's hardcoded stub, verbatim, stands in for "any
// value that isn't production's own") lets the REAL production key through;
// deriving from the correct, single-sourced prefix rejects it.
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
