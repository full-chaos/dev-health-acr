package certify

import (
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
)

// TestALineWithAnUndeclaredKeyIsRefused: a line that carries a key the event's
// declaration does not list does not certify, and the refusal names the key.
func TestALineWithAnUndeclaredKeyIsRefused(t *testing.T) {
	t.Parallel()
	ev := eventspec.OrganizationSubjectRead
	base := canonicalLineFor(ev)
	want := attributionWant(ev, base)
	if _, err := certifyRecovered(t, marshalJoinLines(t, ev.ID, base), Assertion{Event: ev, Want: want}); err != nil {
		t.Fatalf("the canonical line must certify: %v", err)
	}
	extra := deepCopyLine(t, base)
	extra["surprise_key"] = "x"
	_, err := certifyRecovered(t, marshalJoinLines(t, ev.ID, extra), Assertion{Event: ev, Want: want})
	if err == nil {
		t.Fatalf("a line carrying an undeclared key certified")
	}
	if !strings.Contains(err.Error(), "surprise_key") {
		t.Fatalf("the refusal must name the undeclared key, got: %v", err)
	}
}
