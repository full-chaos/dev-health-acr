package contextfabric

import (
	"bytes"
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/hintsource"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// hintCommittingGraph is the probe graph with resolution's exact-commit
// channel for the engine's carried anchor: a request that carries one
// commits that identity on a proven basis; any other request gets the
// scripted response. Calls are recorded by the wrapped graph.
type hintCommittingGraph struct {
	*needTurnGraph
}

func (g hintCommittingGraph) ResolveSubjects(ctx context.Context, principal storage.Principal, request InvestigationRequest, interpretation InterpretedQuestion, binding ResolvedGraphBinding, kind *ConfirmedExpectedKind, anchor *ConfirmedAnchorSelection, frame *QuestionFrame, scopeKind SubjectKind) (SubjectResolution, StructureOfferMaterial, CommitBasisSet, CommitDecisionDigestSet, error) {
	resolution, material, bases, digests, err := g.needTurnGraph.ResolveSubjects(ctx, principal, request, interpretation, binding, kind, anchor, frame, scopeKind)
	for _, hint := range request.RequestedScope.SubjectHints {
		if hint.Source != string(hintsource.EngineCommittedAnchorCarry) {
			continue
		}
		subject := SubjectRef{Kind: hint.Kind, CanonicalID: hint.ID, Label: hint.ID}
		if hint.ID == probeAlpha.CanonicalID {
			subject = probeAlpha
		}
		return SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{subject}}, StructureOfferMaterial{}, provenCommitBases(subject), nil, nil
	}
	return resolution, material, bases, digests, err
}

// pendingTurns runs a window-gated turn whose offers-only pass proves alpha,
// then the confirming turn with the switches as given and window as the
// confirmed window. The graph commits a carried anchor and nothing else.
func pendingTurns(t *testing.T, carryOn, pendingOn bool, window *RequestedEvidenceWindow) (*anchorProbeRig, anchorProbeTurn, anchorProbeTurn) {
	t.Helper()
	rig := newAnchorProbeRig(t, false)
	rig.engine.graph = hintCommittingGraph{rig.graph}
	rig.engine.anchorBindingCarryEnabled, rig.engine.anchorBindingPendingCarryEnabled = carryOn, pendingOn
	rig.interpreter.read(SubjectRepository, true)
	one := rig.turn(t, needTurnRequest("request_pending_one", false), identityProvenResponse(probeAlpha))
	if held := bindingMember(one.saved); held == nil || held.State != AnchorBindingPendingWindowConfirmation || held.CanonicalID != probeAlpha.CanonicalID {
		t.Fatalf("premise: the gated turn's binding = %+v, want alpha pending", held)
	}
	rig.verify()
	rig.interpreter.read(SubjectRepository, false)
	request := continuingNeedTurn(needTurnRequest("request_pending_two", true), one.result.ResultID)
	request.TimeContext.EvidenceWindow = window
	two := rig.turn(t, request, emptyProbeResponse())
	return rig, one, two
}

