package dependencyrelation

import (
	"strings"
	"testing"
)

func TestKeyAndEmittedSpelling(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ raw, key, emitted string }{
		{"relates", "relates_to:fwd", "relates_to"},
		{"relates_to", "relates_to:fwd", "relates_to"},
		{" RELATES ", "relates_to:fwd", "relates_to"},
		{"BLOCKED_BY", "blocks:inv", "blocked_by"},
		{"is_blocked_by", "blocks:inv", "blocked_by"},
		{"blocks", "blocks:fwd", "blocks"},
		{"BLOCKS", "blocks:fwd", "blocks"},
		{"requires", "requires:fwd", "requires"},
		{"", ":fwd", ""},
		{" \t ", ":fwd", ""},
		// ASCII-only normalization, byte-for-byte what KeySQL does in ClickHouse:
		// 'ς' and NBSP-wrapped spellings must NOT be Unicode-folded/trimmed (CHAOS-7177 r3).
		{"ς", "ς:fwd", "ς"},
		{"\u00a0requires\u00a0", "\u00a0requires\u00a0:fwd", "\u00a0requires\u00a0"},
		{"Requires", "requires:fwd", "requires"},
	} {
		if got := Key(tc.raw); got != tc.key {
			t.Errorf("Key(%q) = %q, want %q", tc.raw, got, tc.key)
		}
		if got := EmittedSpelling(tc.raw); got != tc.emitted {
			t.Errorf("EmittedSpelling(%q) = %q, want %q", tc.raw, got, tc.emitted)
		}
	}
}

// KeySQL is generated from the same table; every alias and its key must appear.
func TestKeySQLCoversEveryAlias(t *testing.T) {
	t.Parallel()
	sql := KeySQL("d.relationship_type")
	for alias := range mapping {
		typ, swap, _ := Resolve(alias)
		want := "= '" + alias + "', '" + keyOf(strings.ToLower(string(typ)), swap) + "'"
		if !strings.Contains(sql, want) {
			t.Errorf("KeySQL missing %q: %s", want, sql)
		}
	}
	if !strings.Contains(sql, "':fwd'))") {
		t.Errorf("KeySQL has no pass-through default: %s", sql)
	}
}

func TestBlockerPredicateSQLNamesEverySpellingFromTheMapAndHasNoComparisonOperator(t *testing.T) {
	t.Parallel()
	predicate := BlockerPredicateSQL("d.relationship_type")
	inverse := BlockerInverseSQL("d.relationship_type")
	for _, sql := range []string{predicate, inverse} {
		if strings.Contains(sql, "=") {
			t.Fatalf("a comparison operator inside a JOIN ON operand: %s", sql)
		}
	}
	for _, spelling := range []string{"'blocks'", "'blocked_by'", "'is_blocked_by'"} {
		if !strings.Contains(predicate, spelling) {
			t.Fatalf("predicate does not name %s: %s", spelling, predicate)
		}
	}
	if strings.Contains(inverse, "'blocks'") || !strings.Contains(inverse, "'blocked_by'") || !strings.Contains(inverse, "'is_blocked_by'") {
		t.Fatalf("inverse set is not exactly the swapping spellings: %s", inverse)
	}
}

func TestResolveIdentitySpellingIsTrimmedLowerCase(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ raw, want string }{
		{"blocks", "blocks"},
		{"BLOCKS", "blocks"},
		{" Relates_To\t", "relates_to"},
		{"RELATES", "relates_to"},
		{"relates", "relates_to"},
		{"BLOCKED_BY", "blocks"},
		{"is_blocked_by", "blocks"},
		{"Requires", "requires"},
		{"\u00a0Requires", "\u00a0requires"},
	} {
		if _, _, got := Resolve(tc.raw); got != tc.want {
			t.Errorf("Resolve(%q) identity = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

// A spelling that is already trimmed lower case and unmapped keeps the id it
// always had; this is the set whose edges the version bump must not orphan.
func TestResolveKeepsIdentityOfAlreadyCanonicalSpellings(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"blocks", "relates_to", "duplicates", "related_to", "part_of", "requires"} {
		if _, _, got := Resolve(raw); got != raw {
			t.Errorf("Resolve(%q) identity = %q, want it unchanged", raw, got)
		}
	}
}
