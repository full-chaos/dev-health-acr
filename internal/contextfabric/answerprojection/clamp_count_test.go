package answerprojection

import (
	"reflect"
	"strings"
	"testing"
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
