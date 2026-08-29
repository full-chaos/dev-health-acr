package contextfabric

import (
	"errors"
	"strings"
	"testing"
)

// CHAOS-4522. The live "Which teams are struggling, and why?" answer
// (discovered_cohort, kind team, org 70d529e0) hands synthesis 40 canonical
// facts, ALL of them team-subject, of which 17 are readiness|team:CHAOS --
// one row per work scope/day. Before this change, SynthesisDraft's claim
// grounding resolved a claim's (Kind, Subject) with a FIRST-match-wins
// lookup, so only ONE of those 17 could ever ground a claim. The first of
// them does not carry estimate_coverage_ratio, so every claim about the
// readiness coverage gap -- one of the four signal families the CHAOS-4398
// v2 cohort ranking is actually built on -- was rejected as "not
// canonically observed" while the value sat in the very next fact of the
// same group. Result: HTTP 422 synthesis_rejected, 6 attempts out of 6, on
// a question whose ranking had already succeeded server-side.
//
// The multiplicity is by design and the ranking layer already says so:
// cohort_ranking.go's findFact documents that "readiness/workload/
// deficiency aggregate across every fact of their kind ... because those
// producers can legitimately emit several". CHAOS-4398 gave the RANKING
// that treatment; the claim-grounding validator never got it.
//
// multiFactGroupFixture reproduces exactly that shape at unit scale: two
// canonical facts sharing one (Kind, Subject), the FIRST lacking the field
// the claim cites. Every test below runs against this fixture, so the whole
// file is red on aa214606e and green at tip.
func multiFactGroupFixture(t *testing.T) (SynthesisInput, SynthesisDraft) {
	t.Helper()
	input, draft := closureFixture()
	subject := input.Graph.Resolution.Committed[0]
	sparse := CanonicalFact{
		Kind: FactReadiness, Subject: subject,
		// No "release_ready" at all -- the live first-of-17 readiness fact
		// for team:CHAOS likewise carries no estimate_coverage_ratio.
		Fields:         map[string]FactValue{"backlog_size": IntegerFactValue(24)},
		EvidenceRefIDs: []string{"evidence_release_1234"}, SourceState: SourceAvailable,
		Source: "ops", SourceVersion: "v1",
	}
	input.Facts.Facts = append([]CanonicalFact{sparse}, input.Facts.Facts...)
	return input, draft
}

// TestClaimGroundsAgainstALaterFactOfTheSameKindAndSubject is the pinning
// test for the 422 itself: the claim is grounded in the SECOND fact of the
// group, and must be admitted.
func TestClaimGroundsAgainstALaterFactOfTheSameKindAndSubject(t *testing.T) {
	t.Parallel()
	input, draft := multiFactGroupFixture(t)
	if err := draft.ValidateAgainst(input); err != nil {
		t.Fatalf("ValidateAgainst() error = %v, want a claim grounded in a later fact of the same (kind, subject) group to be admitted", err)
	}
}

// TestClaimGroundsAgainstALaterFactWhenAnEarlierOneHoldsADifferentValue is
// the other half of the same defect: the field IS present on the first
// fact, but with a different value (two work scopes' readiness rows). The
// first-match-wins lookup called this "contradicts the canonical value";
// the claim is in fact grounded in a real observation.
func TestClaimGroundsAgainstALaterFactWhenAnEarlierOneHoldsADifferentValue(t *testing.T) {
	t.Parallel()
	input, draft := multiFactGroupFixture(t)
	input.Facts.Facts[0].Fields["release_ready"] = BooleanFactValue(true)
	if draft.ClaimedFacts[0].Value.Boolean == nil || *draft.ClaimedFacts[0].Value.Boolean {
		t.Fatalf("fixture drift: the claim under test must assert release_ready=false")
	}
	if err := draft.ValidateAgainst(input); err != nil {
		t.Fatalf("ValidateAgainst() error = %v, want a claim grounded in a later fact of the group to be admitted", err)
	}
}

// TestGroundingClosureStillRejectsAFieldNoFactInTheGroupObserves proves the
// closure did NOT become permissive: widening the search to the whole group
// must not admit a field none of the group carries.
func TestGroundingClosureStillRejectsAFieldNoFactInTheGroupObserves(t *testing.T) {
	t.Parallel()
	input, draft := multiFactGroupFixture(t)
	draft.ClaimedFacts[0].Field = "field_no_fact_in_the_group_carries"
	err := draft.ValidateAgainst(input)
	if err == nil || !strings.Contains(err.Error(), "not canonically observed") {
		t.Fatalf("ValidateAgainst() error = %v, want a not-canonically-observed rejection", err)
	}
	if got := SynthesisRejectionReasonOf(err); got != RejectionReasonClaimFieldUnobserved {
		t.Fatalf("rejection reason = %q, want %q", got, RejectionReasonClaimFieldUnobserved)
	}
}

