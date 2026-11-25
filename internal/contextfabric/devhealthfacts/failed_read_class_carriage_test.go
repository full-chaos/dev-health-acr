package devhealthfacts

import (
	"errors"
	"fmt"
	"testing"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// The registry serves a failed read by Class and SourceCode, so a provider
// failure carries both.
func TestAFailedReadCarriesItsClassAndCode(t *testing.T) {
	t.Parallel()
	exception := func(code int32) error {
		return fmt.Errorf("query: %w", &clickhousedriver.Exception{Code: code, Name: "DB::Exception", Message: "text that never leaves the log"})
	}
	cases := []struct {
		name      string
		err       error
		wantClass contextfabric.FactReadFailureClass
		wantCode  int32
	}{
		{"timeout 159", readFailure("query things", exception(159)), contextfabric.FactReadFailureReadFailed, 159},
		{"bytes 307", readFailure("query things", exception(307)), contextfabric.FactReadFailureReadFailed, 307},
		{"result rows 396", readFailure("query things", exception(396)), contextfabric.FactReadFailureReadFailed, 396},
		{"memory 241", readFailure("query things", exception(241)), contextfabric.FactReadFailureReadFailed, 241},
		{"budget 307 measured", readFailure("query things", &BudgetExceededError{Code: 307}), contextfabric.FactReadFailureBudgetExceeded, 0},
		{"budget 158 measured", readFailure("query things", &BudgetExceededError{Code: 158}), contextfabric.FactReadFailureBudgetExceeded, 0},
		{"contended", mixReadFailure("query things", &ProjectMixContendedError{}), contextfabric.FactReadFailureContended, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var failure *contextfabric.FactReadFailure
			if !errors.As(tc.err, &failure) {
				t.Fatalf("%v is not a FactReadFailure", tc.err)
			}
			if failure.Class != tc.wantClass || failure.SourceCode != tc.wantCode {
				t.Fatalf("class %q code %d, want %q %d", failure.Class, failure.SourceCode, tc.wantClass, tc.wantCode)
			}
			if failure.State != contextfabric.SourceUnavailable {
				t.Fatalf("state %q", failure.State)
			}
		})
	}
}
