package contextfabric

import (
	"context"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// Compare-and-drop. The shipped behaviour BLOCKS the carry whenever any
// redeemed member names a kind, which re-opens the loop it was meant to close:
// on a turn linked by a candidate receipt whose candidate does not commit, the
// carry never runs, the pool stays mixed, the kind offer is re-raised, and the
// next turn redeems a kind receipt again.
//
// Compare-and-drop runs the walk and discards its result ONLY when a redeemed
// member on the SUBJECT axis names a kind that differs from the carried one.
// Agreement keeps the carry, which is what keeps the loop closed.
//
// WHICH MEMBERS COMPARE. expected_kind, subject_candidate and subject_handle
// all name a SOUGHT subject's kind. subject_anchor does not: its kind is the
// SCOPE anchor's own kind, a different axis -- verified in code, it becomes an
// AnchorBinding used as the shadow round's anchor discriminator, never a
// filter on the candidate pool, and the package's own fixtures pair a
// repository anchor with a pull-request question. So an anchor naming
// `project` beside a carried `repository` is two axes, not a disagreement.

func kindCarryDropCanon(members ...confirmedStructureMember) requestStructureCanonicalization {
	return requestStructureCanonicalization{Confirmed: members}
}

func member(kind contractsv1.ContextFabricStructureNeedKind, applied contractsv1.ContextFabricSubjectKind) confirmedStructureMember {
	return confirmedStructureMember{Member: kind, AppliedValue: "subject_x", AppliedKind: applied}
}

// TestCarryDrop_AgreeingCandidateKeepsTheCarry is the reviewer's exact trace,
// and the case the shipped block variant loses. The caller picks a candidate of
// the SAME kind the chain already confirmed; the carry must survive, so the
// pool narrows and the kind offer is not raised again.
func TestCarryDrop_AgreeingCandidateKeepsTheCarry(t *testing.T) {
	t.Parallel()
	canon := kindCarryDropCanon(member(contractsv1.ContextFabricStructureNeedSubjectCandidate, contractsv1.ContextFabricSubjectRepository))
	carry := kindCarryResult{Kind: contractsv1.ContextFabricSubjectRepository, SourceResultID: "result_origin", Outcome: KindCarryHit}

	if statedExpectedKindThisTurn(InvestigationRequest{}, canon) {
		t.Fatal("a candidate receipt must no longer BLOCK the walk outright -- it is a comparator, not a gate")
	}
	got := effectiveConfirmedKind(canon.Confirmed, applyCarryDrop(canon.Confirmed, carry))
	if got == nil || got.Kind != contractsv1.ContextFabricSubjectRepository {
		t.Fatalf("effectiveConfirmedKind = %#v, want the carried repository kept: the caller picked a candidate of the SAME kind, so nothing disagrees and the loop must stay closed", got)
	}
}

// TestCarryDrop_DifferingSubjectAxisMemberDropsTheCarry: the caller's own pick
// wins, and it wins by the carry standing down rather than by a veto. The turn
// then resolves as though no carry existed.
func TestCarryDrop_DifferingSubjectAxisMemberDropsTheCarry(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		member contractsv1.ContextFabricStructureNeedKind
	}{
		{"candidate", contractsv1.ContextFabricStructureNeedSubjectCandidate},
		{"handle", contractsv1.ContextFabricStructureNeedSubjectHandle},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			canon := kindCarryDropCanon(member(tc.member, contractsv1.ContextFabricSubjectTeam))
			carry := kindCarryResult{Kind: contractsv1.ContextFabricSubjectRepository, SourceResultID: "result_origin", Outcome: KindCarryHit}
			if got := effectiveConfirmedKind(canon.Confirmed, applyCarryDrop(canon.Confirmed, carry)); got != nil {
				t.Fatalf("effectiveConfirmedKind = %#v, want nil: this turn's own %s names team, so an inherited repository must stand down rather than filter the caller's pick out of the pool", got, tc.name)
			}
		})
	}
}