// TestGroundingClosureStillRejectsAValueNoFactInTheGroupObserves is the
// value-level twin: every fact in the group carries the field, none carries
// the claimed value. CHAOS-3755's guarantee must survive CHAOS-4522.
func TestGroundingClosureStillRejectsAValueNoFactInTheGroupObserves(t *testing.T) {
	t.Parallel()
	input, draft := multiFactGroupFixture(t)
	input.Facts.Facts[0].Fields["release_ready"] = BooleanFactValue(true)
	input.Facts.Facts[1].Fields["release_ready"] = BooleanFactValue(true)
	err := draft.ValidateAgainst(input)
	if err == nil || !strings.Contains(err.Error(), "contradicts the canonical value") {
		t.Fatalf("ValidateAgainst() error = %v, want a contradicts-canonical-value rejection", err)
	}
	if got := SynthesisRejectionReasonOf(err); got != RejectionReasonClaimValueContradicts {
		t.Fatalf("rejection reason = %q, want %q", got, RejectionReasonClaimValueContradicts)
	}
}

// TestRowsAreAttachedFromTheFactThatGroundedTheClaim: once a group can be
// closed over, WHICH member supplied the table stops being incidental. A
// claim's scalar and its rendered table must describe ONE observation, so
// the rows must come from the fact that grounded the claim -- not from
// whichever member of the group happens to sort first.
func TestRowsAreAttachedFromTheFactThatGroundedTheClaim(t *testing.T) {
	t.Parallel()
	input, draft := multiFactGroupFixture(t)
	wrong := "from_the_ungrounded_first_fact"
	right := "from_the_grounding_fact"
	input.Facts.Facts[0].Fields["breakdown"] = FactValue{Rows: []FactValueRow{{Fields: map[string]FactValue{"row": StringFactValue(wrong)}}}}
	input.Facts.Facts[1].Fields["breakdown"] = FactValue{Rows: []FactValueRow{{Fields: map[string]FactValue{"row": StringFactValue(right)}}}}
	if err := draft.ValidateAgainst(input); err != nil {
		t.Fatalf("fixture drift: ValidateAgainst() error = %v", err)
	}
	claims, rowsCount, _, _ := attachCanonicalRows(cloneSlice(draft.ClaimedFacts), input.Facts.Facts)
	if rowsCount != 1 || len(claims[0].Rows) != 1 {
		t.Fatalf("rowsCount = %d, claim rows = %d, want 1 and 1", rowsCount, len(claims[0].Rows))
	}
	got := claims[0].Rows[0].Fields["row"].String
	if got == nil || *got != right {
		t.Fatalf("attached row = %v, want the row from the fact that grounded the claim (%q)", got, right)
	}
}

