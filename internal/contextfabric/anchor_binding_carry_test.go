package contextfabric

import (
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/hintsource"
)

// carriedHintsOf is every engine-carried anchor hint the turn's last
// ResolveSubjects call received.
func carriedHintsOf(rig *anchorProbeRig) []SubjectHint {
	if len(rig.graph.calls) == 0 {
		return nil
	}
	var out []SubjectHint
	for _, hint := range rig.graph.calls[len(rig.graph.calls)-1].request.RequestedScope.SubjectHints {
		if hint.Source == string(hintsource.EngineCommittedAnchorCarry) {
			out = append(out, hint)
		}
	}
	return out
}

// bindingCarryTurns runs turn one (proves alpha, capturing it in the
// confirmed-need ledger and the binding) and a same-question follow-up with
// the carry switch as given; edit runs on the store before the follow-up and
// mutate on its request.
func bindingCarryTurns(t *testing.T, carryOn bool, edit func(*staticResultStore), mutate func(*InvestigationRequest), followUpResponse needTurnResponse) (*anchorProbeRig, anchorProbeTurn, anchorProbeTurn) {
	t.Helper()
	rig := newAnchorProbeRig(t, false)
	rig.interpreter.read(SubjectRepository, false)
	one := rig.turn(t, needTurnRequest("request_carry_one", true), identityProvenResponse(probeAlpha))
	if got := servedLedgerAnchor(one); got.ID != probeAlpha.CanonicalID {
		t.Fatalf("premise: turn one's ledger holds %+v; it must capture alpha", got)
	}
	rig.verify()
	rig.engine.anchorBindingCarryEnabled = carryOn
	if edit != nil {
		edit(rig.store)
	}
	request := continuingNeedTurn(needTurnRequest("request_carry_two", true), one.result.ResultID)
	if mutate != nil {
		mutate(&request)
	}
	two := rig.turn(t, request, followUpResponse)
	return rig, one, two
}

// TestAnAdmittedBindingCarriesItsAnchorIntoTheSameQuestion: the same
// question under the parent, with the binding admitted, carries the held
// identity into resolution, and the turn's own commit makes it effective.
func TestAnAdmittedBindingCarriesItsAnchorIntoTheSameQuestion(t *testing.T) {
	recommit := needTurnResponse{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{probeAlpha}}, bases: provenCommitBases(probeAlpha)}
	rig, _, two := bindingCarryTurns(t, true, nil, nil, recommit)
	hints := carriedHintsOf(rig)
	if len(hints) != 1 || hints[0].ID != probeAlpha.CanonicalID || hints[0].Kind != probeAlpha.Kind {
		t.Fatalf("carried hints = %+v, want exactly the admitted binding's alpha", hints)
	}
	line := two.transitions[0]
	if line.Admission != AnchorBindingAdmissionAdmitted || line.BoundAnchorCarry != BoundAnchorCarryInjected || line.ServedCount != string(CountPopulationScopeAnchorCommitted) || line.ServedCountAnchor.ID != probeAlpha.CanonicalID {
		t.Fatalf("line admission/carry/count = %s/%s/%s %s, want admitted/injected/anchor_committed on alpha", line.Admission, line.BoundAnchorCarry, line.ServedCount, line.ServedCountAnchor.ID)
	}
}

// TestARefusedBindingCarriesNothingEvenWithALedgerEntry: the binding's proof
// is older than the maximum age. Switched off, the ledger still carries its
// engine-committed alpha into resolution (the behaviour on main); switched on,
// nothing is carried, though the ledger holds the same entry.
func TestARefusedBindingCarriesNothingEvenWithALedgerEntry(t *testing.T) {
	expire := mutateStoredBindings(func(s *PersistedSemanticState) {
		editBindingMember(s, func(b *AnchorBinding) { b.ProvenAt = b.ProvenAt.Add(-DefaultAnchorBindingMaxAge - time.Second) })
	})
	for _, tc := range []struct {
		name      string
		carryOn   bool
		carry     BoundAnchorCarry
		wantHints int
	}{
		{"switched off: the ledger carries the expired anchor", false, BoundAnchorCarrySwitchOff, 1},
		{"switched on: a refused binding carries nothing", true, BoundAnchorCarryNotAdmitted, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig, _, two := bindingCarryTurns(t, tc.carryOn, expire, nil, emptyProbeResponse())
			line := two.transitions[0]
			if line.Admission != AnchorBindingAdmissionRefusedExpired || line.BoundAnchorCarry != tc.carry {
				t.Fatalf("line admission/carry = %s/%s, want refused_expired/%s", line.Admission, line.BoundAnchorCarry, tc.carry)
			}
			if hints := carriedHintsOf(rig); len(hints) != tc.wantHints {
				t.Fatalf("carried hints = %+v, want %d", hints, tc.wantHints)
			}
		})
	}
}

// TestAnAbsentBindingCarriesNothingEvenWithALedgerEntry: the parent's
// snapshot carries no binding while its ledger holds the engine-committed
// alpha; switched on, nothing is carried.
func TestAnAbsentBindingCarriesNothingEvenWithALedgerEntry(t *testing.T) {
	remove := mutateStoredBindings(func(s *PersistedSemanticState) { setBindingMember(s, nil) })
	rig, _, two := bindingCarryTurns(t, true, remove, nil, emptyProbeResponse())
	if line := two.transitions[0]; line.ParentBinding != AnchorBindingParentAbsent || line.BoundAnchorCarry != BoundAnchorCarryNone {
		t.Fatalf("line parent/carry = %s/%s, want absent/none", line.ParentBinding, line.BoundAnchorCarry)
	}
	if hints := carriedHintsOf(rig); len(hints) != 0 {
		t.Fatalf("carried hints = %+v, want none: the ledger's entry is never carried with the switch on", hints)
	}
}

