package api

import (
	"os"
	"strings"
	"testing"
)

// TestResponseBudgetMaxSerializedBytesIsWiredThroughTheNumericBarrier covers
// BOTH response-budget log lines with one assertion, because both are built
// from one shared field set.
//
// The value is `min(server ceiling, caller's own Options.MaxSerializedBytes)`
// -- already the enforced bound rather than the raw request, which is the
// right number to report, but still request-derived and so still the numeric
// class the barrier exists for.
//
// A SOURCE-SHAPE PIN, for the same reason the barrier's own tests are: the
// bare and the wired form log an identical integer, so no handler test can
// discriminate them. The single shared builder is also why one pin is enough
// -- and why the count is asserted, so a future second builder cannot appear
// unwired while this test goes on passing.
func TestResponseBudgetMaxSerializedBytesIsWiredThroughTheNumericBarrier(t *testing.T) {
	src, err := os.ReadFile("context_fabric_routes.go")
	if err != nil {
		t.Fatalf("could not read context_fabric_routes.go: %v", err)
	}
	text := string(src)

	const wired = `"max_serialized_bytes", contextfabric.SanitizeLogInt(maximumBytes)`
	if n := strings.Count(text, wired); n != 1 {
		t.Errorf("context_fabric_routes.go wires max_serialized_bytes through the numeric barrier at %d site(s), want exactly 1 -- the exceed line and the measured line share one builder, so one wiring covers both alert locations", n)
	}
	if strings.Contains(text, `"max_serialized_bytes", maximumBytes,`) {
		t.Error("context_fabric_routes.go still logs maximumBytes bare -- it derives from the caller's own MaxSerializedBytes option and reaches a logger unbarriered")
	}
	if total := strings.Count(text, `"max_serialized_bytes",`); total != 1 {
		t.Errorf("context_fabric_routes.go has %d max_serialized_bytes field(s), want 1 -- this pin's own count assumption is stale, so update it rather than deleting it", total)
	}
}
