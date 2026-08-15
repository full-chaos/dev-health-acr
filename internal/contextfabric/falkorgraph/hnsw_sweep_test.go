package falkorgraph

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// Luna round-2 finding 1: the safety gate must FAIL CLOSED against a
// DERIVED production key, not a hardcoded list -- any key this org's
// graphKey derivation produces must be rejected, whether or not anyone
// remembered to hardcode it.
func TestIsSweepTargetSafeRejectsTheDerivedProductionKeyForTheDeclaredOrg(t *testing.T) {
	prefix, orgID := "acr-cf", "70d529e0-3c06-4597-8480-794fd02328b6"
	production := graphKey(prefix, orgID)
	safe, reason := isSweepTargetSafe(production, production, prefix, orgID)
	if safe {
		t.Fatalf("the derived production key must never be accepted even when it matches the declared expected copy key, got safe=true reason=%q", reason)
	}
	if reason == "" {
		t.Fatal("expected a non-empty rejection reason")
	}
}

// A DIFFERENT graph prefix or organization -- the exact gap Luna named --
// still derives to a key isSweepTargetSafe must reject if the target
// happens to land on it, WITHOUT any hardcoded entry naming it.
func TestIsSweepTargetSafeRejectsTheDerivedKeyForAnyOrgNoHardcodingNeeded(t *testing.T) {
	prefix, orgID := "acr-cf", "some-entirely-different-org-id"
	production := graphKey(prefix, orgID)
	safe, _ := isSweepTargetSafe(production, production, prefix, orgID)
	if safe {
		t.Fatal("a derived production key must be rejected regardless of which org it derives from -- no list to maintain means no org is ever missed")
	}
}

// Condition 1: the target key must exactly equal the operator's
// independently declared expected copy key -- a mismatch (e.g. an env var
// typo) is refused even though the target itself might otherwise be safe.
func TestIsSweepTargetSafeRejectsATargetThatDoesNotMatchTheDeclaredExpectedCopyKey(t *testing.T) {
	safe, reason := isSweepTargetSafe("acr-cf-actual-copy-key", "acr-cf-a-different-declared-key", "acr-cf", "some-org")
	if safe {
		t.Fatal("a target/expected mismatch must be rejected")
	}
	if reason == "" {
		t.Fatal("expected a non-empty rejection reason")
	}
}

func TestIsSweepTargetSafeAcceptsAGenuineScratchCopyDistinctFromTheDerivedProductionKey(t *testing.T) {
	copyKey := "acr-cf-3832-sweep-copy-run2"
	safe, reason := isSweepTargetSafe(copyKey, copyKey, "acr-cf", "70d529e0-3c06-4597-8480-794fd02328b6")
	if !safe {
		t.Fatalf("a target matching the declared expected key, distinct from the derived production key, must be accepted, got reason=%q", reason)
	}
}

// UNDERIVABLE = REFUSE: an empty prefix, org id, or expected key must not be
// silently treated as "check passes" -- an unevaluable safety check refuses.
func TestIsSweepTargetSafeRefusesWhenItCannotDeriveTheProductionKey(t *testing.T) {
	cases := []struct {
		name, target, expected, prefix, orgID string
	}{
		{"empty prefix", "acr-cf-copy", "acr-cf-copy", "", "some-org"},
		{"empty org id", "acr-cf-copy", "acr-cf-copy", "acr-cf", ""},
		{"empty expected key", "acr-cf-copy", "", "acr-cf", "some-org"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			safe, reason := isSweepTargetSafe(c.target, c.expected, c.prefix, c.orgID)
			if safe {
				t.Fatalf("an underivable safety check must refuse, not pass, got safe=true reason=%q", reason)
			}
			if reason == "" {
				t.Fatal("expected a non-empty rejection reason")
			}
		})
	}
}

