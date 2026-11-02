package devhealthfacts

// CHAOS-12607: the differential oracle for the completion distribution.
//
// completionBins is a COPY of ops' binsToModel (internal/queryapi/
// capacityforecast/distribution.go), because an Ask Dev chart of a forecast
// must draw the same curve ops' /plan/capacity page draws. This test is the
// only thing that says the copy is still right: it writes cases, copies a
// harness test into an ops checkout's capacityforecast package, EXECUTES ops'
// own binsToModel there, and compares every served bin field with ACR's,
// float bits included. The ops checkout is pinned (ci/forecast-oracle-ops.pin)
// so the comparison is with a known ops commit.
//
// The second subtest is the acceptance gate: defects in the horizon rule and
// the share that the oracle must keep finding. If one stops being found, the
// comparison has been weakened, whatever the first subtest says.
//
// Run: ACR_FORECAST_ORACLE_OPS_DIR=<ops checkout> go test -count=1 \
//   -run TestCompletionBinsOracleAgainstOps ./internal/contextfabric/devhealthfacts/
// CI sets ACR_FORECAST_ORACLE_REQUIRED=1, which turns a missing checkout into
// a failure instead of a skip. The harness file is removed after each run.

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"testing"
)

const (
	forecastOracleOpsDirEnv   = "ACR_FORECAST_ORACLE_OPS_DIR"
	forecastOracleRequiredEnv = "ACR_FORECAST_ORACLE_REQUIRED"
	forecastOracleHarnessName = "zz_acr_forecast_oracle_test.go"
	forecastOracleRandomCases = 300
)

type forecastOracleCase struct {
	Name   string   `json:"name"`
	Values []string `json:"values"`
	Counts []string `json:"counts"`
}

type forecastOracleLeaf struct {
	T string `json:"t"`
	V string `json:"v"`
}

type forecastOracleResult struct {
	Name       string                          `json:"name"`
	Nil        bool                            `json:"nil"`
	Bins       []map[string]forecastOracleLeaf `json:"bins"`
	Unfinished forecastOracleLeaf              `json:"unfinished"`
	Horizon    forecastOracleLeaf              `json:"horizon"`
}

type completionBinsFunc func(values []uint16, counts []uint32, horizon int) ([]completionBin, int)

func oracleStrings[T uint16 | uint32](values []T) []string {
	if values == nil {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, strconv.FormatUint(uint64(value), 10))
	}
	return out
}

func oracleCase(name string, values []uint16, counts []uint32) forecastOracleCase {
	return forecastOracleCase{Name: name, Values: oracleStrings(values), Counts: oracleStrings(counts)}
}