// TestAPendingProofCarriesIntoTheConfirmingTurnWithThePendingCarry: the
// window gate's offers-only pass proves alpha; the user confirms the window
// on the next turn, whose own resolution names nothing. Only with both the
// carry and the pending carry on does the pending binding carry alpha into
// that turn, where resolution commits it and the count is served on it.
// The gated turn itself serves the same bytes in every arm; only its
// ledger line says the proof is held (window_confirmation_gated_pending).
func TestAPendingProofCarriesIntoTheConfirmingTurnWithThePendingCarry(t *testing.T) {
	var gatedServed []byte
	for _, tc := range []struct {
		name               string
		carryOn, pendingOn bool
		skip               CaptureSkipReason
		carry              BoundAnchorCarry
		served             bool
	}{
		{"both off", false, false, CaptureSkipReasonWindowConfirmationGatedDiscard, BoundAnchorCarrySwitchOff, false},
		{"carry only", true, false, CaptureSkipReasonWindowConfirmationGatedDiscard, BoundAnchorCarryPendingHeld, false},
		{"carry and pending carry", true, true, CaptureSkipReasonWindowConfirmationGatedPending, BoundAnchorCarryInjected, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig, one, two := pendingTurns(t, tc.carryOn, tc.pendingOn, validConfirmedWindow())
			if gatedServed == nil {
				gatedServed = one.served
			} else if !bytes.Equal(one.served, gatedServed) {
				t.Fatalf("the gated turn's served result differs between arms:\n%s\n%s", one.served, gatedServed)
			}
			ledgers := rig.telemetry.confirmedNeedLedgers
			if len(ledgers) < 2 || ledgers[0].CaptureSkipReason != tc.skip {
				t.Fatalf("gated turn capture_skip_reason = %+v, want %s", ledgers, tc.skip)
			}
			line := two.transitions[0]
			if line.Admission != AnchorBindingAdmissionAdmitted || line.BoundAnchorCarry != tc.carry {
				t.Fatalf("confirming turn admission/carry = %s/%s, want admitted/%s", line.Admission, line.BoundAnchorCarry, tc.carry)
			}
			committed := two.result.SubjectResolution.Committed
			gotServed := len(committed) == 1 && committed[0].CanonicalID == probeAlpha.CanonicalID && line.ServedCount == string(CountPopulationScopeAnchorCommitted) && line.ServedCountAnchor.ID == probeAlpha.CanonicalID
			if gotServed != tc.served {
				t.Fatalf("confirming turn committed=%+v count=%s/%s, want served on alpha=%v", committed, line.ServedCount, line.ServedCountAnchor.ID, tc.served)
			}
		})
	}
}

// TestAPendingProofIsNotTiedToTheWindowConfirmed: the identity proof does not
// depend on the window, so confirming another window than the default
// still carries it.
func TestAPendingProofIsNotTiedToTheWindowConfirmed(t *testing.T) {
	_, _, two := pendingTurns(t, true, true, &RequestedEvidenceWindow{RelativeID: RelativeWindowTrailing30D})
	if line := two.transitions[0]; line.Admission != AnchorBindingAdmissionAdmitted || line.BoundAnchorCarry != BoundAnchorCarryInjected {
		t.Fatalf("confirming turn admission/carry = %s/%s, want admitted/injected", line.Admission, line.BoundAnchorCarry)
	}
	if committed := two.result.SubjectResolution.Committed; len(committed) != 1 || committed[0].CanonicalID != probeAlpha.CanonicalID {
		t.Fatalf("confirming turn committed = %+v, want alpha", committed)
	}
}

// TestAnExpiredPendingProofDoesNotCarry: a pending proof older than the
// maximum age is refused at admission and carries nothing.
func TestAnExpiredPendingProofDoesNotCarry(t *testing.T) {
	rig := newAnchorProbeRig(t, false)
	rig.engine.graph = hintCommittingGraph{rig.graph}
	rig.engine.anchorBindingCarryEnabled, rig.engine.anchorBindingPendingCarryEnabled = true, true
	rig.interpreter.read(SubjectRepository, true)
	one := rig.turn(t, needTurnRequest("request_pending_one", false), identityProvenResponse(probeAlpha))
	expiredBinding(rig.store)
	rig.verify()
	rig.interpreter.read(SubjectRepository, false)
	two := rig.turn(t, continuingNeedTurn(needTurnRequest("request_pending_two", true), one.result.ResultID), emptyProbeResponse())
	if line := two.transitions[0]; line.Admission != AnchorBindingAdmissionRefusedExpired || line.BoundAnchorCarry != BoundAnchorCarryNotAdmitted {
		t.Fatalf("confirming turn admission/carry = %s/%s, want refused_expired/not_admitted", line.Admission, line.BoundAnchorCarry)
	}
	if len(two.result.SubjectResolution.Committed) != 0 {
		t.Fatalf("committed = %+v, want none", two.result.SubjectResolution.Committed)
	}
}

