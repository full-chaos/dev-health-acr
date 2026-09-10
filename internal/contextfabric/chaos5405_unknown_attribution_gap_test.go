package contextfabric

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// chaos5405UnknownAttributionScope drives ONE resolution whose expander
// refuses `refused` rows for an unrecognised attribution source and admits
// `admit` targets, and returns the event and the resolved scope.
func chaos5405UnknownAttributionScope(t *testing.T, admit []SubjectRef, candidates, refused int) (FactScopeExpansionEvent, FactReadScope) {
	t.Helper()
	expander := &reasonDriverExpander{result: FactScopeExpansionResult{
		Targets: admit,
		Counts: FactScopeExpansionCounts{
			CandidateCount:                candidates,
			UnknownAttributionSourceCount: refused,
		},
	}}
	scope := NewFactReadScopeResolver(expander).Resolve(
		context.Background(), storage.Principal{OrgID: "org_1"},
		newFactScopeResolveInput(scopeTimeRequest(
			[]SubjectRef{scopeProject}, []FactRequirement{{Kind: FactStatus}},
			TimeContext{Axis: TemporalCurrent},
		)),
		map[FactKind]FactCapability{FactStatus: planCapability(FactStatus, "status", SubjectWorkItem)},
	)
	for _, event := range scope.Events {
		if event.RequirementKind == FactStatus {
			return event, scope
		}
	}
	t.Fatal("no event emitted for the requirement under test")
	return FactScopeExpansionEvent{}, FactReadScope{}
}

// TestChaos5405_AnUnrecognisedAttributionSourceIsAGapNotAnEmptyResult pins
// codex r3 P1.
//
// A row that was SEEN and then REFUSED for violating the closed attribution
// vocabulary was reported as `attempted_empty` -- which this file's own
// resolver calls "the ONE outcome here that is a real proof of absence" -- and
// disclosed nothing. Reproduced before the fix:
//
//	outcome="attempted_empty" decision_reason="attribution_source_unrecognized"
//	candidate_count=1 unknown_attribution_source_count=1 has_disclosable_gap=false
//
// Proof of absence requires a MEASURED, RECOGNISED population. A refusal is
// neither, so the outcome is the withheld class (`expanded_partial`, the
// member factScopeGapDegrades already treats as missing evidence, and the one
// the truncation rung uses for the identical reason) and the answer discloses.
//
// The DECISION REASON is asserted alongside, because outcome alone cannot tell
// this apart from truncation: outcome says the answer is short, reason says a
// closed vocabulary was violated upstream. A fix that set the outcome and lost
// the reason would pass an outcome-only pin and destroy the operator's half.
func TestChaos5405_AnUnrecognisedAttributionSourceIsAGapNotAnEmptyResult(t *testing.T) {
	t.Parallel()

	t.Run("every candidate refused", func(t *testing.T) {
		t.Parallel()
		event, scope := chaos5405UnknownAttributionScope(t, nil, 1, 1)
		if event.Outcome != FactScopeExpandedPartial {
			t.Errorf("outcome = %q, want %q -- a refused row is a withheld one, and attempted_empty asserts a proof of absence this traversal never earned",
				event.Outcome, FactScopeExpandedPartial)
		}
		if event.Outcome == FactScopeAttemptedEmpty {
			t.Errorf("outcome is the proof-of-absence member exactly")
		}
		if event.DecisionReason != FactScopeDecisionAttributionSourceUnrecognized {
			t.Errorf("decision_reason = %q, want %q -- the reason is what separates this from truncation",
				event.DecisionReason, FactScopeDecisionAttributionSourceUnrecognized)
		}
		if !scope.HasDisclosableGap() {
			t.Error("has_disclosable_gap = false -- the answer reports completeness it did not earn")
		}
		// VALUES, not presence: the counts that made this a gap must survive
		// onto the event, or a reader cannot tell one refused row from a
		// thousand.
		if event.UnknownAttributionSourceCount != 1 {
			t.Errorf("unknown_attribution_source_count = %d, want 1", event.UnknownAttributionSourceCount)
		}
		if event.CandidateCount != 1 {
			t.Errorf("candidate_count = %d, want 1 -- a candidate existed, which is the whole reason this is not an empty result", event.CandidateCount)
		}
		if event.AdmittedCount != 0 {
			t.Errorf("admitted_count = %d, want 0", event.AdmittedCount)
		}
	})

	// THE PARTIAL CASE, which the review did not name and which is the same
	// defect one rung further down: with some rows admitted and others
	// refused, the ladder previously fell through to the default and reported
	// a clean `expanded`. An answer built from a set that silently lost
	// members is not whole.
	t.Run("some admitted, some refused", func(t *testing.T) {
		t.Parallel()
		admitted := []SubjectRef{{Kind: SubjectWorkItem, CanonicalID: "work_item:linear:WI-1", Label: "WI-1"}}
		event, scope := chaos5405UnknownAttributionScope(t, admitted, 2, 1)
		if event.Outcome == FactScopeExpanded {
			t.Errorf("outcome = %q -- a traversal that threw a row away reported a CLEAN expansion", event.Outcome)
		}
		if event.Outcome != FactScopeExpandedPartial {
			t.Errorf("outcome = %q, want %q", event.Outcome, FactScopeExpandedPartial)
		}
		if !scope.HasDisclosableGap() {
			t.Error("has_disclosable_gap = false on a partially refused traversal")
		}
		if event.AdmittedCount != 1 {
			t.Errorf("admitted_count = %d, want 1 -- the admitted row must still be served", event.AdmittedCount)
		}
	})

	// THE CONTROL, and it is what stops the two arms above from being
	// satisfied by "always report a gap": a traversal that refused NOTHING
	// still reports a clean expansion and discloses nothing.
	t.Run("control: nothing refused stays clean", func(t *testing.T) {
		t.Parallel()
		admitted := []SubjectRef{{Kind: SubjectWorkItem, CanonicalID: "work_item:linear:WI-2", Label: "WI-2"}}
		event, scope := chaos5405UnknownAttributionScope(t, admitted, 1, 0)
		if event.Outcome != FactScopeExpanded {
			t.Fatalf("outcome = %q, want %q -- the control is broken, not the rule", event.Outcome, FactScopeExpanded)
		}
		if scope.HasDisclosableGap() {
			t.Fatal("a traversal that refused nothing reported a disclosable gap")
		}
	})

	// And a MEASURED, RECOGNISED empty population is still allowed to say so.
	// Without this, a fix could satisfy everything above by never emitting
	// attempted_empty at all, which would destroy the distinction the census
	// exists to carry.
	t.Run("control: a measured recognised zero is still a proof of absence", func(t *testing.T) {
		t.Parallel()
		event, scope := chaos5405UnknownAttributionScope(t, nil, 0, 0)
		if event.Outcome != FactScopeAttemptedEmpty {
			t.Fatalf("outcome = %q, want %q -- a genuinely empty, fully measured traversal must still be able to report absence", event.Outcome, FactScopeAttemptedEmpty)
		}
		if scope.HasDisclosableGap() {
			t.Fatal("a measured empty population reported a gap -- disclosing one would train readers to ignore the disclosure")
		}
	})
}