// forecastOracleCases are the edge cases of the horizon rule and the share,
// the largest real distribution measured on askdev-1, and seeded random
// distributions, some with arrays of different lengths.
func forecastOracleCases(t *testing.T) []forecastOracleCase {
	t.Helper()
	cases := []forecastOracleCase{
		oracleCase("both nil", nil, nil),
		oracleCase("both empty", []uint16{}, []uint32{}),
		oracleCase("values only", []uint16{5}, []uint32{}),
		oracleCase("counts only", []uint16{}, []uint32{5}),
		oracleCase("one bin below the horizon", []uint16{5}, []uint32{10000}),
		oracleCase("one bin at the horizon", []uint16{365}, []uint32{10000}),
		oracleCase("one bin past the horizon", []uint16{400}, []uint32{10}),
		oracleCase("the day before the horizon and the horizon", []uint16{364, 365}, []uint32{1, 1}),
		oracleCase("every count zero", []uint16{1, 2}, []uint32{0, 0}),
		oracleCase("some counts zero", []uint16{1, 2, 3}, []uint32{0, 5, 0}),
		oracleCase("more values than counts", []uint16{1, 2, 3}, []uint32{4, 5}),
		oracleCase("more counts than values", []uint16{1, 2}, []uint32{4, 5, 6}),
		oracleCase("the largest day value", []uint16{65535}, []uint32{1}),
		oracleCase("the largest count", []uint16{1, 2}, []uint32{math.MaxUint32, 1}),
		oracleCase("a horizon bin among finished bins", []uint16{5, 12, 365}, []uint32{100, 8000, 1900}),
	}
	var fixture struct {
		Values []string `json:"values"`
		Counts []string `json:"counts"`
	}
	raw, err := os.ReadFile(filepath.Join("testdata", "forecast_oracle", "askdev1_359_bins.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	cases = append(cases, forecastOracleCase{Name: "askdev-1 359 bins", Values: fixture.Values, Counts: fixture.Counts})

	random := rand.New(rand.NewPCG(12607, 4613))
	for index := 0; index < forecastOracleRandomCases; index++ {
		n := 1 + random.IntN(366)
		seen := map[uint16]bool{}
		for len(seen) < n {
			seen[uint16(random.IntN(400))] = true
		}
		values := make([]uint16, 0, n)
		for value := range seen {
			values = append(values, value)
		}
		sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
		counts := make([]uint32, 0, n)
		for range values {
			if random.IntN(10) == 0 {
				counts = append(counts, 0)
			} else {
				counts = append(counts, uint32(1+random.IntN(1000)))
			}
		}
		switch random.IntN(8) {
		case 0:
			values = values[:random.IntN(len(values)+1)]
		case 1:
			counts = counts[:random.IntN(len(counts)+1)]
		}
		cases = append(cases, oracleCase(fmt.Sprintf("random %d", index), values, counts))
	}
	return cases
}

func parseOracleUints[T uint16 | uint32](t *testing.T, raw []string, bits int) []T {
	t.Helper()
	if raw == nil {
		return nil
	}
	out := make([]T, 0, len(raw))
	for _, text := range raw {
		value, err := strconv.ParseUint(text, 10, bits)
		if err != nil {
			t.Fatalf("case value %q: %v", text, err)
		}
		out = append(out, T(value))
	}
	return out
}

// runOpsForecastOracle executes ops' binsToModel on cases and returns its
// results. It fails, loudly, if the harness did not run or did not answer
// every case: a comparison with nothing is not a pass.
func runOpsForecastOracle(t *testing.T, cases []forecastOracleCase) []forecastOracleResult {
	t.Helper()
	opsDir := os.Getenv(forecastOracleOpsDirEnv)
	if opsDir == "" {
		if os.Getenv(forecastOracleRequiredEnv) == "1" {
			t.Fatalf("%s is required when %s=1", forecastOracleOpsDirEnv, forecastOracleRequiredEnv)
		}
		t.Skipf("%s names no ops checkout: the forecast oracle did not run", forecastOracleOpsDirEnv)
	}
	packageDir := filepath.Join(opsDir, "internal", "queryapi", "capacityforecast")
	if _, err := os.Stat(filepath.Join(packageDir, "distribution.go")); err != nil {
		t.Fatalf("%s is not an ops checkout with capacityforecast/distribution.go: %v", opsDir, err)
	}
	harness, err := os.ReadFile(filepath.Join("testdata", "forecast_oracle", "ops_harness_test.go.txt"))
	if err != nil {
		t.Fatal(err)
	}
	harnessPath := filepath.Join(packageDir, forecastOracleHarnessName)
	if err := os.WriteFile(harnessPath, harness, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(harnessPath) })

	work := t.TempDir()
	casesPath, outPath := filepath.Join(work, "cases.json"), filepath.Join(work, "out.json")
	encoded, err := json.Marshal(cases)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(casesPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("go", "test", "-count=1", "-run", "^TestACRForecastOracle$", "./internal/queryapi/capacityforecast/")
	command.Dir = opsDir
	command.Env = append(os.Environ(), "ACR_FORECAST_ORACLE_CASES="+casesPath, "ACR_FORECAST_ORACLE_OUT="+outPath)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("the ops harness failed: %v\n%s", err, output)
	}
	raw, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("the ops harness wrote no results, so nothing was compared: %v\n%s", err, output)
	}
	var results []forecastOracleResult
	if err := json.Unmarshal(raw, &results); err != nil {
		t.Fatal(err)
	}
	if len(results) != len(cases) {
		t.Fatalf("the ops harness answered %d of %d cases", len(results), len(cases))
	}
	return results
}

// forecastOracleMismatches compares fn with ops case by case and returns
// every disagreement.
func forecastOracleMismatches(t *testing.T, cases []forecastOracleCase, ops []forecastOracleResult, fn completionBinsFunc) (mismatches []string) {
	t.Helper()
	known := map[string]bool{"Value": true, "Count": true, "CumulativeShare": true}
	for index, c := range cases {
		want := ops[index]
		if want.Name != c.Name {
			t.Fatalf("result %d is %q, want %q", index, want.Name, c.Name)
		}
		if want.Horizon.V != strconv.Itoa(forecastHorizonDays) {
			mismatches = append(mismatches, fmt.Sprintf("ops horizon %s, ACR %d", want.Horizon.V, forecastHorizonDays))
			continue
		}
		bins, unfinished := fn(parseOracleUints[uint16](t, c.Values, 16), parseOracleUints[uint32](t, c.Counts, 32), forecastHorizonDays)
		if (bins == nil) != want.Nil {
			mismatches = append(mismatches, fmt.Sprintf("%s: ACR nil=%t, ops nil=%t", c.Name, bins == nil, want.Nil))
			continue
		}
		if strconv.Itoa(unfinished) != want.Unfinished.V {
			mismatches = append(mismatches, fmt.Sprintf("%s: unfinished ACR %d, ops %s", c.Name, unfinished, want.Unfinished.V))
		}
		if len(bins) != len(want.Bins) {
			mismatches = append(mismatches, fmt.Sprintf("%s: ACR %d bins, ops %d", c.Name, len(bins), len(want.Bins)))
			continue
		}
		for b, bin := range bins {
			opsBin := want.Bins[b]
			for field := range opsBin {
				if !known[field] {
					mismatches = append(mismatches, fmt.Sprintf("%s: ops serves bin field %q that the ACR copy does not compute", c.Name, field))
				}
			}
			got := map[string]string{
				"Value":           strconv.Itoa(bin.Value),
				"Count":           strconv.Itoa(bin.Count),
				"CumulativeShare": strconv.FormatUint(math.Float64bits(bin.CumulativeShare), 16),
			}
			for field, value := range got {
				if opsBin[field].V != value {
					mismatches = append(mismatches, fmt.Sprintf("%s bin %d %s: ACR %s, ops %s", c.Name, b, field, value, opsBin[field].V))
				}
			}
		}
	}
	return mismatches
}

