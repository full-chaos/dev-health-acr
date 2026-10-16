package devhealthfacts

import (
	"errors"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

func TestEveryDevhealthfactsFailureSetsItsClass(t *testing.T) {
	t.Parallel()
	classOf := func(err error) contextfabric.FactReadFailureClass {
		t.Helper()
		var failure *contextfabric.FactReadFailure
		if !errors.As(err, &failure) {
			t.Fatalf("%v is not a FactReadFailure", err)
		}
		return failure.Class
	}
	if got := classOf(readFailure("query things", errors.New("boom"))); got != contextfabric.FactReadFailureReadFailed {
		t.Errorf("read failure class = %q", got)
	}
	if got := classOf(readFailure("query things", &BudgetExceededError{})); got != contextfabric.FactReadFailureBudgetExceeded {
		t.Errorf("budget class = %q", got)
	}
	if got := classOf(mixReadFailure("query things", &ProjectMixContendedError{})); got != contextfabric.FactReadFailureContended {
		t.Errorf("contended class = %q", got)
	}
	_, err := requireOrgID(" ")
	if got := classOf(err); got != contextfabric.FactReadFailureOrganizationRequired {
		t.Errorf("organization required class = %q", got)
	}
}
