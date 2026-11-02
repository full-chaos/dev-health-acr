package contextfabric

import "testing"

// CHAOS-12558: the input turn of a client synthesis redeems no offer, and every
// other stored result keeps the claim it made before.
func TestOnlyAClientInputTurnLeavesAnOfferUnclaimed(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		source  SynthesisSource
		version string
		redeems bool
	}{
		"the client input turn":        {SynthesisSourceClient, SynthesisVersionNotSynthesized, false},
		"the client answer turn":       {SynthesisSourceClient, "context-fabric-synthesis.v29", true},
		"a server answer":              {SynthesisSourceServer, "context-fabric-synthesis.v29", true},
		"a server turn with no answer": {"", SynthesisVersionNotSynthesized, true},
		"a stored row with no source":  {"", "context-fabric-synthesis.v12", true},
		"a turn with nothing wired":    {"", unwiredVersion, true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			result := InvestigationResult{Versions: VersionSet{SynthesisSource: tc.source, SynthesisVersion: tc.version}}
			if got := RedeemsStructureOffers(result); got != tc.redeems {
				t.Fatalf("redeems = %v, want %v", got, tc.redeems)
			}
		})
	}
}