// TestAGatedTurnThatProvesNothingStillReadsDiscard: with the pending carry
// on, a window-gated turn whose offers-only pass proves no identity holds
// nothing pending, and its ledger line keeps window_confirmation_gated_discard.
func TestAGatedTurnThatProvesNothingStillReadsDiscard(t *testing.T) {
	rig := newAnchorProbeRig(t, false)
	rig.engine.anchorBindingCarryEnabled, rig.engine.anchorBindingPendingCarryEnabled = true, true
	rig.interpreter.read(SubjectRepository, true)
	one := rig.turn(t, needTurnRequest("request_pending_none", false), emptyProbeResponse())
	if held := bindingMember(one.saved); held != nil && held.State == AnchorBindingPendingWindowConfirmation {
		t.Fatalf("premise: the gated turn holds %+v pending; it proved nothing", held)
	}
	if ledgers := rig.telemetry.confirmedNeedLedgers; len(ledgers) != 1 || ledgers[0].CaptureSkipReason != CaptureSkipReasonWindowConfirmationGatedDiscard {
		t.Fatalf("gated turn ledger = %+v, want window_confirmation_gated_discard", ledgers)
	}
}

// TestPendingHeldOutranksTheCallerAndTheReceipt: with the pending carry off,
// a pending parent reads pending_held even when the caller names a hint of
// its kind or redeems an anchor receipt: the binding's own state decides
// before either contest, and nothing is injected either way.
func TestPendingHeldOutranksTheCallerAndTheReceipt(t *testing.T) {
	toPending := mutateStoredBindings(func(s *PersistedSemanticState) {
		editBindingMember(s, func(b *AnchorBinding) { b.State = AnchorBindingPendingWindowConfirmation })
	})
	t.Run("caller hint", func(t *testing.T) {
		betaHint := func(r *InvestigationRequest) {
			r.RequestedScope.SubjectHints = []SubjectHint{{Kind: probeBeta.Kind, ID: probeBeta.CanonicalID, Label: probeBeta.Label, Source: "ask-dev"}}
		}
		rig, _, two := bindingCarryTurnsWith(t, carryTurnOptions{carryOn: true, edit: toPending, mutate: betaHint}, emptyProbeResponse())
		if line := two.transitions[0]; line.Admission != AnchorBindingAdmissionAdmitted || line.BoundAnchorCarry != BoundAnchorCarryPendingHeld {
			t.Fatalf("admission/carry = %s/%s, want admitted/pending_held", line.Admission, line.BoundAnchorCarry)
		}
		if hints := carriedHintsOf(rig); len(hints) != 0 {
			t.Fatalf("carried hints = %+v, want none", hints)
		}
	})
	t.Run("redeemed anchor receipt", func(t *testing.T) {
		rig, _, two := bindingCarryTurns(t, true, nil, nil, anchorOfferingResponse())
		if two.result.StructureNeeds == nil || len(two.result.StructureNeeds.AnchorOptions) != 1 {
			t.Fatalf("premise: turn two must offer one anchor, got %+v", two.result.StructureNeeds)
		}
		toPending(rig.store)
		rig.engine.anchorVerifier = func(context.Context, string, contractsv1.ContextFabricSubjectKind, string, string) (bool, AnchorVerificationReason) {
			return true, AnchorVerificationValid
		}
		rig.engine.anchorMembershipVerifier = func(context.Context, storage.Principal, RequestedScope, ResolvedGraphBinding, contractsv1.ContextFabricSubjectKind, string, string) (bool, AnchorVerificationReason) {
			return true, AnchorVerificationValid
		}
		request := continuingNeedTurnAgain(needTurnRequest("request_carry_three", true), needTurnRequest("request_carry_two", true), two.result.ResultID)
		request.PriorAnchorReceipts = []BoundSubjectReceipt{{ResultID: two.result.ResultID, ReceiptID: two.result.StructureNeeds.AnchorOptions[0].ReceiptID}}
		three := rig.turn(t, request, emptyProbeResponse())
		if line := three.transitions[0]; line.Admission != AnchorBindingAdmissionAdmitted || line.BoundAnchorCarry != BoundAnchorCarryPendingHeld {
			t.Fatalf("admission/carry = %s/%s, want admitted/pending_held", line.Admission, line.BoundAnchorCarry)
		}
		if hints := carriedHintsOf(rig); len(hints) != 0 {
			t.Fatalf("carried hints = %+v, want none", hints)
		}
	})
}
