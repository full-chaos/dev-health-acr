package devhealthfacts_test

import (
	"fmt"
	"testing"
)

// diagPeak is one diagnostic statement's query_log reading.
type diagPeak struct {
	matched, lowest, highest, readBytes uint64
	events                              string
}

// diagReadPeak scans one query_log aggregate row (matched rows first) and
// refuses a reading no row matched: a zero table would read as a measurement.
func diagReadPeak(scan func(dest ...any) error, marker string) (diagPeak, error) {
	var p diagPeak
	if err := scan(&p.matched, &p.lowest, &p.highest, &p.readBytes, &p.events); err != nil {
		return p, fmt.Errorf("%s: read query_log: %w", marker, err)
	}
	if p.matched == 0 {
		return p, fmt.Errorf("%s: no query_log row matched the statement marker: the measurement did not happen", marker)
	}
	return p, nil
}

func TestDiagReadPeakRefusesAMeasurementWithNoMatchingRow(t *testing.T) {
	fake := func(matched uint64) func(dest ...any) error {
		return func(dest ...any) error {
			*dest[0].(*uint64) = matched
			return nil
		}
	}
	if _, err := diagReadPeak(fake(0), "diag-planted"); err == nil {
		t.Fatal("a reading with no matching query_log row was accepted")
	}
	if _, err := diagReadPeak(fake(3), "diag-planted"); err != nil {
		t.Fatalf("a reading with matching rows was refused: %v", err)
	}
}