// TestRejectionCarriesTheRejectingClaimsOwnFactGroupSize is codex R1
// finding 1. The group size reported beside a rejection reason must be the
// REJECTING claim's, never a maximum over the draft: ValidateAgainst
// short-circuits at the first failing statement, so a scan of every claim
// can report the group of a LATER claim that was never evaluated -- which
// makes the documented "fact_group_size=1 means the model claimed a field
// that does not exist / >1 means a multi-fact grounding problem" reading
// wrong in exactly the case it exists to diagnose.
//
// The fixture below is that adversarial shape: claim ONE fails against a
// group of size 1, while a later, never-evaluated claim sits on a group of
// size 3.
func TestRejectionCarriesTheRejectingClaimsOwnFactGroupSize(t *testing.T) {
	t.Parallel()
	input, draft := multiFactGroupFixture(t)
	subject := input.Graph.Resolution.Committed[0]

	// A lone flow fact for this subject: group size 1.
	input.Facts.Facts = append(input.Facts.Facts, CanonicalFact{
		Kind: FactFlow, Subject: subject,
		Fields:         map[string]FactValue{"items_completed": IntegerFactValue(3)},
		EvidenceRefIDs: []string{"evidence_release_1234"}, SourceState: SourceAvailable,
		Source: "ops", SourceVersion: "v1",
	})
	// A third readiness fact, taking that group to 3.
	third := input.Facts.Facts[0]
	third.Fields = map[string]FactValue{"backlog_size": IntegerFactValue(7)}
	input.Facts.Facts = append(input.Facts.Facts, third)

	draft.ClaimedFacts = []ClaimedFact{
		// Rejects here: flow group size 1.
		{ClaimID: "claim_flow_rejects_first", Kind: FactFlow, Subject: subject,
			Field: "field_no_flow_fact_carries", Value: boolScalar(false)},
		// Never evaluated: readiness group size 3.
		{ClaimID: "claim_readiness_never_reached", Kind: FactReadiness, Subject: subject,
			Field: "release_ready", Value: boolScalar(false)},
	}
	draft.Drivers[0].Category = "flow"
	draft.Drivers[0].ClaimedFactIDs = []string{"claim_flow_rejects_first"}

	err := draft.ValidateAgainst(input)
	if err == nil {
		t.Fatal("ValidateAgainst() = nil, want a rejection on the first claim")
	}
	if got := SynthesisRejectionReasonOf(err); got != RejectionReasonClaimFieldUnobserved {
		t.Fatalf("rejection reason = %q, want %q", got, RejectionReasonClaimFieldUnobserved)
	}
	if got := SynthesisFactGroupSizeOf(err); got != 1 {
		t.Fatalf("fact group size = %d, want 1 (the REJECTING claim's flow group) -- 3 would be the later readiness claim ValidateAgainst never reached", got)
	}
}

// TestNonClaimRejectionsCarryNoFactGroupSize: a rule with no claim in play
// has no group to measure, and must report 0 rather than a number borrowed
// from somewhere else in the draft.
func TestNonClaimRejectionsCarryNoFactGroupSize(t *testing.T) {
	t.Parallel()
	input, draft := multiFactGroupFixture(t)
	draft.Status = "not_a_status"
	err := draft.ValidateAgainst(input)
	if got := SynthesisRejectionReasonOf(err); got != RejectionReasonStatusInvalid {
		t.Fatalf("rejection reason = %q, want %q", got, RejectionReasonStatusInvalid)
	}
	if got := SynthesisFactGroupSizeOf(err); got != 0 {
		t.Fatalf("fact group size = %d, want 0 for a rule with no claim in play", got)
	}
}

// TestBoundDiagnosisMirrorAgreesWithTheWidenedGrounding: bound_diagnosis.go
// is a statement-by-statement MIRROR of ValidateAgainst and must be updated
// with it (CHAOS-3784 round-4). A draft ValidateAgainst now ADMITS must not
// leave the mirror claiming a bound was violated.
func TestBoundDiagnosisMirrorAgreesWithTheWidenedGrounding(t *testing.T) {
	t.Parallel()
	input, draft := multiFactGroupFixture(t)
	if err := draft.ValidateAgainst(input); err != nil {
		t.Fatalf("fixture drift: ValidateAgainst() error = %v", err)
	}
	if bound, diagnosed := diagnoseSynthesisDraftBound(draft, input); diagnosed {
		t.Fatalf("diagnoseSynthesisDraftBound() = (%q, true), want no bound for a draft ValidateAgainst admits", bound)
	}
}

