package eventspec

// CHAOS-5516 r3 -- round r3 finding 2's own domain cells: EVERY required
// string_slice/object_slice field on EVERY generated constructor, nil vs
// empty vs canonical, driven through the REAL generated New<Name>Fields
// functions (not a raw-JSON fixture -- this is a Go-level constructor
// contract, not a certify.Parse-level one). nil must never mark the value
// constructed; [] (or a populated slice) must.

import "testing"

func TestGeneratedConstructorRejectsNilRequiredSlicesPerField(t *testing.T) {
	type cell struct {
		name  string
		build func(nilOne bool) bool // returns IsConstructed()
	}

	decisionSummaryAllValid := func() (committedIDs, commitGates, commitBases, withheldIDs, reservedKinds, filterKinds []string) {
		return []string{}, []string{}, []string{}, []string{}, []string{}, []string{}
	}
	rankedCutAllValid := func() (survivedIDs []string, rescue []map[string]any) {
		return []string{}, []map[string]any{}
	}

	cells := []cell{
		{"DecisionSummary.committed_ids", func(nilOne bool) bool {
			c, cg, cb, w, r, f := decisionSummaryAllValid()
			if nilOne {
				c = nil
			}
			return NewDecisionSummaryFields("r", 0, 0, 0, 0, c, cg, cb, false, "none", "none", 0, 0, false, 0, "none", "none", w, 0, "none", "none", "none", r, f).IsConstructed()
		}},
		{"DecisionSummary.commit_gates", func(nilOne bool) bool {
			c, cg, cb, w, r, f := decisionSummaryAllValid()
			if nilOne {
				cg = nil
			}
			return NewDecisionSummaryFields("r", 0, 0, 0, 0, c, cg, cb, false, "none", "none", 0, 0, false, 0, "none", "none", w, 0, "none", "none", "none", r, f).IsConstructed()
		}},
		{"DecisionSummary.commit_bases", func(nilOne bool) bool {
			c, cg, cb, w, r, f := decisionSummaryAllValid()
			if nilOne {
				cb = nil
			}
			return NewDecisionSummaryFields("r", 0, 0, 0, 0, c, cg, cb, false, "none", "none", 0, 0, false, 0, "none", "none", w, 0, "none", "none", "none", r, f).IsConstructed()
		}},
		{"DecisionSummary.offer_pool_anchor_kind_withheld_ids", func(nilOne bool) bool {
			c, cg, cb, w, r, f := decisionSummaryAllValid()
			if nilOne {
				w = nil
			}
			return NewDecisionSummaryFields("r", 0, 0, 0, 0, c, cg, cb, false, "none", "none", 0, 0, false, 0, "none", "none", w, 0, "none", "none", "none", r, f).IsConstructed()
		}},
		{"DecisionSummary.reserved_kinds", func(nilOne bool) bool {
			c, cg, cb, w, r, f := decisionSummaryAllValid()
			if nilOne {
				r = nil
			}
			return NewDecisionSummaryFields("r", 0, 0, 0, 0, c, cg, cb, false, "none", "none", 0, 0, false, 0, "none", "none", w, 0, "none", "none", "none", r, f).IsConstructed()
		}},
		{"DecisionSummary.filter_kinds", func(nilOne bool) bool {
			c, cg, cb, w, r, f := decisionSummaryAllValid()
			if nilOne {
				f = nil
			}
			return NewDecisionSummaryFields("r", 0, 0, 0, 0, c, cg, cb, false, "none", "none", 0, 0, false, 0, "none", "none", w, 0, "none", "none", "none", r, f).IsConstructed()
		}},
		{"RankedCutSummary.survived_ids", func(nilOne bool) bool {
			s, rr := rankedCutAllValid()
			if nilOne {
				s = nil
			}
			return NewRankedCutSummaryFields("r", 1, 0, 0, s, 0, "none", "none", 0, 0, rr).IsConstructed()
		}},
		{"RankedCutSummary.declared_kind_rescue", func(nilOne bool) bool {
			s, rr := rankedCutAllValid()
			if nilOne {
				rr = nil
			}
			return NewRankedCutSummaryFields("r", 1, 0, 0, s, 0, "none", "none", 0, 0, rr).IsConstructed()
		}},
	}

	for _, c := range cells {
		c := c
		t.Run(c.name+"/nil", func(t *testing.T) {
			if got := c.build(true); got {
				t.Errorf("%s passed nil -- IsConstructed() = true, want false", c.name)
			}
		})
		t.Run(c.name+"/empty_canonical", func(t *testing.T) {
			if got := c.build(false); !got {
				t.Errorf("%s passed an empty (non-nil) slice -- IsConstructed() = false, want true", c.name)
			}
		})
	}
	if len(cells) != 8 {
		t.Fatalf("census: %d cells, want 8 (6 DecisionSummary slice fields + 2 RankedCutSummary slice fields) -- a future slice field is swept the day it lands", len(cells))
	}
}