func TestCompletionBinsOracleAgainstOps(t *testing.T) {
	cases := forecastOracleCases(t)
	ops := runOpsForecastOracle(t, cases)

	t.Run("the ACR copy agrees with ops on every case", func(t *testing.T) {
		mismatches := forecastOracleMismatches(t, cases, ops, completionBins)
		for _, mismatch := range mismatches {
			t.Error(mismatch)
		}
		t.Logf("compared %d cases with ops", len(cases))
	})

	t.Run("the oracle finds every planted defect", func(t *testing.T) {
		for name, plant := range forecastOraclePlants() {
			if mismatches := forecastOracleMismatches(t, cases, ops, plant); len(mismatches) == 0 {
				t.Errorf("plant %q SURVIVED: the oracle no longer finds it", name)
			} else {
				t.Logf("plant %q KILLED: %s", name, mismatches[0])
			}
		}
	})
}

// forecastOraclePlants are completionBins with one defect each. They live in
// the test, not the product: the gate that keeps the oracle honest.
func forecastOraclePlants() map[string]completionBinsFunc {
	return map[string]completionBinsFunc{
		"a run at the horizon counted as done (> for >=)": func(values []uint16, counts []uint32, horizon int) ([]completionBin, int) {
			return plantedCompletionBins(values, counts, horizon, plantOptions{strictHorizon: true})
		},
		"the total taken over finished runs only": func(values []uint16, counts []uint32, horizon int) ([]completionBin, int) {
			return plantedCompletionBins(values, counts, horizon, plantOptions{totalFinishedOnly: true})
		},
		"the horizon bin dropped": func(values []uint16, counts []uint32, horizon int) ([]completionBin, int) {
			return plantedCompletionBins(values, counts, horizon, plantOptions{dropHorizonBins: true})
		},
		"arrays of different lengths not cut to the shorter": func(values []uint16, counts []uint32, horizon int) ([]completionBin, int) {
			return plantedCompletionBins(values, counts, horizon, plantOptions{noCut: true})
		},
		"the share taken before the bin's own runs": func(values []uint16, counts []uint32, horizon int) ([]completionBin, int) {
			return plantedCompletionBins(values, counts, horizon, plantOptions{shareBeforeBin: true})
		},
		"one empty array read as a distribution": func(values []uint16, counts []uint32, horizon int) ([]completionBin, int) {
			return plantedCompletionBins(values, counts, horizon, plantOptions{nilOnlyWhenBothEmpty: true})
		},
	}
}

type plantOptions struct {
	strictHorizon, totalFinishedOnly, dropHorizonBins, noCut, shareBeforeBin, nilOnlyWhenBothEmpty bool
}

func plantedCompletionBins(values []uint16, counts []uint32, horizon int, plant plantOptions) (bins []completionBin, unfinished int) {
	if plant.nilOnlyWhenBothEmpty {
		if len(values) == 0 && len(counts) == 0 {
			return nil, 0
		}
	} else if len(values) == 0 || len(counts) == 0 {
		return nil, 0
	}
	length := min(len(values), len(counts))
	if plant.noCut {
		length = max(len(values), len(counts))
	}
	at := func(index int) (int, int) {
		value, count := 0, 0
		if index < len(values) {
			value = int(values[index])
		}
		if index < len(counts) {
			count = int(counts[index])
		}
		return value, count
	}
	unfinishedAt := func(value int) bool {
		if plant.strictHorizon {
			return horizon > 0 && value > horizon
		}
		return horizon > 0 && value >= horizon
	}
	total := 0
	for index := 0; index < length; index++ {
		value, count := at(index)
		if plant.totalFinishedOnly && unfinishedAt(value) {
			continue
		}
		total += count
	}
	bins = make([]completionBin, 0, length)
	finished := 0
	for index := 0; index < length; index++ {
		value, count := at(index)
		before := finished
		if unfinishedAt(value) {
			unfinished += count
		} else {
			finished += count
		}
		if plant.dropHorizonBins && unfinishedAt(value) {
			continue
		}
		share := completionShare(finished, total)
		if plant.shareBeforeBin {
			share = completionShare(before, total)
		}
		bins = append(bins, completionBin{Value: value, Count: count, CumulativeShare: share})
	}
	return bins, unfinished
}
