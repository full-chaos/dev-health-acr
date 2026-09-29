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