// TestEveryValidateAgainstRejectionCarriesAClosedVocabularyReason is the
// telemetry guarantee CHAOS-4522 adds. Before it, EVERY rejection collapsed
// into the receipt outcome "invalid_output" and the route classification
// "synthesis_rejected"; violated_bound named only the small subset
// attributable to a contracts/v1 bound, so the entire grounding and
// business-rule population -- including this ticket's own defect -- reached
// the operator with no name at all. Each row below plants one specific
// defect and asserts the reason it must carry.
func TestEveryValidateAgainstRejectionCarriesAClosedVocabularyReason(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		mutate func(*SynthesisInput, *SynthesisDraft)
		want   SynthesisRejectionReason
	}{
		{"status", func(_ *SynthesisInput, d *SynthesisDraft) { d.Status = "not_a_status" }, RejectionReasonStatusInvalid},
		{"direct_judgment", func(_ *SynthesisInput, d *SynthesisDraft) { d.DirectJudgment = "  " }, RejectionReasonDirectJudgmentMissing},
		{"deterministic_answer", func(_ *SynthesisInput, d *SynthesisDraft) { d.DeterministicAnswer = "" }, RejectionReasonDeterministicAnswerMissing},
		{"evidence", func(_ *SynthesisInput, d *SynthesisDraft) {
			d.EvidenceRefIDs = append(d.EvidenceRefIDs, "evidence_never_offered")
		}, RejectionReasonEvidenceUnknown},
		{"claim_duplicate", func(_ *SynthesisInput, d *SynthesisDraft) {
			d.ClaimedFacts = append(d.ClaimedFacts, d.ClaimedFacts[0])
		}, RejectionReasonClaimIDDuplicate},
		{"claim_rows", func(_ *SynthesisInput, d *SynthesisDraft) {
			value := "fabricated"
			d.ClaimedFacts[0].Rows = []ClaimedFactRow{{Fields: map[string]ScalarValue{"anything": {String: &value}}}}
		}, RejectionReasonClaimRowsModelAuthored},
		{"claim_subject", func(_ *SynthesisInput, d *SynthesisDraft) {
			d.ClaimedFacts[0].Subject = SubjectRef{Kind: SubjectProject, CanonicalID: "project_never_resolved", Label: "Elsewhere"}
		}, RejectionReasonClaimSubjectOutOfScope},
		{"claim_no_fact", func(_ *SynthesisInput, d *SynthesisDraft) { d.ClaimedFacts[0].Kind = FactHealth }, RejectionReasonClaimNoCanonicalFact},
		{"claim_field", func(_ *SynthesisInput, d *SynthesisDraft) {
			d.ClaimedFacts[0].Field = "field_no_fact_in_the_group_carries"
		}, RejectionReasonClaimFieldUnobserved},
		{"driver_path", func(_ *SynthesisInput, d *SynthesisDraft) {
			d.Drivers[0].PathIDs = []string{"path_never_offered"}
		}, RejectionReasonDriverPathUnknown},
		{"driver_evidence", func(_ *SynthesisInput, d *SynthesisDraft) {
			d.Drivers[0].EvidenceRefIDs = []string{"evidence_never_offered"}
		}, RejectionReasonDriverEvidenceUnknown},
		{"driver_subject", func(_ *SynthesisInput, d *SynthesisDraft) {
			d.Drivers[0].AffectedSubjects = []SubjectRef{{Kind: SubjectProject, CanonicalID: "project_never_resolved", Label: "Elsewhere"}}
		}, RejectionReasonDriverSubjectOutOfScope},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			input, draft := multiFactGroupFixture(t)
			tc.mutate(&input, &draft)
			err := draft.ValidateAgainst(input)
			if err == nil {
				t.Fatalf("ValidateAgainst() = nil, want a rejection to name %q", tc.want)
			}
			if got := SynthesisRejectionReasonOf(err); got != tc.want {
				t.Fatalf("rejection reason = %q, want %q (error: %v)", got, tc.want, err)
			}
			var rejection *SynthesisRejection
			if !errors.As(err, &rejection) {
				t.Fatalf("error does not carry a *SynthesisRejection: %v", err)
			}
		})
	}
}

// TestSynthesisRejectionReasonVocabularyIsClosed: an unrecognized reason
// must degrade to "unclassified" rather than reaching a log line verbatim
// -- the same fail-closed posture every other closed-vocabulary field in
// this package applies at its own boundary, and the reason a corpus string
// can never ride out on this field.
func TestSynthesisRejectionReasonVocabularyIsClosed(t *testing.T) {
	t.Parallel()
	if ValidSynthesisRejectionReason("a question the user typed") {
		t.Fatal("an arbitrary string must not be a valid rejection reason")
	}
	rogue := &SynthesisRejection{Reason: "a question the user typed", err: errors.New("x")}
	if got := SynthesisRejectionReasonOf(rogue); got != RejectionReasonUnclassified {
		t.Fatalf("SynthesisRejectionReasonOf(rogue) = %q, want %q", got, RejectionReasonUnclassified)
	}
	if got := SynthesisRejectionReasonOf(errors.New("not a rejection at all")); got != RejectionReasonUnclassified {
		t.Fatalf("SynthesisRejectionReasonOf(plain) = %q, want %q", got, RejectionReasonUnclassified)
	}
	if got := SynthesisRejectionReasonOf(nil); got != RejectionReasonUnclassified {
		t.Fatalf("SynthesisRejectionReasonOf(nil) = %q, want %q", got, RejectionReasonUnclassified)
	}
}

