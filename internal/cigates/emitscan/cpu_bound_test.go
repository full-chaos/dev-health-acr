package emitscan_test

import (
	"testing"
	"time"
)

// cpuBound is the CPU a plant with the given plain-build limit may use in this test binary.
func cpuBound(limit, calibration time.Duration) (time.Duration, error) {
	return limit * time.Duration(timedScale), nil
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
		bound, err := cpuBound(time.Second, row.calibration)
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
