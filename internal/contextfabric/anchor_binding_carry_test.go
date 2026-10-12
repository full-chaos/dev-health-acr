package contextfabric

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/hintsource"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
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
	return bindingCarryTurnsWith(t, carryTurnOptions{carryOn: carryOn, edit: edit, mutate: mutate}, followUpResponse)
}

// carryTurnOptions is bindingCarryTurns' follow-up setup: the two switches,
// a store edit, a request mutation, and a rig hook run just before the
// follow-up.
type carryTurnOptions struct {
	carryOn, pendingOn bool
	edit               func(*staticResultStore)
	mutate             func(*InvestigationRequest)
	before             func(*anchorProbeRig)
}

func bindingCarryTurnsWith(t *testing.T, o carryTurnOptions, followUpResponse needTurnResponse) (*anchorProbeRig, anchorProbeTurn, anchorProbeTurn) {
	t.Helper()
	rig := newAnchorProbeRig(t, false)
	rig.interpreter.read(SubjectRepository, false)
	one := rig.turn(t, needTurnRequest("request_carry_one", true), identityProvenResponse(probeAlpha))
	if got := servedLedgerAnchor(one); got.ID != probeAlpha.CanonicalID {
		t.Fatalf("premise: turn one's ledger holds %+v; it must capture alpha", got)
	}
	rig.verify()
	rig.engine.anchorBindingCarryEnabled, rig.engine.anchorBindingPendingCarryEnabled = o.carryOn, o.pendingOn
	if o.edit != nil {
		o.edit(rig.store)
	}
	if o.before != nil {
		o.before(rig)
	}
	request := continuingNeedTurn(needTurnRequest("request_carry_two", true), one.result.ResultID)
	if o.mutate != nil {
		o.mutate(&request)
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
	// The ledger entry naming the same identity stays applied: the
	// selection is alpha, the served entry is applied, and the outgoing
	// ledger keeps it.
	if call := rig.graph.calls[len(rig.graph.calls)-1]; call.anchor == nil || call.anchor.CanonicalID != probeAlpha.CanonicalID {
		t.Fatalf("resolution anchor selection = %+v, want the agreeing ledger alpha", call.anchor)
	}
	got := memberEntries(two.result, contractsv1.ContextFabricStructureNeedSubjectAnchor)
	if len(got) != 1 || got[0].Disposition != contractsv1.ContextFabricStructureDispositionApplied || got[0].AppliedValue != probeAlpha.CanonicalID {
		t.Fatalf("served subject_anchor entry = %+v, want alpha applied", got)
	}
	if anchor := servedLedgerAnchor(two); anchor.ID != probeAlpha.CanonicalID {
		t.Fatalf("outgoing ledger anchor = %+v, want alpha kept", anchor)
	}
}

// TestALedgerAnchorTheBindingDisagreesWithIsVetoedAsAConflict: the admitted
// binding holds beta while the ledger holds alpha. Beta is carried; alpha is
// withheld from resolution and served as vetoed_conflict.
func TestALedgerAnchorTheBindingDisagreesWithIsVetoedAsAConflict(t *testing.T) {
	holdBeta := mutateStoredBindings(func(s *PersistedSemanticState) {
		editBindingMember(s, func(b *AnchorBinding) {
			b.Kind, b.CanonicalID, b.Witness = probeBeta.Kind, probeBeta.CanonicalID, []string{string(probeBeta.Kind) + ":" + probeBeta.CanonicalID}
		})
	})
	rig, _, two := bindingCarryTurns(t, true, holdBeta, nil, emptyProbeResponse())
	if line := two.transitions[0]; line.Admission != AnchorBindingAdmissionAdmitted || line.BoundAnchorCarry != BoundAnchorCarryInjected {
		t.Fatalf("line admission/carry = %s/%s, want admitted/injected", line.Admission, line.BoundAnchorCarry)
	}
	if hints := carriedHintsOf(rig); len(hints) != 1 || hints[0].ID != probeBeta.CanonicalID {
		t.Fatalf("carried hints = %+v, want the binding's beta only", hints)
	}
	if call := rig.graph.calls[len(rig.graph.calls)-1]; call.anchor != nil {
		t.Fatalf("resolution anchor selection = %+v, want none: the disagreeing ledger alpha is withheld", call.anchor)
	}
	got := memberEntries(two.result, contractsv1.ContextFabricStructureNeedSubjectAnchor)
	if len(got) != 1 || got[0].Disposition != contractsv1.ContextFabricStructureDispositionVetoedConflict || got[0].AppliedValue != probeAlpha.CanonicalID {
		t.Fatalf("served subject_anchor entry = %+v, want alpha vetoed_conflict", got)
	}
}

// TestTheRememberedOfferListsTheHeldIdentityOnce: the resolver lists the
// held identity twice; the served list names it once, first.
func TestTheRememberedOfferListsTheHeldIdentityOnce(t *testing.T) {
	alpha, again, beta := probeOffer("receipt_dup_alpha", probeAlpha), probeOffer("receipt_dup_alpha_again", probeAlpha), probeOffer("receipt_dup_beta", probeBeta)
	_, _, two := bindingCarryTurns(t, true, nil, nil, offeringResponse(beta, alpha, again))
	served := two.result.SubjectResolution.Candidates
	if len(served) != 2 || served[0].ReceiptID != alpha.ReceiptID || served[1].ReceiptID != beta.ReceiptID {
		t.Fatalf("served candidates = %+v, want alpha once, first, then beta", served)
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

// probeOffer is an ambiguous candidate the resolver offered, with its own
// receipt.
func probeOffer(receipt string, subject SubjectRef) SubjectCandidate {
	return SubjectCandidate{
		ReceiptID: receipt, Subject: subject, State: ResolutionAmbiguous, Provider: "github",
		MatchedTerms: []string{"a"}, MatchReasons: []string{"matched"}, Confidence: 0.4, EvidenceRefIDs: []string{},
	}
}

// offeringResponse commits nothing and offers candidates in the given
// order, with the generic prompt the resolver builds from them.
func offeringResponse(candidates ...SubjectCandidate) needTurnResponse {
	return needTurnResponse{
		resolution: SubjectResolution{Committed: []SubjectRef{}, Candidates: candidates, ClarificationPrompt: ClarificationPrompt(candidates)},
		bases:      CommitBasisSet{},
	}
}

// TestAnUncommittedCarryOffersTheRememberedAnchorFirst: the carried anchor
// is injected, resolution commits nothing and offers it second; the served
// clarification lists it first, with the receipt, label and provider the
// resolver gave it, and the prompt names it first too.
func TestAnUncommittedCarryOffersTheRememberedAnchorFirst(t *testing.T) {
	alpha, beta := probeOffer("receipt_carry_alpha", probeAlpha), probeOffer("receipt_carry_beta", probeBeta)
	_, _, two := bindingCarryTurns(t, true, nil, nil, offeringResponse(beta, alpha))
	if line := two.transitions[0]; line.BoundAnchorCarry != BoundAnchorCarryRememberedOffered {
		t.Fatalf("line carry = %s, want remembered_offered", line.BoundAnchorCarry)
	}
	served := two.result.SubjectResolution
	if len(served.Candidates) != 2 || !reflect.DeepEqual(served.Candidates[0], alpha) || !reflect.DeepEqual(served.Candidates[1], beta) {
		t.Fatalf("served candidates = %+v, want the resolver's own alpha first, then beta", served.Candidates)
	}
	if want := ClarificationPrompt([]SubjectCandidate{alpha, beta}); served.ClarificationPrompt != want {
		t.Fatalf("served prompt = %q, want %q naming the remembered identity first", served.ClarificationPrompt, want)
	}
	if len(served.Committed) != 0 {
		t.Fatalf("committed = %+v, want none: the remembered anchor is an offer, never a silent serve", served.Committed)
	}
}

// TestACarryTheResolverDidNotOfferIsNotOffered: resolution commits nothing
// and offers only beta; the remembered identity is not added from memory,
// so the served list and prompt stay the resolver's own.
func TestACarryTheResolverDidNotOfferIsNotOffered(t *testing.T) {
	beta := probeOffer("receipt_carry_beta", probeBeta)
	response := offeringResponse(beta)
	_, _, two := bindingCarryTurns(t, true, nil, nil, response)
	if line := two.transitions[0]; line.BoundAnchorCarry != BoundAnchorCarryInjected {
		t.Fatalf("line carry = %s, want injected: nothing to offer", line.BoundAnchorCarry)
	}
	served := two.result.SubjectResolution
	if len(served.Candidates) != 1 || !reflect.DeepEqual(served.Candidates[0], beta) || served.ClarificationPrompt != response.resolution.ClarificationPrompt {
		t.Fatalf("served candidates/prompt = %+v %q, want the resolver's own beta and prompt", served.Candidates, served.ClarificationPrompt)
	}
}

// TestTheRememberedOfferIsRedeemedThroughItsOwnReceipt: three turns. Turn
// two's binding has turn one as its origin and offers the remembered alpha
// first; turn three redeems that offer's receipt from turn two, the
// immediate parent, and the receipt's hint reaches resolution in place of a
// carry.
func TestTheRememberedOfferIsRedeemedThroughItsOwnReceipt(t *testing.T) {
	alpha, beta := probeOffer("receipt_carry_alpha", probeAlpha), probeOffer("receipt_carry_beta", probeBeta)
	rig, one, two := bindingCarryTurns(t, true, nil, nil, offeringResponse(beta, alpha))
	if two.result.SubjectResolution.Candidates[0].ReceiptID != alpha.ReceiptID {
		t.Fatalf("premise: turn two offers %+v first, want alpha", two.result.SubjectResolution.Candidates[0])
	}
	if two.saved == nil || bindingMember(two.saved) == nil || bindingMember(two.saved).OriginResultID != one.result.ResultID {
		t.Fatalf("premise: turn two's binding must keep turn one as its origin")
	}
	request := continuingNeedTurnAgain(needTurnRequest("request_carry_three", true), needTurnRequest("request_carry_two", true), two.result.ResultID)
	request.PriorSubjectReceipts = []BoundSubjectReceipt{{ResultID: two.result.ResultID, ReceiptID: alpha.ReceiptID}}
	three := rig.turn(t, request, needTurnResponse{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{probeAlpha}}, bases: provenCommitBases(probeAlpha)})
	hints := rig.graph.calls[len(rig.graph.calls)-1].request.RequestedScope.SubjectHints
	if len(hints) != 1 || hints[0].ID != probeAlpha.CanonicalID || hints[0].Source != string(hintsource.PriorSubjectReceipt) {
		t.Fatalf("turn three hints = %+v, want only the redeemed receipt's alpha", hints)
	}
	if line := three.transitions[0]; line.BoundAnchorCarry != BoundAnchorCarryCallerSuperseded {
		t.Fatalf("turn three carry = %s, want caller_superseded by the redeemed receipt", line.BoundAnchorCarry)
	}
	if committed := three.result.SubjectResolution.Committed; len(committed) != 1 || committed[0].CanonicalID != probeAlpha.CanonicalID {
		t.Fatalf("turn three committed = %+v, want alpha", committed)
	}
}

// TestTheRememberedOfferAndTheSubstitutionGuardNeverBothFire: on one turn at
// most one of the two lists the remembered subject. With zero commits the
// carry offers it and the guard does not fire; with a commit of another
// subject the guard fires and offers it, and the carry does not.
func TestTheRememberedOfferAndTheSubstitutionGuardNeverBothFire(t *testing.T) {
	alpha, beta := probeOffer("receipt_exclusive_alpha", probeAlpha), probeOffer("receipt_exclusive_beta", probeBeta)
	for _, tc := range []struct {
		name      string
		response  needTurnResponse
		carry     BoundAnchorCarry
		guardFire bool
	}{
		{"zero commits: the carry offers it", offeringResponse(beta, alpha), BoundAnchorCarryRememberedOffered, false},
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

// expiredBinding ages the stored binding's proof past the maximum age, so
// admission refuses it while the ledger still holds the same anchor.
var expiredBinding = mutateStoredBindings(func(s *PersistedSemanticState) {
	editBindingMember(s, func(b *AnchorBinding) { b.ProvenAt = b.ProvenAt.Add(-DefaultAnchorBindingMaxAge - time.Second) })
})

// TestAWithheldLedgerAnchorReachesNoResolutionCall: the binding is refused
// (expired) while the ledger still holds the engine-committed alpha.
// Switched off, the ledger's alpha reaches the decisive resolution as the
// anchor selection. Switched on, it does not, the served carried entry
// discloses it as vetoed_unresolved, and the outgoing ledger drops it.
//
// The follow-up is a continuation, so it carries its parent's stated window
// and never reaches the window gate: this row covers the decisive call only.
// The exits that end before resolution are
// TestAWithheldLedgerAnchorOnAnExitBeforeResolution's.
func TestAWithheldLedgerAnchorReachesNoResolutionCall(t *testing.T) {
	for _, tc := range []struct {
		name       string
		carryOn    bool
		wantAnchor bool
	}{
		{"switched off", false, true},
		{"switched on", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig, _, two := bindingCarryTurns(t, tc.carryOn, expiredBinding, nil, emptyProbeResponse())
			if two.transitions[0].Evaluation != AnchorBindingEvaluationResolved {
				t.Fatalf("premise: evaluation = %s, want the decisive resolution", two.transitions[0].Evaluation)
			}
			call := rig.graph.calls[len(rig.graph.calls)-1]
			if got := call.anchor != nil && call.anchor.CanonicalID == probeAlpha.CanonicalID; got != tc.wantAnchor {
				t.Fatalf("resolution anchor selection = %+v, want alpha=%v", call.anchor, tc.wantAnchor)
			}
			if !tc.carryOn {
				return
			}
			got := memberEntries(two.result, contractsv1.ContextFabricStructureNeedSubjectAnchor)
			if len(got) != 1 || got[0].Disposition != contractsv1.ContextFabricStructureDispositionVetoedUnresolved || got[0].AppliedValue != probeAlpha.CanonicalID {
				t.Fatalf("served subject_anchor entry = %+v, want alpha vetoed_unresolved", got)
			}
			if anchor := servedLedgerAnchor(two); anchor.identifies() {
				t.Fatalf("outgoing ledger anchor = %+v, want none: a withheld entry is not inherited", anchor)
			}
		})
	}
}

// TestAWithheldLedgerAnchorOnAnExitBeforeResolution: the same refused
// binding, on the two exits a same-question continuation can reach before
// resolution: the frame-gate refusal and graph not projected. Switched off,
// the entry reads not_evaluated and passes to the next turn (the behaviour
// on main). Switched on, it reads vetoed_unresolved, is not passed on, and
// no resolution call receives it.
func TestAWithheldLedgerAnchorOnAnExitBeforeResolution(t *testing.T) {
	refuseFrame := func(rig *anchorProbeRig) {
		rig.interpreter.outcome.Gate = FrameGate{Outcome: FrameGateRefusedBasis, RefuseBasis: CohortMemberKindUnservable, DeclaredMemberKind: SubjectTeam}
	}
	notProjected := needTurnResponse{err: fmt.Errorf("query context graph: %w", ErrGraphNotProjected)}
	for _, exit := range []struct {
		name     string
		before   func(*anchorProbeRig)
		response needTurnResponse
		calls    int
	}{
		{"frame gate", refuseFrame, emptyProbeResponse(), 0},
		{"graph not projected", nil, notProjected, 1},
	} {
		for _, tc := range []struct {
			carryOn     bool
			disposition contractsv1.ContextFabricStructureDisposition
			passedOn    bool
		}{
			{false, contractsv1.ContextFabricStructureDispositionNotEvaluated, true},
			{true, contractsv1.ContextFabricStructureDispositionVetoedUnresolved, false},
		} {
			t.Run(fmt.Sprintf("%s carry=%v", exit.name, tc.carryOn), func(t *testing.T) {
				var mark int
				before := func(rig *anchorProbeRig) {
					mark = len(rig.graph.calls)
					if exit.before != nil {
						exit.before(rig)
					}
				}
				rig, _, two := bindingCarryTurnsWith(t, carryTurnOptions{carryOn: tc.carryOn, edit: expiredBinding, before: before}, exit.response)
				calls := rig.graph.calls[mark:]
				if len(calls) != exit.calls {
					t.Fatalf("premise: resolution calls = %d, want %d on this exit", len(calls), exit.calls)
				}
				for _, call := range calls {
					if got := call.anchor != nil; got != !tc.carryOn {
						t.Fatalf("resolution anchor selection = %+v, want present=%v", call.anchor, !tc.carryOn)
					}
				}
				got := memberEntries(two.result, contractsv1.ContextFabricStructureNeedSubjectAnchor)
				if len(got) != 1 || got[0].Disposition != tc.disposition || got[0].AppliedValue != probeAlpha.CanonicalID {
					t.Fatalf("served subject_anchor entry = %+v, want alpha %s", got, tc.disposition)
				}
				if passed := servedLedgerAnchor(two).ID == probeAlpha.CanonicalID; passed != tc.passedOn {
					t.Fatalf("outgoing ledger anchor = %+v, want passed on=%v", servedLedgerAnchor(two), tc.passedOn)
				}
			})
		}
	}
}

// anchorOfferingResponse commits nothing, offers beta as a candidate, and
// raises the subject_anchor need with one anchor offer.
func anchorOfferingResponse() needTurnResponse {
	response := offeringResponse(probeOffer("receipt_anchor_offer_beta", probeBeta))
	response.material.Missing = []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedSubjectAnchor}
	response.material.AnchorOptions = []AnchorOption{{Label: "need-r2", Kind: SubjectRepository, CanonicalID: "repository:need-r2", MatchedTermHash: "aa11bb22cc33dd44ee55ff66", OfferSource: "engine"}}
	return response
}

// TestARedeemedAnchorReceiptOutranksTheCarry: three turns of one question.
// Turn two keeps the binding on alpha (origin turn one) and offers an anchor;
// turn three redeems that anchor receipt for another repository. The binding
// is admitted but injects nothing, and resolution's anchor selection is the
// receipt's.
func TestARedeemedAnchorReceiptOutranksTheCarry(t *testing.T) {
	rig, one, two := bindingCarryTurns(t, true, nil, nil, anchorOfferingResponse())
	if two.result.StructureNeeds == nil || len(two.result.StructureNeeds.AnchorOptions) != 1 {
		t.Fatalf("premise: turn two must offer one anchor, got %+v", two.result.StructureNeeds)
	}
	if held := bindingMember(two.saved); held == nil || held.CanonicalID != probeAlpha.CanonicalID || held.OriginResultID != one.result.ResultID {
		t.Fatalf("premise: turn two's binding must hold alpha from turn one, got %+v", held)
	}
	rig.engine.anchorVerifier = func(context.Context, string, contractsv1.ContextFabricSubjectKind, string, string) (bool, AnchorVerificationReason) {
		return true, AnchorVerificationValid
	}
	rig.engine.anchorMembershipVerifier = func(context.Context, storage.Principal, RequestedScope, ResolvedGraphBinding, contractsv1.ContextFabricSubjectKind, string, string) (bool, AnchorVerificationReason) {
		return true, AnchorVerificationValid
	}
	request := continuingNeedTurnAgain(needTurnRequest("request_carry_three", true), needTurnRequest("request_carry_two", true), two.result.ResultID)
	request.PriorAnchorReceipts = []BoundSubjectReceipt{{ResultID: two.result.ResultID, ReceiptID: two.result.StructureNeeds.AnchorOptions[0].ReceiptID}}
	three := rig.turn(t, request, emptyProbeResponse())
	if line := three.transitions[0]; line.Admission != AnchorBindingAdmissionAdmitted || line.BoundAnchorCarry != BoundAnchorCarryReceiptSuperseded {
		t.Fatalf("line admission/carry = %s/%s, want admitted/receipt_superseded", line.Admission, line.BoundAnchorCarry)
	}
	if hints := carriedHintsOf(rig); len(hints) != 0 {
		t.Fatalf("carried hints = %+v, want none beside the redeemed receipt", hints)
	}
	call := rig.graph.calls[len(rig.graph.calls)-1]
	if call.anchor == nil || call.anchor.CanonicalID != "repository:need-r2" {
		t.Fatalf("resolution anchor selection = %+v, want the redeemed receipt's", call.anchor)
	}
}

// TestACarryAtTheHintCapIsRecordedAsCapFull: the caller already sends the
// maximum number of hints, none of the binding's kind; the due carry is not
// appended and the line says cap_full, not injected.
func TestACarryAtTheHintCapIsRecordedAsCapFull(t *testing.T) {
	full := func(r *InvestigationRequest) {
		r.RequestedScope.SubjectHints = make([]SubjectHint, 0, maxCarrySubjectHints)
		for i := 0; i < maxCarrySubjectHints; i++ {
			id := fmt.Sprintf("team:cap-%02d", i)
			r.RequestedScope.SubjectHints = append(r.RequestedScope.SubjectHints, SubjectHint{Kind: SubjectTeam, ID: id, Label: id, Source: "ask-dev"})
		}
	}
	rig, _, two := bindingCarryTurns(t, true, nil, full, emptyProbeResponse())
	if line := two.transitions[0]; line.BoundAnchorCarry != BoundAnchorCarryCapFull {
		t.Fatalf("line carry = %s, want cap_full", line.BoundAnchorCarry)
	}
	if hints := carriedHintsOf(rig); len(hints) != 0 {
		t.Fatalf("carried hints = %+v, want none past the cap", hints)
	}
}

// TestAContestedOrPendingBindingCarry: a contested binding carries nothing;
// a binding pending window confirmation is admitted and carries its held
// identity only with the pending carry on, and reads pending_held without
// it. Whatever carries nothing withholds the ledger's alpha too.
func TestAContestedOrPendingBindingCarry(t *testing.T) {
	for _, tc := range []struct {
		name      string
		state     AnchorBindingState
		pendingOn bool
		carry     BoundAnchorCarry
		wantHints int
	}{
		{"contested", AnchorBindingContested, true, BoundAnchorCarryNotAdmitted, 0},
		{"pending, pending carry off", AnchorBindingPendingWindowConfirmation, false, BoundAnchorCarryPendingHeld, 0},
		{"pending, pending carry on", AnchorBindingPendingWindowConfirmation, true, BoundAnchorCarryInjected, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			set := mutateStoredBindings(func(s *PersistedSemanticState) {
				editBindingMember(s, func(b *AnchorBinding) {
					b.State = tc.state
					if tc.state == AnchorBindingContested {
						b.Reason, b.ContenderKind, b.ContenderID = AnchorBindingReasonContestedByResolution, probeBeta.Kind, probeBeta.CanonicalID
					}
				})
			})
			rig, _, two := bindingCarryTurnsWith(t, carryTurnOptions{carryOn: true, pendingOn: tc.pendingOn, edit: set}, emptyProbeResponse())
			if line := two.transitions[0]; line.BoundAnchorCarry != tc.carry {
				t.Fatalf("line carry = %s, want %s", line.BoundAnchorCarry, tc.carry)
			}
			if hints := carriedHintsOf(rig); len(hints) != tc.wantHints {
				t.Fatalf("carried hints = %+v, want %d", hints, tc.wantHints)
			}
			// The ledger's alpha reaches the selection only beside a due carry
			// of the same identity; otherwise it is withheld.
			carried := tc.wantHints == 1
			if call := rig.graph.calls[len(rig.graph.calls)-1]; (call.anchor != nil) != carried {
				t.Fatalf("resolution anchor selection = %+v, want present=%v", call.anchor, carried)
			}
			if !carried {
				got := memberEntries(two.result, contractsv1.ContextFabricStructureNeedSubjectAnchor)
				if len(got) != 1 || got[0].Disposition != contractsv1.ContextFabricStructureDispositionVetoedUnresolved {
					t.Fatalf("served subject_anchor entry = %+v, want vetoed_unresolved", got)
				}
			}
		})
	}
}

// TestNewEngineRefusesTheCarryWithoutTheShadow: the carry is owned by the
// shadow binding, so the switch with the shadow off is a configuration
// error; the pending carry widens the carry, so it alone is one too.
func TestNewEngineRefusesTheCarryWithoutTheShadow(t *testing.T) {
	deps := EngineDependencies{
		Interpreter: &anchorProbeInterpreter{},
		Graph:       &needTurnGraph{},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return validInvestigationResult(), nil
		}),
	}
	options := EngineOptions{ServiceVersion: "carry", NewResultID: resultIDSequence(), AnchorBindingCarryEnabled: true}
	if _, err := NewEngine(deps, options); err != nil {
		t.Fatalf("premise: the carry with the shadow on must construct, got %v", err)
	}
	options.AnchorBindingPendingCarryEnabled = true
	if _, err := NewEngine(deps, options); err != nil {
		t.Fatalf("premise: the pending carry beside the carry must construct, got %v", err)
	}
	options.AnchorBindingCarryEnabled = false
	if _, err := NewEngine(deps, options); err == nil || !strings.Contains(err.Error(), "pending carry needs the anchor binding carry") {
		t.Fatalf("NewEngine() error = %v, want the pending-needs-carry refusal", err)
	}
	options.AnchorBindingCarryEnabled, options.AnchorBindingPendingCarryEnabled = true, false
	options.DisableAnchorBindingShadow = true
	_, err := NewEngine(deps, options)
	if err == nil || !strings.Contains(err.Error(), "needs the shadow anchor binding") {
		t.Fatalf("NewEngine() error = %v, want the carry-needs-shadow refusal", err)
	}
}
