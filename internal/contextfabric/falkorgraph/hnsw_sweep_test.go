package falkorgraph

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

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
					// The reference build always finds the true 2 neighbors.
					if seed == "seed-a" {
						return []row{{"id": "n1"}, {"id": "n2"}}, nil
					}
					return []row{{"id": "n3"}, {"id": "n4"}}, nil
				}
				// The degraded build misses one true neighbor per seed.
				if seed == "seed-a" {
					return []row{{"id": "n1"}, {"id": "nX"}}, nil
				}
				return []row{{"id": "nY"}, {"id": "n4"}}, nil
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
				return []row{{"id": "n1"}}, nil
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
				return []row{{"id": "n1"}}, nil
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
}
