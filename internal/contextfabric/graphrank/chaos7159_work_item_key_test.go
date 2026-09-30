package graphrank

import "testing"

// CHAOS-7159 / CHAOS-7200: the any-prefix key grammar. BindWorkItemKey binds
// only a whole key; HasWorkItemKeyToken scans text; BindHandles (free text)
// keeps the registered CHAOS prefix only.
func TestWorkItemKeyAnyPrefixGrammar(t *testing.T) {
	for _, tc := range []struct {
		text       string
		exact, has bool
		freeText   int // BindHandles count
	}{
		{"PAY-42", true, true, 0},
		{"CHAOS-4322", true, true, 1},
		{"pay2-7", true, true, 0},
		{"please inspect PAY-42", false, true, 0},
		{"CHAOS-1 PAY-2", false, true, 1},
		{"PR-123", true, true, 0},
		{"PAY-", false, false, 0},
		{"PAY-abc", false, false, 0},
		{"9PAY-1", false, false, 0},
		{"well-known", false, false, 0},
		{"run 12345", false, false, 1},
		{"ABCDEFGHIJKLMNOPQRSTUVWXYZABCDEFG-1", false, false, 0},
		{"Δ-123", false, false, 0},
		{"", false, false, 0},
	} {
		if _, ok := BindWorkItemKey(tc.text); ok != tc.exact {
			t.Errorf("BindWorkItemKey(%q) = %v, want %v", tc.text, ok, tc.exact)
		}
		if got := HasWorkItemKeyToken(tc.text); got != tc.has {
			t.Errorf("HasWorkItemKeyToken(%q) = %v, want %v", tc.text, got, tc.has)
		}
		if got := len(BindHandles(tc.text)); got != tc.freeText {
			t.Errorf("BindHandles(%q) = %d handles, want %d", tc.text, got, tc.freeText)
		}
	}
}
