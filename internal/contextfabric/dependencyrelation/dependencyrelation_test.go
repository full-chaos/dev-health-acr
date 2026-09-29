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