// TestACallerHintOfTheBindingsKindSupersedesTheCarry: the caller names a
// repository itself; the binding carries nothing beside it.
func TestACallerHintOfTheBindingsKindSupersedesTheCarry(t *testing.T) {
	betaHint := func(r *InvestigationRequest) {
		r.RequestedScope.SubjectHints = []SubjectHint{{Kind: probeBeta.Kind, ID: probeBeta.CanonicalID, Label: probeBeta.Label, Source: "ask-dev"}}
	}
	rig, _, two := bindingCarryTurns(t, true, nil, betaHint, emptyProbeResponse())
	if line := two.transitions[0]; line.BoundAnchorCarry != BoundAnchorCarryCallerSuperseded {
		t.Fatalf("line carry = %s, want caller_superseded", line.BoundAnchorCarry)
	}
	if hints := carriedHintsOf(rig); len(hints) != 0 {
		t.Fatalf("carried hints = %+v, want none beside the caller's own", hints)
	}
}

// TestAnUncommittedCarryOffersTheRememberedAnchorFirst: the carried anchor
// is injected but resolution commits nothing and offers candidates; the
// clarification lists the remembered identity first, redeemable as a
// subject receipt.
func TestAnUncommittedCarryOffersTheRememberedAnchorFirst(t *testing.T) {
	offering := needTurnResponse{
		resolution: SubjectResolution{Committed: []SubjectRef{}, Candidates: []SubjectCandidate{{
			ReceiptID: "receipt_carry_beta", Subject: probeBeta, State: ResolutionAmbiguous,
			MatchedTerms: []string{"a"}, MatchReasons: []string{"matched"}, Confidence: 0.4, EvidenceRefIDs: []string{},
		}}},
		bases: CommitBasisSet{},
	}
	_, one, two := bindingCarryTurns(t, true, nil, nil, offering)
	line := two.transitions[0]
	if line.BoundAnchorCarry != BoundAnchorCarryRememberedOffered {
		t.Fatalf("line carry = %s, want remembered_offered", line.BoundAnchorCarry)
	}
	candidates := two.result.SubjectResolution.Candidates
	if len(candidates) != 2 || candidates[0].Subject.CanonicalID != probeAlpha.CanonicalID || candidates[1].Subject.CanonicalID != probeBeta.CanonicalID {
		t.Fatalf("served candidates = %+v, want alpha first, then beta", candidates)
	}
	if want := subjectSubstitutionReceiptID(one.result.ResultID, SubjectRef{Kind: probeAlpha.Kind, CanonicalID: probeAlpha.CanonicalID, Label: probeAlpha.CanonicalID}); candidates[0].ReceiptID != want {
		t.Fatalf("remembered receipt = %q, want %q minted from the proving result", candidates[0].ReceiptID, want)
	}
	if len(two.result.SubjectResolution.Committed) != 0 {
		t.Fatalf("committed = %+v, want none: the remembered anchor is an offer, never a silent serve", two.result.SubjectResolution.Committed)
	}
}

// TestTheRememberedOfferAndTheSubstitutionGuardNeverBothFire: on one turn at
// most one of the two lists the remembered subject. With zero commits the
// carry offers it and the guard does not fire; with a commit of another
// subject the guard fires and offers it, and the carry does not.
func TestTheRememberedOfferAndTheSubstitutionGuardNeverBothFire(t *testing.T) {
	betaCandidate := SubjectCandidate{
		ReceiptID: "receipt_exclusive_beta", Subject: probeBeta, State: ResolutionAmbiguous,
		MatchedTerms: []string{"a"}, MatchReasons: []string{"matched"}, Confidence: 0.4, EvidenceRefIDs: []string{},
	}
	for _, tc := range []struct {
		name      string
		response  needTurnResponse
		carry     BoundAnchorCarry
		guardFire bool
	}{
		{"zero commits: the carry offers it", needTurnResponse{resolution: SubjectResolution{Committed: []SubjectRef{}, Candidates: []SubjectCandidate{betaCandidate}}, bases: CommitBasisSet{}}, BoundAnchorCarryRememberedOffered, false},
		{"another subject committed: the guard offers it", identityProvenResponse(probeBeta), BoundAnchorCarryInjected, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, two := bindingCarryTurns(t, true, nil, nil, tc.response)
			line := two.transitions[0]
			if line.BoundAnchorCarry != tc.carry || line.SubstitutionGuard.Fired() != tc.guardFire {
				t.Fatalf("carry/guard fired = %s/%v, want %s/%v", line.BoundAnchorCarry, line.SubstitutionGuard.Fired(), tc.carry, tc.guardFire)
			}
			listed := 0
			for _, candidate := range two.result.SubjectResolution.Candidates {
				if candidate.Subject.CanonicalID == probeAlpha.CanonicalID {
					listed++
				}
			}
			if listed != 1 || two.result.SubjectResolution.Candidates[0].Subject.CanonicalID != probeAlpha.CanonicalID {
				t.Fatalf("served candidates = %+v, want the remembered alpha listed exactly once, first", two.result.SubjectResolution.Candidates)
			}
		})
	}
}
