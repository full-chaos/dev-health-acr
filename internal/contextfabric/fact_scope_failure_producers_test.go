package contextfabric

import (
	"context"
	"errors"
	"testing"
)

// TestEveryDeclaredFactScopeFailureClassHasAProducer fails when a class is
// declared in factScopeFailureClasses without an entry here, and when an entry
// does not classify to its class. The producers' side (a real expander
// returning these errors) is proven in devhealthfacts, in
// TestChaos5405_EveryTypedFactScopeFailureIsProducedByTheRealExpander.
func TestEveryDeclaredFactScopeFailureClassHasAProducer(t *testing.T) {
	t.Parallel()
	producers := map[FactScopeFailureClass]error{
		FactScopeFailureBackendUnavailable: errors.New("clickhouse unavailable"),
		FactScopeFailureTimeout:            context.DeadlineExceeded,
		FactScopeFailureAxisUnsupported:    ErrFactScopeAxisUnsupported,
		FactScopeFailureReadLimitExceeded:  ErrFactScopeReadLimitExceeded,
	}
	for _, class := range factScopeFailureClasses {
		err, ok := producers[class]
		if !ok {
			t.Errorf("failure class %q is declared with no producer", class)
			continue
		}
		if got := classifyFactScopeFailure(err); got != class {
			t.Errorf("producer error for %q classifies as %q", class, got)
		}
		if reason := factScopeDecisionReasonForFailure(class); !validFactScopeDecisionReason(reason) {
			t.Errorf("failure class %q maps to undeclared decision reason %q", class, reason)
		}
	}
	if len(producers) != len(factScopeFailureClasses) {
		t.Errorf("producers has %d entries for %d declared classes", len(producers), len(factScopeFailureClasses))
	}
}
