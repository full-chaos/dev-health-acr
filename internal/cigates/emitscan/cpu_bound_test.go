package emitscan_test

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/cigates/emitscan"
)

const (
	// calibrationBaseline is the calibration scan's CPU in a plain build (45-50 ms measured on the 64-core dev host).
	calibrationBaseline = 45 * time.Millisecond
	// maxSlowdown is about 3x the largest slowdown measured (race with atomic coverage, about 51x).
	maxSlowdown      = 160
	calibrationRuns  = 3
	calibrationRules = "R1"
)

// calibrationSource is linear in size and takes the scan path of the costliest plant: calls through a many-constructor alias.
var calibrationSource = header + "var a = slog.String\n" + repeat(300, "var a = slog.F%d\n", idx) +
	"func f(v string) {\n" + repeat(2000, "\t_ = a(\"x\", v)\n") + "}\n" +
	"func g(v string) { _ = attribute.String(\"api_token\", v) }\n"

var calibration struct {
	once sync.Once
	cpu  time.Duration
	err  error
}

// calibrationCPU is the least CPU of the calibration scans, measured once per test binary.
func calibrationCPU(t *testing.T) time.Duration {
	t.Helper()
	calibration.once.Do(func() { calibration.cpu, calibration.err = measureCalibration(t) })
	if calibration.err != nil {
		t.Fatalf("calibration scan: %v", calibration.err)
	}
	return calibration.cpu
}

func measureCalibration(t *testing.T) (time.Duration, error) {
	best := time.Duration(-1)
	for i := 0; i < calibrationRuns; i++ {
		root := t.TempDir()
		dir := filepath.Join(root, "internal", "sample")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return 0, err
		}
		if err := os.WriteFile(filepath.Join(dir, "sample.go"), []byte(calibrationSource), 0o600); err != nil {
			return 0, err
		}
		start := cpuTime(t)
		findings, err := emitscan.Scan(root, []string{"internal"})
		used := cpuTime(t) - start
		if err != nil {
			return 0, err
		}
		if got := rules(findings); got != calibrationRules {
			return 0, fmt.Errorf("rules = %q, want %q: the workload was not scanned as written", got, calibrationRules)
		}
		if best < 0 || used < best {
			best = used
		}
	}
	return best, nil
}

// cpuBound is the CPU a plant with the given plain-build limit may use in this test binary, and how it was set.
func cpuBound(limit, calibration time.Duration) (time.Duration, string, error) {
	if calibration <= 0 {
		return 0, "", fmt.Errorf("calibration scan used %s cpu: it measured no work", calibration)
	}
	slowdown := float64(calibration) / float64(calibrationBaseline)
	if slowdown > maxSlowdown {
		return 0, "", fmt.Errorf("calibration scan used %s cpu, %.1fx the %s plain-build baseline, over the %dx ceiling", calibration, slowdown, calibrationBaseline, maxSlowdown)
	}
	slowdown = max(slowdown, 1)
	note := fmt.Sprintf("limit %s x %.2f: calibration scan %s cpu, plain-build baseline %s", limit, slowdown, calibration, calibrationBaseline)
	return time.Duration(float64(limit) * slowdown), note, nil
}

// cpuLimit is cpuBound with this binary's calibration; take it before the measured work starts.
func cpuLimit(t *testing.T, limit time.Duration) (time.Duration, string) {
	t.Helper()
	bound, note, err := cpuBound(limit, calibrationCPU(t))
	if err != nil {
		t.Fatal(err)
	}
	return bound, note
}

// A linear plant passes under every instrumentation, a quadratic one fails, and a broken calibration is an error.
func TestCPUBoundFollowsTheCalibrationSlowdown(t *testing.T) {
	rows := []struct {
		name        string
		calibration time.Duration
		elapsed     time.Duration
		wantPass    bool
		wantErr     bool
	}{
		{"plain, linear plant", 46 * time.Millisecond, 410 * time.Millisecond, true, false},
		{"race, linear plant", 337 * time.Millisecond, 2710 * time.Millisecond, true, false},
		{"race with atomic coverage, linear plant", 2346 * time.Millisecond, 16950 * time.Millisecond, true, false},
		{"plain, quadratic plant", 46 * time.Millisecond, 100 * 410 * time.Millisecond, false, false},
		{"race, quadratic plant", 337 * time.Millisecond, 100 * 2710 * time.Millisecond, false, false},
		{"race with atomic coverage, quadratic plant", 2346 * time.Millisecond, 100 * 16950 * time.Millisecond, false, false},
		{"host faster than the baseline keeps the plain limit", 20 * time.Millisecond, 1500 * time.Millisecond, false, false},
		{"zero calibration", 0, 0, false, true},
		{"negative calibration", -time.Millisecond, 0, false, true},
		{"calibration over the ceiling", 10 * time.Second, time.Second, false, true},
	}
	for _, row := range rows {
		bound, _, err := cpuBound(time.Second, row.calibration)
		if (err != nil) != row.wantErr {
			t.Errorf("%s: cpuBound(1s, %s) error = %v, want error %v", row.name, row.calibration, err, row.wantErr)
			continue
		}
		if err != nil {
			continue
		}
		if pass := row.elapsed < bound; pass != row.wantPass {
			t.Errorf("%s: %s cpu against bound %s (calibration %s): pass = %v, want %v", row.name, row.elapsed, bound, row.calibration, pass, row.wantPass)
		}
	}
}

// timed must use this binary's calibration: one positive value per binary, and a race build must measure a slowdown.
func TestTimedUsesThisBinarysCalibration(t *testing.T) {
	first, second := calibrationCPU(t), calibrationCPU(t)
	if first <= 0 || first != second {
		t.Fatalf("calibration = %s then %s, want one positive value per test binary", first, second)
	}
	bound, note := cpuLimit(t, time.Second)
	t.Logf("calibration %s cpu; bound for a 1s limit: %s (%s)", first, bound, note)
	if builtWithRace && bound <= time.Second {
		t.Errorf("race build: bound %s for a 1s limit (%s), want the race slowdown measured", bound, note)
	}
}
