package devhealthfacts_test

import (
	"math"
	"testing"
)

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
// smaller size's min that slopeGrew reports, for these readings.
func minDetectableGrowth(smallMin, smallSpread, floor uint64, minAgainstMax bool) uint64 {
	base := smallMin
	if minAgainstMax {
		base = smallMin + smallSpread
	}
	need := floor + 1
	if byRatio := uint64(math.Floor((statementMemorySlope-1)*float64(base))) + 1; byRatio > need {
		need = byRatio
	}
	return base - smallMin + need
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
	// Every other statement keeps min against min.
	if !slopeGrew(smallMin, smallSpread, largeMin, floor, noisySlopeStatements["projectLinkedRepositories"]) {
		t.Error("projectLinkedRepositories lost the min-against-min guard")
	}
}

// A name that is not a scope expander statement would silently keep min
// against min for the statement it meant; a precise-spread statement must not
// lose its sensitivity.
func TestNoisySlopeStatementsNameRealNoisyStatements(t *testing.T) {
	labels := map[string]bool{}
	for _, statement := range scopeExpanderStatements {
		labels[statement.label] = true
	}
	for name := range noisySlopeStatements {
		if !labels[name] {
			t.Errorf("noisySlopeStatements names %q, which is not a scope expander statement", name)
		}
		if preciseSpreadStatements[name] {
			t.Errorf("%q is both a precise-spread statement and a min-against-max statement", name)
		}
	}
}
