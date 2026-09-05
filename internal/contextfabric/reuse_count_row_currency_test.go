package contextfabric

// IS THE REUSED COUNT ROW EVER STALE? THE PREMISE, MEASURED RATHER THAN READ.
//
// The reported defect is this: the reuse degrade drops cohort members and
// sets `Complete=false, Truncated=true`, while `hasCountOutcome` sees the
// STORED document's assembled-result `count` row and returns early -- so the
// backfill never re-states the cardinality and the served answer claims an
// exact census over a population it no longer carries.
//
// The mechanism is real; the FIRST STEP OF IT IS NOT REACHABLE. These tests
// establish that by execution rather than by reading, because an unreachable
// premise and a live defect look identical in a source trace, and a fix for
// dead code is a fix nothing can red.
//
// THE STRUCTURAL REASON, in one sentence: the degrade only ever assigns
// `member.EvidenceRefIDs`, evidence references are OPTIONAL on a cohort
// member under both the write and the legacy bounds, and `keepRefs` returns
// a non-nil empty slice -- so `strippingBrokeIt` is false for every member on
// every input, and the member-drop branch beside it cannot run.
//
// EVERY ASSERTION HERE CARRIES A POSITIVE CONTROL IN THE SAME RUN. A test
// that only shows a counter staying at zero cannot tell "the branch is
// unreachable" apart from "the fixture never reached the code at all", and
// this file exists precisely to tell those two apart. The control is a
// `Finding`, whose evidence references are REQUIRED (allowEmpty=false) and
// which therefore does break when stripped -- same degrade, same run, same
// missing set.

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// countCurrencyMemberRef and countCurrencyFindingRef are the two references
// the probe removes. They are DISTINCT so a per-carrier over-count cannot
// hide, and both are legal evidence-ref ids (8-256 chars, trimmed, no '|').
const (
	countCurrencyMemberRef  = "evidence_member_currency_a"
	countCurrencyFindingRef = "evidence_finding_currency_a"
)

// TestStrippingEveryEvidenceRefLeavesACohortMemberValid isolates the bound
// that makes the member-drop branch unreachable.
//
// It drives the CONTRACT's own validator, never a local restatement of the
// rule, and it checks both shapes the strip can produce: the non-nil empty
// slice `keepRefs` actually returns, and nil.
func TestStrippingEveryEvidenceRefLeavesACohortMemberValid(t *testing.T) {
	t.Parallel()

	member := CohortMember{
		Subject:          SubjectRef{Kind: SubjectTeam, CanonicalID: "team:CURRENCY", Label: "Currency"},
		Rank:             1,
		InclusionReasons: []string{"matched"},
		EvidenceRefIDs:   []string{countCurrencyMemberRef},
	}
	// NON-VACUITY: the member must be valid BEFORE the strip, or
	// `strippingBrokeIt` would be false for a reason this test is not about.
	if err := member.Validate(); err != nil {
		t.Fatalf("the fixture member does not validate before stripping: %v -- this probe measures a "+
			"valid-then-invalid transition and there is no valid state to start from", err)
	}

	// The exact value keepRefs returns when every ref is removed: a NEW,
	// non-nil, zero-length slice.
	stripped := member
	stripped.EvidenceRefIDs = make([]string, 0, 1)
	if err := stripped.Validate(); err != nil {
		t.Fatalf("a cohort member stripped to an empty ref list no longer validates: %v -- if this is now "+
			"true, members ARE droppable on the reuse degrade and the count row goes stale silently", err)
	}

	// And nil, the other shape a stored row can legitimately carry.
	nilled := member
	nilled.EvidenceRefIDs = nil
	if err := nilled.Validate(); err != nil {
		t.Fatalf("a cohort member with nil evidence refs no longer validates: %v", err)
	}

	// POSITIVE CONTROL, same file, same shape of edit: a Finding's evidence
	// references are REQUIRED, so the identical strip DOES break it. Without
	// this the two assertions above are indistinguishable from a validator
	// that accepts everything.
	finding := Finding{
		FindingID:      "finding_currency_01",
		Kind:           string(contractsv1.ContextFabricDriverCategoryRelationship),
		Summary:        "Acceptance work remains open.",
		Subjects:       []SubjectRef{{Kind: SubjectTeam, CanonicalID: "team:CURRENCY", Label: "Currency"}},
		EvidenceRefIDs: []string{countCurrencyFindingRef},
	}
	if err := finding.Validate(); err != nil {
		t.Fatalf("the control finding does not validate before stripping: %v", err)
	}
	strippedFinding := finding
	strippedFinding.EvidenceRefIDs = make([]string, 0, 1)
	if strippedFinding.Validate() == nil {
		t.Fatal("the CONTROL did not fire: a finding stripped of every evidence reference still validates, " +
			"so this file cannot tell an unreachable branch apart from a validator that accepts anything")
	}
}

