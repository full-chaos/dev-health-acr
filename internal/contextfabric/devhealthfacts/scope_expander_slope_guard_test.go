package devhealthfacts_test

import "testing"

// noisySlopeStatements are the scope expander statements whose three warm runs
// still move by MBs under precise memory accounting (CHAOS-12317): they run at
// the server's default max_threads, so their peak follows thread scheduling. A
// min-of-three reading at the smaller size can then catch one low run and make
// the larger size look grown (acr #1827, run 41221131667: historicalDropCount
// small 20,920,872 with a 7,423,274 spread against large 29,849,674 with a
// 485,375 spread). For these statements only, the slope guard compares the
// larger size's MIN with the smaller size's MAX.
//
// The cost: the guard cannot see a growth smaller than the smaller size's
// observed spread (plus the 1.15x rule or the floor). The PEAK line prints that
// minimum detectable growth for every run, so the blind spot is visible.
var noisySlopeStatements = map[string]bool{"historicalDropCount": true, "historicalTouchCount": true, "projectRepositoriesAsOf": true}

// slopeGrew is the scope expander slope guard: the larger size's reading
// (smallest of three warm runs) over 1.15x the smaller size's base AND over it
// by more than the floor. The base is the smaller size's min, or its max (min +
// spread) for a noisySlopeStatements row.
func slopeGrew(smallMin, smallSpread, largeMin, floor uint64, minAgainstMax bool) bool {
	base := smallMin
	if minAgainstMax {
		base = smallMin + smallSpread
	}
	return float64(largeMin) > statementMemorySlope*float64(base) && largeMin > base+floor
}

// minDetectableGrowth is the smallest rise of the larger size's min over the
// smaller size's min that slopeGrew reports, for these readings. It searches
// slopeGrew itself (monotonic in largeMin), so it is the guard's exact
// boundary, float rounding included, not a closed form that can drift from it.
func minDetectableGrowth(smallMin, smallSpread, floor uint64, minAgainstMax bool) uint64 {
	lo, hi := smallMin, smallMin+2*(smallMin+smallSpread)+floor+2
	for lo < hi {
		mid := lo + (hi-lo)/2
		if slopeGrew(smallMin, smallSpread, mid, floor, minAgainstMax) {
			hi = mid
		} else {
			lo = mid + 1
		}
	}
	return lo - smallMin
}

func TestScopeExpanderSlopeGuardOnRun41221131667(t *testing.T) {
	const (
		smallMin, smallSpread, largeMin = 20_920_872, 7_423_274, 29_849_674
		orgSmallMin, orgSmallSpread     = 187_927_064, 73_744
		orgLargeMin                     = 268_323_866
		floor                           = uint64(8 << 20)
	)
	if !noisySlopeStatements["historicalDropCount"] {
		t.Fatal("historicalDropCount is not a min-against-max statement")
	}
	// The false failure: min against min reads one low smaller-size run as growth.
	if !slopeGrew(smallMin, smallSpread, largeMin, floor, false) {
		t.Fatal("min against min no longer flags run 41221131667's numbers: the red case is gone, so this test proves nothing")
	}
	if slopeGrew(smallMin, smallSpread, largeMin, floor, noisySlopeStatements["historicalDropCount"]) {
		t.Errorf("historicalDropCount: run 41221131667's scoped readings (small %d, spread %d; large %d) still read as growth", smallMin, smallSpread, largeMin)
	}
	// The dropped scope filter (the organization-wide form of the same run) is
	// still caught: a real regression lifts every run.
	if !slopeGrew(orgSmallMin, orgSmallSpread, orgLargeMin, floor, true) {
		t.Errorf("min against max misses the organization-wide form of run 41221131667 (small %d, spread %d; large %d)", orgSmallMin, orgSmallSpread, orgLargeMin)
	}
	// Growth just at and just past the printed minimum detectable growth.
	detect := minDetectableGrowth(smallMin, smallSpread, floor, true)
	if slopeGrew(smallMin, smallSpread, smallMin+detect-1, floor, true) || !slopeGrew(smallMin, smallSpread, smallMin+detect, floor, true) {
		t.Errorf("minDetectableGrowth %d is not the boundary of slopeGrew", detect)
	}
	if detect <= smallSpread {
		t.Errorf("minDetectableGrowth %d is not above the smaller size's spread %d: the stated cost is wrong", detect, smallSpread)
	}
	// The printed number is the boundary for every floor in use, both guards,
	// and a grid of readings across the sizes this test measures.
	floors := []uint64{arrayMemoryFloor}
	for _, own := range slopeFloors {
		floors = append(floors, own)
	}
	for _, f := range floors {
		for small := uint64(1 << 20); small < 400<<20; small += 104729 {
			for _, spread := range []uint64{0, 160, 485_375, smallSpread} {
				for _, minAgainstMax := range []bool{false, true} {
					d := minDetectableGrowth(small, spread, f, minAgainstMax)
					if slopeGrew(small, spread, small+d-1, f, minAgainstMax) || !slopeGrew(small, spread, small+d, f, minAgainstMax) {
						t.Fatalf("minDetectableGrowth(%d, %d, %d, %v) = %d is not the boundary of slopeGrew", small, spread, f, minAgainstMax, d)
					}
				}
			}
		}
	}
	// Every other statement keeps min against min.
	if !slopeGrew(smallMin, smallSpread, largeMin, floor, noisySlopeStatements["projectLinkedRepositories"]) {
		t.Error("projectLinkedRepositories lost the min-against-min guard")
	}
}

// The set is exactly the three reviewed statements: a dropped name silently
// returns that statement to the false failure, an added name silently lowers
// that statement's sensitivity. Each must be a real scope expander statement
// that runs multithreaded (the reason for the set), and none may be a
// precise-spread statement.
func TestNoisySlopeStatementsNameRealNoisyStatements(t *testing.T) {
	want := []string{"historicalDropCount", "historicalTouchCount", "projectRepositoriesAsOf"}
	if len(noisySlopeStatements) != len(want) {
		t.Errorf("noisySlopeStatements has %d names, want exactly %v", len(noisySlopeStatements), want)
	}
	for _, name := range want {
		if !noisySlopeStatements[name] {
			t.Errorf("noisySlopeStatements does not name %q", name)
		}
	}
	threadBound := map[string]bool{}
	for _, statement := range scopeExpanderStatements {
		threadBound[statement.label] = statement.threadBound
	}
	for name := range noisySlopeStatements {
		bound, found := threadBound[name]
		if !found {
			t.Errorf("noisySlopeStatements names %q, which is not a scope expander statement", name)
			continue
		}
		if bound {
			t.Errorf("%q runs thread-bound, so its spread is not multithread variance: it must keep min against min", name)
		}
		if preciseSpreadStatements[name] {
			t.Errorf("%q is both a precise-spread statement and a min-against-max statement", name)
		}
	}
}
