package hintsource_test

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/hintsource"
)

// CHAOS-12230: the Ask Dev page-context source is caller authored. It is named
// in this package so the resolver has one spelling of it, and it must stay out
// of the engine-minted registry: registering it would reclassify every web
// page hint as this engine's own output.
func TestAskDevPageContextIsCallerAuthoredAndUnregistered(t *testing.T) {
	for _, source := range hintsource.All() {
		if string(source) == hintsource.AskDevPageContext {
			t.Fatalf("%q is registered as an engine-minted source; it is written by web", hintsource.AskDevPageContext)
		}
	}
	attributes := hintsource.Lookup(hintsource.AskDevPageContext)
	if attributes.EngineMinted || !attributes.ContestExempt.Exempt() || !attributes.ShortCircuitEligible.Eligible() || !attributes.SearchFallback.Permitted() {
		t.Fatalf("Lookup(%q) = %+v, want the caller-authored attributes", hintsource.AskDevPageContext, attributes)
	}
}