// The round-1 substring heuristic ("contains copy") is GONE: a target that
// matches its declared expected key and is not the derived production key
// must be accepted even though its name carries no "copy" marker at all --
// proving the derivation-based comparison, not a naming convention, is what
// the gate actually rests on now.
func TestIsSweepTargetSafeAcceptsAMatchingKeyWithNoCopySubstringAtAll(t *testing.T) {
	key := "acr-cf-scratch-3832-run"
	safe, reason := isSweepTargetSafe(key, key, "acr-cf", "70d529e0-3c06-4597-8480-794fd02328b6")
	if !safe {
		t.Fatalf("a key with no \"copy\" substring must still be accepted once the substring heuristic is removed, got reason=%q", reason)
	}
}

func TestDedupeSweepPointsPutsReferenceFirstAndDropsDuplicates(t *testing.T) {
	reference := SweepBuildPoint{M: 16, EfConstruction: 200, EfRuntime: 10}
	points := []SweepBuildPoint{
		{M: 16, EfConstruction: 200, EfRuntime: 50},
		reference, // duplicate of the reference itself
		{M: 16, EfConstruction: 200, EfRuntime: 50}, // duplicate of the first point
		{M: 16, EfConstruction: 512, EfRuntime: 100},
	}
	got := dedupeSweepPoints(reference, points)
	want := []SweepBuildPoint{
		reference,
		{M: 16, EfConstruction: 200, EfRuntime: 50},
		{M: 16, EfConstruction: 512, EfRuntime: 100},
	}
	if len(got) != len(want) {
		t.Fatalf("dedupeSweepPoints() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("dedupeSweepPoints()[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestLatencyPercentilesEmptyIsZero(t *testing.T) {
	p50, p95 := latencyPercentiles(nil)
	if p50 != 0 || p95 != 0 {
		t.Fatalf("latencyPercentiles(nil) = (%v, %v), want (0, 0)", p50, p95)
	}
}

func TestLatencyPercentilesOrdersUnsortedInput(t *testing.T) {
	samples := []time.Duration{
		50 * time.Millisecond, 10 * time.Millisecond, 100 * time.Millisecond,
		20 * time.Millisecond, 30 * time.Millisecond, 40 * time.Millisecond,
		60 * time.Millisecond, 70 * time.Millisecond, 80 * time.Millisecond, 90 * time.Millisecond,
	}
	p50, p95 := latencyPercentiles(samples)
	if p50 != 50*time.Millisecond {
		t.Fatalf("p50 = %v, want 50ms", p50)
	}
	if p95 != 90*time.Millisecond && p95 != 100*time.Millisecond {
		// nearest-rank at n=10, p95 lands on index 9 (95th of 10) -> either
		// the 90ms or 100ms sample depending on rounding; both are the
		// correct tail, just pin it to one of the two adjacent samples.
		t.Fatalf("p95 = %v, want the top of the distribution (90ms or 100ms)", p95)
	}
}

// RunHNSWSweep end to end against a fake conn: the reference point must be
// built first, report RecallAtK=1.0 against itself, and every other point's
// recall must be computed against the REFERENCE's captured top-K, not
// against each other.
func TestRunHNSWSweepReferencePointReportsPerfectRecall(t *testing.T) {
	reference := SweepBuildPoint{M: 16, EfConstruction: 200, EfRuntime: 200}
	degraded := SweepBuildPoint{M: 16, EfConstruction: 200, EfRuntime: 10}

	var currentBuild SweepBuildPoint
	buildCount := map[SweepBuildPoint]int{}
	fake := &fakeConn{
		queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
			switch {
			case strings.HasPrefix(cypher, "CREATE VECTOR INDEX"):
				// Record which build is currently active so the seed query
				// below can hand back a build-dependent result set.
				if strings.Contains(cypher, "efRuntime:200") {
					currentBuild = reference
				} else {
					currentBuild = degraded
				}
				buildCount[currentBuild]++
			case strings.Contains(cypher, "db.idx.vector.queryNodes"):
				seed, _ := params["seed"].(string)
				if currentBuild == reference {
					// The reference build always finds the true 2 neighbors,
					// at DISTINCT (non-tied) scores.
					if seed == "seed-a" {
						return []row{{"id": "n1", "score": 0.1}, {"id": "n2", "score": 0.2}}, nil
					}
					return []row{{"id": "n3", "score": 0.1}, {"id": "n4", "score": 0.2}}, nil
				}
				// The degraded build misses one true neighbor per seed.
				if seed == "seed-a" {
					return []row{{"id": "n1", "score": 0.1}, {"id": "nX", "score": 0.3}}, nil
				}
				return []row{{"id": "nY", "score": 0.1}, {"id": "n4", "score": 0.2}}, nil
			}
			return nil, nil
		},
		indexesFunc: func(ctx context.Context, graphKey string) ([]indexStatus, error) {
			return []indexStatus{{
				Label: labelSubject, Status: "OPERATIONAL",
				Types:   map[string][]string{propEmbedding: {"VECTOR"}},
				Options: map[string]interface{}{propEmbedding: map[string]interface{}{"dimension": int64(4)}},
			}}, nil
		},
	}
	adapter := newFakeAdapter(t, fake)
	results, err := adapter.RunHNSWSweep(context.Background(), "k", 4,
		[]string{"seed-a", "seed-b"}, 2, reference, []SweepBuildPoint{degraded}, nil)
	if err != nil {
		t.Fatalf("RunHNSWSweep: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d: %#v", len(results), results)
	}
	if results[0].Point != reference {
		t.Fatalf("results[0].Point = %v, want the reference point built first", results[0].Point)
	}
	if results[0].RecallAtK != 1.0 {
		t.Fatalf("reference point RecallAtK = %v, want 1.0 by construction", results[0].RecallAtK)
	}
	if results[1].Point != degraded {
		t.Fatalf("results[1].Point = %v, want %v", results[1].Point, degraded)
	}
	// Each seed's degraded top-2 shares exactly 1 of 2 with the reference's
	// top-2 -> recall 0.5 for both seeds -> average 0.5.
	if results[1].RecallAtK != 0.5 {
		t.Fatalf("degraded point RecallAtK = %v, want 0.5", results[1].RecallAtK)
	}
	if buildCount[reference] != 1 || buildCount[degraded] != 1 {
		t.Fatalf("expected exactly one index build per distinct point, got %v", buildCount)
	}
}

// onResult must fire per point, synchronously, before the whole sweep
// finishes -- and a later point's failure must still leave every EARLIER
// point's result in the returned slice (not just delivered to onResult),
// because a caller that does not pass onResult must not lose completed work
// to one bad point (this is exactly what a live 512-efConstruction rebuild
// timing out mid-sweep did on the first real run -- see docs/design/
// context-fabric-hnsw-sweep.md).
func TestRunHNSWSweepDeliversResultsIncrementallyAndPreservesThemOnAMidSweepFailure(t *testing.T) {
	reference := SweepBuildPoint{EfRuntime: 200}
	ok := SweepBuildPoint{EfRuntime: 50}
	bad := SweepBuildPoint{EfRuntime: 10} // this exact point never reports OPERATIONAL below.

	var buildingBad bool
	fake := &fakeConn{
		queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
			if strings.HasPrefix(cypher, "CREATE VECTOR INDEX") {
				buildingBad = strings.Contains(cypher, "efRuntime:10")
			}
			if strings.Contains(cypher, "db.idx.vector.queryNodes") {
				return []row{{"id": "n1", "score": 0.1}}, nil
			}
			return nil, nil
		},
		indexesFunc: func(ctx context.Context, graphKey string) ([]indexStatus, error) {
			// Every build reports OPERATIONAL, EXCEPT the "bad" point, which
			// forces pollVectorIndexOperational to time out on it --
			// simulating the live rebuild-timeout failure mode without an
			// actual multi-second wait.
			if buildingBad {
				return nil, nil
			}
			return []indexStatus{{
				Label: labelSubject, Status: "OPERATIONAL",
				Types:   map[string][]string{propEmbedding: {"VECTOR"}},
				Options: map[string]interface{}{propEmbedding: map[string]interface{}{"dimension": int64(4)}},
			}}, nil
		},
	}
	adapter := newFakeAdapter(t, fake)
	adapter.config.RequestTimeout = 10 * time.Millisecond // fail the "bad" poll fast, not after 30s.

	var delivered []SweepBuildPoint
	results, err := adapter.RunHNSWSweep(context.Background(), "k", 4,
		[]string{"seed-a"}, 1, reference, []SweepBuildPoint{ok, bad},
		func(r SweepResult) { delivered = append(delivered, r.Point) })

	if err == nil {
		t.Fatal("expected the sweep to fail once it reaches a point whose index never reports OPERATIONAL")
	}
	if len(delivered) != 2 || delivered[0] != reference || delivered[1] != ok {
		t.Fatalf("onResult delivered %v, want [reference ok] before the failure", delivered)
	}
	if len(results) != 2 || results[0].Point != reference || results[1].Point != ok {
		t.Fatalf("results = %v, want the 2 completed points preserved despite the later failure", results)
	}
}

// A query that errors at a swept point must not corrupt that seed's
// contribution as either a hit or a miss -- it is excluded, and Queries
// reports how many seeds actually contributed.
func TestRunHNSWSweepExcludesFailedQueriesRatherThanScoringThem(t *testing.T) {
	reference := SweepBuildPoint{EfRuntime: 200}
	fake := &fakeConn{
		queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
			if strings.Contains(cypher, "db.idx.vector.queryNodes") {
				seed, _ := params["seed"].(string)
				if seed == "bad-seed" {
					return nil, errors.New("ERR simulated query failure")
				}
				return []row{{"id": "n1", "score": 0.1}}, nil
			}
			return nil, nil
		},
		indexesFunc: func(ctx context.Context, graphKey string) ([]indexStatus, error) {
			return []indexStatus{{
				Label: labelSubject, Status: "OPERATIONAL",
				Types:   map[string][]string{propEmbedding: {"VECTOR"}},
				Options: map[string]interface{}{propEmbedding: map[string]interface{}{"dimension": int64(4)}},
			}}, nil
		},
	}
	adapter := newFakeAdapter(t, fake)
	results, err := adapter.RunHNSWSweep(context.Background(), "k", 4,
		[]string{"good-seed", "bad-seed"}, 1, reference, nil, nil)
	if err != nil {
		t.Fatalf("RunHNSWSweep: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Queries != 1 {
		t.Fatalf("Queries = %d, want 1 (the failed seed excluded, not counted)", results[0].Queries)
	}
	if results[0].SkippedSeeds != 1 {
		t.Fatalf("SkippedSeeds = %d, want 1 -- partial coverage must be VISIBLE, not silently absorbed into a clean-looking recall number", results[0].SkippedSeeds)
	}
}

// Luna round-1 finding 1: a point where EVERY seed's query fails (e.g. a
// misconfigured dimension) must not report a green 0.0 recall -- that is
// indistinguishable from "measured, genuinely zero recall" unless the caller
// is forced to see the failure. RunHNSWSweep must both deliver the
// diagnostic result (Queries=0, SkippedSeeds=N) via onResult/results AND
// return a non-nil error.
func TestRunHNSWSweepFailsClosedOnZeroQueryCoverage(t *testing.T) {
	reference := SweepBuildPoint{EfRuntime: 200}
	fake := &fakeConn{
		queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
			if strings.Contains(cypher, "db.idx.vector.queryNodes") {
				return nil, errors.New("ERR simulated total query failure (e.g. wrong dimension)")
			}
			return nil, nil
		},
		indexesFunc: func(ctx context.Context, graphKey string) ([]indexStatus, error) {
			return []indexStatus{{
				Label: labelSubject, Status: "OPERATIONAL",
				Types:   map[string][]string{propEmbedding: {"VECTOR"}},
				Options: map[string]interface{}{propEmbedding: map[string]interface{}{"dimension": int64(4)}},
			}}, nil
		},
	}
	adapter := newFakeAdapter(t, fake)
	var delivered []SweepResult
	results, err := adapter.RunHNSWSweep(context.Background(), "k", 4,
		[]string{"seed-a", "seed-b"}, 2, reference, nil,
		func(r SweepResult) { delivered = append(delivered, r) })

	if err == nil {
		t.Fatal("a zero-query-coverage point must return a non-nil error, never a silent green pass")
	}
	if len(results) != 1 || results[0].Queries != 0 || results[0].SkippedSeeds != 2 {
		t.Fatalf("results = %#v, want exactly 1 diagnostic result with Queries=0 SkippedSeeds=2", results)
	}
	if len(delivered) != 1 || delivered[0].Queries != 0 {
		t.Fatalf("onResult must still receive the zero-coverage diagnostic result, got %#v", delivered)
	}
}

// Luna round-1 finding 3: two build points that both correctly rank a
// TIED-score group at the k-th boundary, but happen to include different
// members of that tie in their literal top-k rows, must not be scored as a
// recall miss -- they found equally-close neighbors, not different ones.
func TestRunHNSWSweepTieToleranceAtTheBoundaryDoesNotCountAsAMiss(t *testing.T) {
	reference := SweepBuildPoint{EfRuntime: 200}
	tiedPoint := SweepBuildPoint{EfRuntime: 10}

	var currentBuild SweepBuildPoint
	fake := &fakeConn{
		queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
			switch {
			case strings.HasPrefix(cypher, "CREATE VECTOR INDEX"):
				if strings.Contains(cypher, "efRuntime:200") {
					currentBuild = reference
				} else {
					currentBuild = tiedPoint
				}
			case strings.Contains(cypher, "db.idx.vector.queryNodes"):
				if currentBuild == reference {
					// k=2, overfetch=2x -> asked for 4; return 4 rows where
					// positions 2 and 3 (0-indexed) are EXACTLY TIED at 0.2 --
					// a genuine boundary tie group at k=2.
					return []row{
						{"id": "n1", "score": 0.1}, {"id": "n2", "score": 0.2},
						{"id": "n3", "score": 0.2}, {"id": "n4", "score": 0.4},
					}, nil
				}
				// The "tied" point's own top-2 picks the OTHER member of the
				// tie group than the reference's literal first two rows.
				return []row{{"id": "n1", "score": 0.1}, {"id": "n3", "score": 0.2}}, nil
			}
			return nil, nil
		},
		indexesFunc: func(ctx context.Context, graphKey string) ([]indexStatus, error) {
			return []indexStatus{{
				Label: labelSubject, Status: "OPERATIONAL",
				Types:   map[string][]string{propEmbedding: {"VECTOR"}},
				Options: map[string]interface{}{propEmbedding: map[string]interface{}{"dimension": int64(4)}},
			}}, nil
		},
	}
	adapter := newFakeAdapter(t, fake)
	results, err := adapter.RunHNSWSweep(context.Background(), "k", 4,
		[]string{"seed-a"}, 2, reference, []SweepBuildPoint{tiedPoint}, nil)
	if err != nil {
		t.Fatalf("RunHNSWSweep: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	// n3 is tied with n2 at 0.2 (the k=2 boundary score). A strict top-k
	// comparison would see {n1,n2} vs {n1,n3} and score 0.5 (only n1
	// matches); tie-tolerant recall must see both n2 and n3 as equally
	// "correct" at the boundary and score this 1.0.
	if results[1].RecallAtK != 1.0 {
		t.Fatalf("tie-tolerant RecallAtK = %v, want 1.0 -- a boundary tie swap must not read as a miss", results[1].RecallAtK)
	}
}

// Luna round-2 finding 3: a tie group larger than the initial 2x overfetch
// window must be chased further, not silently truncated. Here the tie group
// spans 6 entries at k=2 -- the initial 4-row window is STILL tied at its own
// edge, so the fetch must escalate to 8 rows, where the group finally ends.
func TestRunHNSWSweepReferenceTieCompletionEscalatesPastTheInitialWindow(t *testing.T) {
	reference := SweepBuildPoint{EfRuntime: 200}
	fullRanked := []row{
		{"id": "n1", "score": 0.1}, {"id": "n2", "score": 0.2}, {"id": "n3", "score": 0.2},
		{"id": "n4", "score": 0.2}, {"id": "n5", "score": 0.2}, {"id": "n6", "score": 0.2},
		{"id": "n7", "score": 0.5}, {"id": "n8", "score": 0.6},
	}
	var requestedKs []int
	fake := &fakeConn{
		queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
			if strings.Contains(cypher, "db.idx.vector.queryNodes") {
				reqK := extractFetchK(t, cypher)
				requestedKs = append(requestedKs, reqK)
				if reqK > len(fullRanked) {
					reqK = len(fullRanked)
				}
				return fullRanked[:reqK], nil
			}
			return nil, nil
		},
		indexesFunc: func(ctx context.Context, graphKey string) ([]indexStatus, error) {
			return []indexStatus{{
				Label: labelSubject, Status: "OPERATIONAL",
				Types:   map[string][]string{propEmbedding: {"VECTOR"}},
				Options: map[string]interface{}{propEmbedding: map[string]interface{}{"dimension": int64(4)}},
			}}, nil
		},
	}
	adapter := newFakeAdapter(t, fake)
	// No other points -- this test is only about the reference build's own
	// tie-completion fetch escalating past referenceOverfetch.
	results, err := adapter.RunHNSWSweep(context.Background(), "k", 4,
		[]string{"seed-a"}, 2, reference, nil, nil)
	if err != nil {
		t.Fatalf("RunHNSWSweep: %v", err)
	}
	if len(results) != 1 || results[0].Queries != 1 {
		t.Fatalf("expected the reference build to succeed with 1 contributing seed, got %#v", results)
	}
	if len(requestedKs) < 2 || requestedKs[0] != 4 || requestedKs[1] != 8 {
		t.Fatalf("expected the fetch to escalate from k*2=4 to k*4=8 when the window's own edge was still tied, got requested Ks %v", requestedKs)
	}
}

// Luna round-2 finding 3's hard bound: a tie group that NEVER resolves
// (every fetched row, at every escalation, remains tied at the boundary)
// must fail closed for that seed once maxReferenceTieMultiplier is reached
// -- never silently compared against a truncated tie class. Being the only
// seed, this also exercises round-1 finding 1's zero-coverage fail-closed
// check end to end.
func TestRunHNSWSweepReferenceTieCompletionFailsClosedWhenUnbounded(t *testing.T) {
	reference := SweepBuildPoint{EfRuntime: 200}
	var lastRequestedK int
	fake := &fakeConn{
		queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
			if strings.Contains(cypher, "db.idx.vector.queryNodes") {
				reqK := extractFetchK(t, cypher)
				lastRequestedK = reqK
				// A pathologically huge, perfectly-tied cluster: every row
				// at every fetch size comes back at the SAME score, so the
				// window's edge never resolves as strictly worse than the
				// boundary, however large the request grows.
				rows := make([]row, reqK)
				for i := range rows {
					rows[i] = row{"id": "tied", "score": 0.2}
				}
				return rows, nil
			}
			return nil, nil
		},
		indexesFunc: func(ctx context.Context, graphKey string) ([]indexStatus, error) {
			return []indexStatus{{
				Label: labelSubject, Status: "OPERATIONAL",
				Types:   map[string][]string{propEmbedding: {"VECTOR"}},
				Options: map[string]interface{}{propEmbedding: map[string]interface{}{"dimension": int64(4)}},
			}}, nil
		},
	}
	adapter := newFakeAdapter(t, fake)
	results, err := adapter.RunHNSWSweep(context.Background(), "k", 4,
		[]string{"seed-a"}, 2, reference, nil, nil)
	if err == nil {
		t.Fatal("an unresolved reference tie group must fail closed (via the zero-coverage check), not report a silent measurement")
	}
	if len(results) != 1 || results[0].Queries != 0 || results[0].SkippedSeeds != 1 {
		t.Fatalf("results = %#v, want the reference point's diagnostic result with the tied seed skipped", results)
	}
	wantMaxK := 2 * maxReferenceTieMultiplier
	if lastRequestedK != wantMaxK {
		t.Fatalf("last requested k = %d, want the bounded ceiling %d (k=2 * maxReferenceTieMultiplier)", lastRequestedK, wantMaxK)
	}
}
