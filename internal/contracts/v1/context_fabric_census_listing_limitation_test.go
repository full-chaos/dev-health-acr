package v1

import (
	"strconv"
	"strings"
	"testing"
)

func TestTheCensusListingLimitationsAreComposedAndRecognised(t *testing.T) {
	t.Parallel()
	for _, count := range []int{1, 2, 10, 999, 1001, contextFabricCensusLimitationMaxOf} {
		got := ContextFabricCensusUnlistedLimitation(count)
		if !IsContextFabricCensusListingLimitation(got) || !IsContextFabricServiceAuthoredLimitation(got) {
			t.Fatalf("unlisted %d: %q is not recognised", count, got)
		}
		if strings.Contains(got, "no candidate") || strings.Contains(got, "found no") {
			t.Fatalf("unlisted %d: %q reads as nothing found", count, got)
		}
		if !strings.HasPrefix(got, strconv.Itoa(count)+" subject") {
			t.Fatalf("unlisted %d: %q does not state the count", count, got)
		}
	}
	for _, tc := range []struct{ count, listed int }{{2, 1}, {10, 5}, {999, 25}, {1001, 2}} {
		got := ContextFabricCensusListedLimitation(tc.count, tc.listed)
		if !IsContextFabricCensusListingLimitation(got) || !IsContextFabricServiceAuthoredLimitation(got) {
			t.Fatalf("listed %+v: %q is not recognised", tc, got)
		}
		if !strings.Contains(got, "Only "+strconv.Itoa(tc.listed)+" of them") {
			t.Fatalf("listed %+v: %q does not state the listing", tc, got)
		}
	}
	for _, tc := range []struct {
		name string
		got  string
	}{
		{"unlisted zero", ContextFabricCensusUnlistedLimitation(0)},
		{"unlisted negative", ContextFabricCensusUnlistedLimitation(-1)},
		{"listed none", ContextFabricCensusListedLimitation(10, 0)},
		{"listed all", ContextFabricCensusListedLimitation(10, 10)},
		{"listed past the count", ContextFabricCensusListedLimitation(10, 11)},
	} {
		if tc.got != "" {
			t.Fatalf("%s: %q, want nothing", tc.name, tc.got)
		}
	}
}

// A model caveat cannot pass for a service sentence: only what the composers
// write parses.
func TestAForgedCensusListingLimitationIsNotRecognised(t *testing.T) {
	t.Parallel()
	many := ContextFabricCensusUnlistedLimitation(10)
	partly := ContextFabricCensusListedLimitation(10, 5)
	for _, forged := range []string{
		"",
		strings.Replace(many, "10 ", "1 ", 1),
		strings.Replace(many, "10 ", "0 ", 1),
		strings.Replace(many, "10 ", "010 ", 1),
		strings.Replace(many, "10 ", "+10 ", 1),
		strings.Replace(many, "10 ", "ten ", 1),
		strings.Replace(many, "10 ", "9999999999 ", 1),
		many + " ",
		" " + many,
		strings.Replace(partly, "Only 5 ", "Only 10 ", 1),
		strings.Replace(partly, "Only 5 ", "Only 11 ", 1),
		strings.Replace(partly, "Only 5 ", "Only 0 ", 1),
		strings.Replace(partly, "Only 5 ", "Only 1 ", 1),
		strings.Replace(ContextFabricCensusListedLimitation(10, 1), "Only 1 ", "Only 2 ", 1),
		strings.Replace(partly, "10 ", "1 ", 1),
	} {
		if IsContextFabricCensusListingLimitation(forged) {
			t.Fatalf("%q passed as a service sentence", forged)
		}
	}
}
