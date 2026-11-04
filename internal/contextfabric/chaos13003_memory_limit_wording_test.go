package contextfabric

import (
	"strings"
	"testing"
)

// CHAOS-13003: a read the server stopped at its memory limit is served as that,
// in the kind's own word, and keeps the source state.
func TestAMemoryLimitedReadIsServedAsOne(t *testing.T) {
	t.Parallel()
	failure := &FactReadFailure{State: SourceUnavailable, Reason: FactReadMemoryLimitReason, Class: FactReadFailureReadFailed, SourceCode: 241}
	state, reason := classifyFactReadError(FactBlockers, failure)
	if state != SourceUnavailable {
		t.Fatalf("state %q", state)
	}
	if !strings.Contains(reason, "memory limit") || strings.Contains(reason, "read failed") || !strings.HasSuffix(reason, "the facts for it are not shown.") {
		t.Fatalf("reason %q", reason)
	}
	_, generic := classifyFactReadError(FactBlockers, &FactReadFailure{State: SourceUnavailable, Reason: "devhealthfacts: query team blockers failed", Class: FactReadFailureReadFailed})
	if !strings.Contains(generic, "read failed") || generic == reason {
		t.Fatalf("a generic failure reads %q", generic)
	}
}