// TestClassifySynthesisRejectionPreservesTheReasonThroughItsWrapping: the
// production path wraps the ValidateAgainst error in ErrSynthesisRejected/
// ErrModelOutput (and possibly a ModelBoundViolation) before the route ever
// sees it. The reason must survive that, or the whole telemetry is dead on
// arrival at the only place it is read.
func TestClassifySynthesisRejectionPreservesTheReasonThroughItsWrapping(t *testing.T) {
	t.Parallel()
	input, draft := multiFactGroupFixture(t)
	draft.ClaimedFacts[0].Field = "field_no_fact_in_the_group_carries"
	cause := draft.ValidateAgainst(input)
	if cause == nil {
		t.Fatal("fixture drift: expected a rejection")
	}
	classified := ClassifySynthesisRejection(draft, input, cause)
	if !errors.Is(classified, ErrSynthesisRejected) || !errors.Is(classified, ErrModelOutput) {
		t.Fatalf("classified error lost its sentinels: %v", classified)
	}
	if got := SynthesisRejectionReasonOf(classified); got != RejectionReasonClaimFieldUnobserved {
		t.Fatalf("rejection reason through ClassifySynthesisRejection = %q, want %q", got, RejectionReasonClaimFieldUnobserved)
	}
}

// TestCohortMemberEvidenceIsInsideTheEvidenceClosure is CHAOS-4522's SECOND
// defect, the exact symmetric twin of the first and the one that kept the
// live answer at 422 even after the grounding closure was widened.
//
// synthesisSubjects has admitted input.Graph.Cohort.Members[].Subject since
// CHAOS-4398, and genkitruntime's synthesisInputFromDomain SHOWS the model
// the whole Cohort -- each member's EvidenceRefIDs included. The evidence
// closure in ValidateAgainst was never widened to match. So the engine
// displayed a member's evidence ref to the model and then rejected the
// answer as "references unknown evidence" when the model cited it. On the
// live three-team answer the cohort ranks team:gh:ops-team, whose only
// evidence ref is acr:v1:team:gh:ops-team, and no canonical fact exists for
// that member -- nothing else in the closure could ever have supplied it,
// so EVERY attempt at that answer died here, with rejection_reason
// evidence_unknown on all four post-fix replicates.
func TestCohortMemberEvidenceIsInsideTheEvidenceClosure(t *testing.T) {
	t.Parallel()
	input, draft := multiFactGroupFixture(t)
	member := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:gh:ops-team", Label: "Ops Team"}
	// A cohort member with NO canonical fact of its own -- the live shape:
	// the member is ranked, but nothing else in the closure names it.
	input.Graph.Cohort = &Cohort{
		Kind: SubjectTeam,
		Members: []CohortMember{{
			Subject: member, Rank: 1, InclusionReasons: []string{"ranked by the cohort formula"},
			EvidenceRefIDs: []string{"acr:v1:team:gh:ops-team"},
		}},
	}
	draft.EvidenceRefIDs = append(draft.EvidenceRefIDs, "acr:v1:team:gh:ops-team")
	if err := draft.ValidateAgainst(input); err != nil {
		t.Fatalf("ValidateAgainst() error = %v, want an evidence ref the engine's OWN cohort supplied to be citable", err)
	}
}

// TestEvidenceClosureStillRejectsARefNothingSupplied proves the widening did
// not turn the evidence closure into a rubber stamp: a ref no path, fact,
// candidate, graph context OR cohort member ever carried is still fatal.
func TestEvidenceClosureStillRejectsARefNothingSupplied(t *testing.T) {
	t.Parallel()
	input, draft := multiFactGroupFixture(t)
	input.Graph.Cohort = &Cohort{
		Kind: SubjectTeam,
		Members: []CohortMember{{
			Subject:          SubjectRef{Kind: SubjectTeam, CanonicalID: "team:gh:ops-team", Label: "Ops Team"},
			Rank:             1,
			InclusionReasons: []string{"ranked by the cohort formula"},
			EvidenceRefIDs:   []string{"acr:v1:team:gh:ops-team"},
		}},
	}
	draft.EvidenceRefIDs = append(draft.EvidenceRefIDs, "acr:v1:team:gh:invented-by-the-model")
	err := draft.ValidateAgainst(input)
	if err == nil || !strings.Contains(err.Error(), "unknown evidence") {
		t.Fatalf("ValidateAgainst() error = %v, want an unknown-evidence rejection", err)
	}
	if got := SynthesisRejectionReasonOf(err); got != RejectionReasonEvidenceUnknown {
		t.Fatalf("rejection reason = %q, want %q", got, RejectionReasonEvidenceUnknown)
	}
}
