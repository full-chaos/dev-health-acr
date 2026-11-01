package v1

import "testing"

// CHAOS-12415, the per-row ruling on CHAOS-12413: the word a reader sees for
// each source state. The ten are pinned together, because keeping unknown,
// stale, no data, not applicable and not needed apart (North Star check 12) is
// a property of the set: "no data" must not become web's "zero rows", which is
// true of only one of the three no_data paths.
func TestEachSourceStateServesItsRuledWord(t *testing.T) {
	want := map[ContextFabricSourceState]string{
		ContextFabricSourceAvailable:     "available",
		ContextFabricSourceStale:         "may be out of date",
		ContextFabricSourceUnavailable:   "unavailable",
		ContextFabricSourceUnconfigured:  "not set up",
		ContextFabricSourceUnauthorized:  "not readable with your access",
		ContextFabricSourceNoData:        "no data",
		ContextFabricSourceTruncated:     "partly included",
		ContextFabricSourceConflicted:    "sources disagree",
		ContextFabricSourceNotApplicable: "not applicable",
		ContextFabricSourcePruned:        "not needed for this question",
	}
	if len(want) != len(contextFabricSourceStateLabels) {
		t.Fatalf("this table has %d states, the registry %d", len(want), len(contextFabricSourceStateLabels))
	}
	seen := map[string]ContextFabricSourceState{}
	for state, word := range want {
		got := ContextFabricSourceStateLabel(state)
		if got != word {
			t.Errorf("state %q reads %q, want %q", state, got, word)
		}
		if other, duplicate := seen[got]; duplicate {
			t.Errorf("states %q and %q read the same word %q: two states a reader must tell apart", state, other, got)
		}
		seen[got] = state
	}
}
