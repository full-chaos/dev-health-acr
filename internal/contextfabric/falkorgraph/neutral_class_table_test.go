package falkorgraph

import (
	"errors"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
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

// TestNeutralClassesAndAbsenceClassesStayDisjoint is the cross-check main
// asked for. Two vocabularies meet in this package and must never blur: the
// DEPENDENCY classes (ErrUnavailable / ErrInvalidResult / ErrRateLimited),
// and the ABSENCE classes (ErrGraphNotProjected, ErrProjectionWatermarkNotFound)
// that say a graph key or a watermark is confirmed missing.
//
// Both directions matter and neither implies the other. If a later "helpful"
// declaration gave ErrNotFound a dependency class, a never-projected
// organization would answer 503 instead of the clean empty answer Engine
// degrades it to. If an absence carrier ever picked up a dependency class --
// or a dependency sentinel picked up an absence class -- a transient outage
// could trigger the destructive rebuild path ErrProjectionWatermarkNotFound's
// own doc comment warns about. Quantified over the whole sentinel set, so a
// sentinel added later is covered without touching this test.
func TestNeutralClassesAndAbsenceClassesStayDisjoint(t *testing.T) {
	absenceClasses := []error{
		contextfabric.ErrGraphNotProjected,
		contextfabric.ErrProjectionWatermarkNotFound,
	}
	if len(knownSentinels) == 0 || len(absenceClasses) == 0 {
		t.Fatal("an input set is empty; this test would be vacuous")
	}
	checked := 0
	for _, sentinel := range knownSentinels {
		for _, absence := range absenceClasses {
			if errors.Is(sentinel, absence) {
				t.Fatalf("sentinel %v satisfies the ABSENCE class %v -- a dependency failure "+
					"must never read as a confirmed absence", sentinel, absence)
			}
			checked++
		}
	}
	// And the other direction, on the real carriers rather than on the bare
	// sentinels: these are the two errors that legitimately DO mean absence.
	absenceCarriers := []error{
		notFoundWatermarkErr(),
		graphNotProjectedError(classifyFalkorError("read", errors.New("Invalid graph operation on empty key"))),
	}
	for _, carrier := range absenceCarriers {
		for _, dependency := range neutralUniverse {
			if errors.Is(carrier, dependency) {
				t.Fatalf("absence carrier %v satisfies the dependency class %v -- a confirmed "+
					"absence must never read as an outage", carrier, dependency)
			}
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("no pairing was checked; the disjointness proof is vacuous")
	}
	t.Logf("assertion reach: %d disjointness pairings", checked)
}