// TestTheReuseDegradeNeverDropsACohortMember drives the whole degrade and
// reads the counter the reported defect depends on.
//
// The same run drops a FINDING, which is what makes the zero meaningful: the
// missing set reached the strip, the strip removed objects, and the member
// survived anyway.
func TestTheReuseDegradeNeverDropsACohortMember(t *testing.T) {
	t.Parallel()

	stored := storedResultWithCandidateEvidence()
	stored.Cohort = &Cohort{
		Kind: SubjectTeam, Rationale: "reuse currency probe", Complete: true,
		Members: []CohortMember{
			{Subject: SubjectRef{Kind: SubjectTeam, CanonicalID: "team:A", Label: "A"},
				Rank: 1, InclusionReasons: []string{"matched"},
				EvidenceRefIDs: []string{countCurrencyMemberRef}},
			{Subject: SubjectRef{Kind: SubjectTeam, CanonicalID: "team:B", Label: "B"},
				Rank: 2, InclusionReasons: []string{"matched"}},
		},
	}
	stored.RemainingWork = []Finding{{
		FindingID: "finding_currency_01",
		Kind:      string(contractsv1.ContextFabricDriverCategoryRelationship),
		Summary:   "Acceptance work remains open.",
		Subjects:  []SubjectRef{{Kind: SubjectTeam, CanonicalID: "team:A", Label: "A"}},
		// The CONTROL carrier: required refs, so this one breaks.
		EvidenceRefIDs: []string{countCurrencyFindingRef},
	}}
	// The stored answer's own count row, satisfied over the two members it
	// carries -- the strongest starting claim, and the one the defect says a
	// member drop would leave standing.
	stored.Completeness.Outcomes = append(stored.Completeness.Outcomes,
		RequirementOutcomeRow{
			Stage:       contractsv1.ContextFabricOutcomeStageAssembledResult,
			Requirement: string(ObligationCount) + "/" + string(SubjectRoleMember) + "/" + string(SubjectTeam),
			Obligation:  string(ObligationCount),
			Outcome:     contractsv1.ContextFabricRequirementSatisfied,
			Impact:      contractsv1.ContextFabricAnswerImpactNone,
			Served:      2, Declared: 2,
		})
	stored.Completeness = ComputeAnswerCompleteness(stored)

	missing := map[string]struct{}{
		countCurrencyMemberRef:  {},
		countCurrencyFindingRef: {},
	}
	degraded, counts, _, ok := degradeReusedResult(stored, missing)
	if !ok {
		t.Fatal("degradeReusedResult() refused; this fixture is meant to degrade")
	}

	// THE CONTROL, read FIRST: the missing set really did reach the strip and
	// really did drop an object. A zero member count means nothing without it.
	if counts.DroppedFindings == 0 {
		t.Fatalf("the CONTROL did not fire: the degrade dropped no finding even though its only evidence "+
			"reference was in the missing set. counts = %+v -- until this drops, the member assertion below "+
			"cannot distinguish an unreachable branch from a strip that never ran", counts)
	}
	if counts.Refs() == 0 {
		t.Fatalf("the degrade removed no references at all; counts = %+v", counts)
	}

	// THE PREMISE OF THE REPORTED DEFECT, measured.
	if counts.DroppedMembers != 0 {
		t.Fatalf("the reuse degrade dropped %d cohort member(s). The reported stale-count defect becomes "+
			"REACHABLE the moment this is non-zero: `hasCountOutcome` would then return early over a stored "+
			"row describing a population the served answer no longer carries", counts.DroppedMembers)
	}
	if degraded.Cohort == nil {
		t.Fatal("the degraded answer carries no cohort")
	}
	if got, want := len(degraded.Cohort.Members), 2; got != want {
		t.Fatalf("the degraded cohort carries %d members, want %d -- the population did not change", got, want)
	}
	// The cohort's coverage flags are untouched, which is the OTHER half of
	// the same fact: the branch that flips them is the member-drop branch.
	if !degraded.Cohort.Complete || degraded.Cohort.Truncated {
		t.Fatalf("the degraded cohort reports complete=%v truncated=%v; both move only inside the "+
			"member-drop branch, so this disagrees with a zero member drop",
			degraded.Cohort.Complete, degraded.Cohort.Truncated)
	}

	// And therefore the served count still describes the members served.
	rows := countOutcomeRows(degraded, contractsv1.ContextFabricOutcomeStageAssembledResult)
	if len(rows) != 1 {
		t.Fatalf("assembled-result `count` rows after degrade = %d, want exactly 1", len(rows))
	}
	if got, want := rows[0].Served, len(degraded.Cohort.Members); got != want {
		t.Fatalf("the served count says %d and the served answer carries %d members", got, want)
	}
}
