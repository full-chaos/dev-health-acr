package falkorgraph

import (
	"errors"
	"testing"
)

// CHAOS-4874. These two tests are STRUCTURAL: they constrain the neutralClass
// table itself, so they exist only alongside it and have no meaning on a tree
// without it. The behavioural red-first proofs live in
// neutral_error_class_test.go, which compiles unchanged against the parent
// commit.

// TestNeutralClassCoversEveryKnownSentinel is the structural guard: a sentinel
// added to knownSentinels without a neutralClass decision re-opens exactly the
// hole this ticket closed. Key PRESENCE is what is required; a nil value is a
// recorded decision that the condition has no neutral class.
func TestNeutralClassCoversEveryKnownSentinel(t *testing.T) {
	if len(knownSentinels) == 0 {
		t.Fatal("knownSentinels is empty; this test would be vacuous")
	}
	checked := 0
	for _, sentinel := range knownSentinels {
		if _, ok := neutralClass[sentinel]; !ok {
			t.Fatalf("sentinel %v has no neutralClass entry: decide its backend-neutral "+
				"class (or record nil for 'deliberately none') -- without one it reaches "+
				"projectionrun as failure_class=unclassified", sentinel)
		}
		checked++
	}
	if checked != len(knownSentinels) {
		t.Fatalf("checked %d of %d sentinels", checked, len(knownSentinels))
	}
}

// TestSentinelsCarryTheirDeclaredNeutralClass proves config.go's declarations
// agree with neutralClass, the specification. Without this the two could drift
// silently: the map would still read correctly while the sentinels shipped
// bare.
func TestSentinelsCarryTheirDeclaredNeutralClass(t *testing.T) {
	if len(neutralClass) == 0 {
		t.Fatal("neutralClass is empty; this test would be vacuous")
	}
	positive, negative := 0, 0
	for sentinel, neutral := range neutralClass {
		// The pairing loop below quantifies over neutralUniverse, so a
		// value OUTSIDE it would be silently unchecked in both directions
		// -- a sweep keyed on the wrong population. Close that first.
		if neutral != nil {
			member := false
			for _, candidate := range neutralUniverse {
				if candidate == neutral {
					member = true
				}
			}
			if !member {
				t.Fatalf("neutralClass names %v for %v, but it is not in neutralUniverse: "+
					"add it there or this row is never actually asserted", neutral, sentinel)
			}
		}
		for _, candidate := range neutralUniverse {
			want := neutral != nil && errors.Is(candidate, neutral)
			got := errors.Is(sentinel, candidate)
			if want && !got {
				t.Fatalf("sentinel %v does not satisfy its declared neutral class %v -- "+
					"declare it as fmt.Errorf(\"%%w: ...\", %v) in config.go", sentinel, neutral, neutral)
			}
			if !want && got {
				t.Fatalf("sentinel %v unexpectedly satisfies %v (neutralClass says %v)",
					sentinel, candidate, neutral)
			}
			if want {
				positive++
			} else {
				negative++
			}
		}
	}
	if positive == 0 {
		t.Fatal("no sentinel asserted a positive neutral class; the test proved nothing")
	}
	if negative == 0 {
		t.Fatal("no sentinel asserted the ABSENCE of a class; the wrong-class case is unprobed")
	}
	t.Logf("assertion reach: %d positive, %d negative pairings", positive, negative)
}
