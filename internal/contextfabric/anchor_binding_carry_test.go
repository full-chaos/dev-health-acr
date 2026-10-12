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

// TestAWithheldLedgerAnchorReachesNoResolutionCall: the binding is refused
// (expired) while the ledger still holds the engine-committed alpha.
// Switched off, the ledger's alpha reaches resolution as the anchor
// selection. Switched on, no resolution call receives it, on the decisive
// path or the window-gated offers-only path, and the served carried entry
// discloses it as vetoed_unresolved on the decisive path.
func TestAWithheldLedgerAnchorReachesNoResolutionCall(t *testing.T) {
	expire := mutateStoredBindings(func(s *PersistedSemanticState) {
		editBindingMember(s, func(b *AnchorBinding) { b.ProvenAt = b.ProvenAt.Add(-DefaultAnchorBindingMaxAge - time.Second) })
	})
	for _, tc := range []struct {
		name         string
		carryOn      bool
		statedWindow bool
		wantAnchor   bool
	}{
		{"switched off, decisive", false, true, true},
		{"switched on, decisive", true, true, false},
		{"switched off, window gated", false, false, true},
		{"switched on, window gated", true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig, _, two := bindingCarryTurns(t, tc.carryOn, expire, func(r *InvestigationRequest) {
				if !tc.statedWindow {
					*r = continuingNeedTurn(needTurnRequest("request_carry_two", false), r.ParentResultID)
				}
			}, emptyProbeResponse())
			calls := rig.graph.calls[len(rig.graph.calls)-1:]
			gotAnchor := calls[0].anchor != nil && calls[0].anchor.CanonicalID == probeAlpha.CanonicalID
			if gotAnchor != tc.wantAnchor {
				t.Fatalf("resolution anchor selection = %+v, want alpha=%v", calls[0].anchor, tc.wantAnchor)
			}
			if !tc.statedWindow || !tc.carryOn {
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
// identity.
func TestAContestedOrPendingBindingCarry(t *testing.T) {
	for _, tc := range []struct {
		name      string
		state     AnchorBindingState
		carry     BoundAnchorCarry
		wantHints int
	}{
		{"contested", AnchorBindingContested, BoundAnchorCarryNotAdmitted, 0},
		{"pending window confirmation", AnchorBindingPendingWindowConfirmation, BoundAnchorCarryInjected, 1},
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
			rig, _, two := bindingCarryTurns(t, true, set, nil, emptyProbeResponse())
			if line := two.transitions[0]; line.BoundAnchorCarry != tc.carry {
				t.Fatalf("line carry = %s, want %s", line.BoundAnchorCarry, tc.carry)
			}
			if hints := carriedHintsOf(rig); len(hints) != tc.wantHints {
				t.Fatalf("carried hints = %+v, want %d", hints, tc.wantHints)
			}
		})
	}
}

// TestNewEngineRefusesTheCarryWithoutTheShadow: the carry is owned by the
// shadow binding, so the switch with the shadow off is a configuration error.
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
	options.DisableAnchorBindingShadow = true
	_, err := NewEngine(deps, options)
	if err == nil || !strings.Contains(err.Error(), "needs the shadow anchor binding") {
		t.Fatalf("NewEngine() error = %v, want the carry-needs-shadow refusal", err)
	}
}
