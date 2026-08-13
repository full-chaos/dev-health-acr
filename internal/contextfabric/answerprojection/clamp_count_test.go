package answerprojection

import (
	"reflect"
	"strconv"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// TestClampedCountMatchesTheSurvivorsActuallyShortened closes a miscount in
// clamper.strings (codex round-9 F4).
//
// The count loop indexed the ORIGINAL slice by SURVIVOR position. Dedup
// removes entries, so after the first drop every later survivor was compared
// against the wrong original -- and a survivor that WAS shortened went
// uncounted because the unrelated original it landed on happened to be
// short. ValuesClamped then under-reported, which is the one thing a budget
// field may never do: a consumer reads it to decide whether the values it
// received are verbatim.
//
// The interleaved case is the one that fails: a shortened entry, a duplicate
// that collides with it, then a second shortened entry.
func TestClampedCountMatchesTheSurvivorsActuallyShortened(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		values    []string
		maxCount  int
		maxLength int
		survivors []string
		clamped   int
		dropped   int
	}{
		{
			// The exact case from the finding: "aaaX" clamps to "aaa" and
			// collides with the literal "aaa"; "bbbX" clamps to "bbb". Two
			// survivors, BOTH of them shortened.
			name:      "dedup collision before a later shortened entry",
			values:    []string{"aaaX", "aaa", "bbbX"},
			maxCount:  3,
			maxLength: 3,
			survivors: []string{"aaa", "bbb"},
			clamped:   2,
			dropped:   1,
		},
		{
			// A survivor that was never shortened must not be counted just
			// because it sits at a position whose original was long.
			name:      "short survivor after a dropped long one",
			values:    []string{"aaaX", "aaa", "bbb"},
			maxCount:  3,
			maxLength: 3,
			survivors: []string{"aaa", "bbb"},
			clamped:   1,
			dropped:   1,
		},
		{
			// Truncation past maxCount must not count entries that never
			// reached the wire.
			name:      "entries cut by maxCount are not counted as clamped",
			values:    []string{"aaaX", "bbbX", "cccX"},
			maxCount:  1,
			maxLength: 3,
			survivors: []string{"aaa"},
			clamped:   1,
			dropped:   2,
		},
		{
			name:      "nothing shortened counts nothing",
			values:    []string{"aa", "bb"},
			maxCount:  5,
			maxLength: 3,
			survivors: []string{"aa", "bb"},
			clamped:   0,
			dropped:   0,
		},
		{
			// Runes, not bytes: a multi-byte entry is shortened by rune
			// count, and counted once.
			name:      "multi-byte entry shortened by runes",
			values:    []string{strings.Repeat("é", 5)},
			maxCount:  5,
			maxLength: 3,
			survivors: []string{strings.Repeat("é", 3)},
			clamped:   1,
			dropped:   0,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			clamp := &clamper{}
			got, dropped := clamp.strings(testCase.values, testCase.maxCount, testCase.maxLength)
			if !reflect.DeepEqual(got, testCase.survivors) {
				t.Errorf("survivors = %q, want %q", got, testCase.survivors)
			}
			if dropped != testCase.dropped {
				t.Errorf("dropped = %d, want %d", dropped, testCase.dropped)
			}
			if clamp.count != testCase.clamped {
				t.Errorf("ValuesClamped counted %d, want %d (the number of survivors actually shortened)", clamp.count, testCase.clamped)
			}
		})
	}
}

// TestNarrativeClampCountMatchesTheSurvivorsActuallyShortened is the same
// property as the table above, on the NARRATIVE path (codex round-10 F1).
//
// boundedNarrative clamped through clamper.text, which counts on every
// input -- so it counted before dedup and before the projection's own
// 100-entry cap. 101 distinct oversized limitations produced 100 values on
// the wire and reported values_clamped: 101, a count of something the
// consumer never received. The round-9 F4 fix established the mechanics;
// this path simply was not covered by that table.
func TestNarrativeClampCountMatchesTheSurvivorsActuallyShortened(t *testing.T) {
	const maxLength = contractsv1.ContextFabricProjectedNarrativeMaxLength
	const maxCount = contractsv1.ContextFabricProjectedNarrativeMaxCount

	// oversized builds a distinct entry longer than the projection allows.
	oversized := func(seed string) string {
		return seed + "-" + strings.Repeat("x", maxLength)
	}

	t.Run("entries dropped by the count cap are not counted as clamped", func(t *testing.T) {
		values := make([]string, 0, maxCount+1)
		for i := 0; i < maxCount+1; i++ {
			values = append(values, oversized("limitation"+strconv.Itoa(i)))
		}
		clamp := &clamper{}
		got, omitted := boundedNarrative(values, clamp)
		if len(got) != maxCount {
			t.Fatalf("survivors = %d, want %d", len(got), maxCount)
		}
		if omitted != 1 {
			t.Errorf("omitted = %d, want 1", omitted)
		}
		if clamp.count != maxCount {
			t.Errorf("ValuesClamped counted %d, want %d: the %dst entry was dropped by the cap and never reached the wire, so it was not a value the consumer received in shortened form",
				clamp.count, maxCount, maxCount+1)
		}
	})

	t.Run("entries dropped by dedup are not counted as clamped", func(t *testing.T) {
		// Two entries that are distinct until clamped, then collide.
		shared := strings.Repeat("y", maxLength)
		clamp := &clamper{}
		got, omitted := boundedNarrative([]string{shared + "-first", shared + "-second"}, clamp)
		if len(got) != 1 {
			t.Fatalf("survivors = %d, want 1 (they collide once clamped)", len(got))
		}
		if omitted != 1 {
			t.Errorf("omitted = %d, want 1", omitted)
		}
		if clamp.count != 1 {
			t.Errorf("ValuesClamped counted %d, want 1: only one clamped value survived to the wire", clamp.count)
		}
	})

	t.Run("interleaved short and oversized entries", func(t *testing.T) {
		clamp := &clamper{}
		short := "a short limitation"
		got, omitted := boundedNarrative([]string{oversized("one"), short, oversized("two")}, clamp)
		if len(got) != 3 || omitted != 0 {
			t.Fatalf("survivors = %d, omitted = %d, want 3 and 0", len(got), omitted)
		}
		if got[1] != short {
			t.Errorf("the short entry was rewritten: %q", got[1])
		}
		if clamp.count != 2 {
			t.Errorf("ValuesClamped counted %d, want 2: both oversized entries survived shortened, the short one was verbatim", clamp.count)
		}
	})

	t.Run("nothing oversized counts nothing", func(t *testing.T) {
		clamp := &clamper{}
		got, omitted := boundedNarrative([]string{"one", "two"}, clamp)
		if len(got) != 2 || omitted != 0 {
			t.Fatalf("survivors = %d, omitted = %d, want 2 and 0", len(got), omitted)
		}
		if clamp.count != 0 {
			t.Errorf("ValuesClamped counted %d, want 0", clamp.count)
		}
	})
}