// TestCarryDrop_AnchorIsNotAComparator pins the axis distinction. An anchor's
// kind is the SCOPE's kind, not the sought subject's, so it can differ from a
// carried kind without any disagreement existing. Treating it as a comparator
// would drop carries on exactly the scoped questions this mechanism exists for
// ("which repositories does the OPS TEAM own" -- anchor team, sought
// repository, no conflict).
func TestCarryDrop_AnchorIsNotAComparator(t *testing.T) {
	t.Parallel()
	canon := kindCarryDropCanon(member(contractsv1.ContextFabricStructureNeedSubjectAnchor, contractsv1.ContextFabricSubjectTeam))
	carry := kindCarryResult{Kind: contractsv1.ContextFabricSubjectRepository, SourceResultID: "result_origin", Outcome: KindCarryHit}

	got := effectiveConfirmedKind(canon.Confirmed, applyCarryDrop(canon.Confirmed, carry))
	if got == nil || got.Kind != contractsv1.ContextFabricSubjectRepository {
		t.Fatalf("effectiveConfirmedKind = %#v, want the carried repository KEPT: a team ANCHOR and a repository sought-kind are two axes, not a disagreement", got)
	}
}

// TestCarryDrop_SubjectAxisTieDropsTheCarry: two subject-axis members naming
// different kinds on one turn. An inherited value never breaks a same-turn tie,
// even when it agrees with one side of it.
func TestCarryDrop_SubjectAxisTieDropsTheCarry(t *testing.T) {
	t.Parallel()
	canon := kindCarryDropCanon(
		member(contractsv1.ContextFabricStructureNeedSubjectCandidate, contractsv1.ContextFabricSubjectRepository),
		member(contractsv1.ContextFabricStructureNeedSubjectHandle, contractsv1.ContextFabricSubjectTeam),
	)
	carry := kindCarryResult{Kind: contractsv1.ContextFabricSubjectRepository, SourceResultID: "result_origin", Outcome: KindCarryHit}
	if got := effectiveConfirmedKind(canon.Confirmed, applyCarryDrop(canon.Confirmed, carry)); got != nil {
		t.Fatalf("effectiveConfirmedKind = %#v, want nil: this turn's own receipts disagree with each other, so an inherited value has no business picking the winner even though it matches one of them", got)
	}
}

// TestCarryDrop_DroppedCarryIsNotDisclosed. A dropped carry was not used, so
// the wire must not say it was. This is the round-1 disclosure class read the
// other way round: then a carry that APPLIED went undisclosed; here a carry
// that did NOT apply must not be disclosed as applied.
func TestCarryDrop_DroppedCarryIsNotDisclosed(t *testing.T) {
	t.Parallel()
	dropped := kindCarryResult{Kind: contractsv1.ContextFabricSubjectRepository, SourceResultID: "result_origin", Outcome: KindCarryDroppedRedeemedKindDiffers}
	if entry := composeCarriedKindEntry(dropped); entry != nil {
		t.Fatalf("composeCarriedKindEntry(dropped) = %#v, want nil: a carry the resolution never used must not appear on the wire as applied", entry)
	}
}

// TestCarryDrop_RegressionPinsThatMustNotMove: the guarantees #413 shipped.
func TestCarryDrop_RegressionPinsThatMustNotMove(t *testing.T) {
	t.Parallel()
	// An explicit expected_kinds still BLOCKS outright, any count -- it is the
	// caller naming the sought kind directly, not picking a subject.
	plural := InvestigationRequest{ExpectedKinds: []contractsv1.ContextFabricSubjectKind{
		contractsv1.ContextFabricSubjectTeam, contractsv1.ContextFabricSubjectProject,
	}}
	if !statedExpectedKindThisTurn(plural, requestStructureCanonicalization{}) {
		t.Fatal("plural explicit expected_kinds must still block the carry outright")
	}
	// A same-turn kind receipt still wins over a carried one.
	own := kindCarryDropCanon(confirmedStructureMember{
		Member: contractsv1.ContextFabricStructureNeedExpectedKind, AppliedValue: string(contractsv1.ContextFabricSubjectProject),
	})
	if !statedExpectedKindThisTurn(InvestigationRequest{}, own) {
		t.Fatal("a same-turn expected_kind receipt must still block the carry outright")
	}
	if got := effectiveConfirmedKind(own.Confirmed, kindCarryResult{Kind: contractsv1.ContextFabricSubjectTeam, Outcome: KindCarryHit}); got == nil || got.Kind != contractsv1.ContextFabricSubjectProject {
		t.Fatalf("effectiveConfirmedKind = %#v, want this turn's own project", got)
	}
	// A miss still yields nothing, and discloses nothing.
	if got := effectiveConfirmedKind(nil, kindCarryResult{Outcome: KindCarryMissNoReference}); got != nil {
		t.Fatalf("effectiveConfirmedKind(miss) = %#v, want nil", got)
	}
	_ = context.Background()
}
