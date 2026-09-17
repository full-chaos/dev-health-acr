package contextfabric

import (
	"context"
	"reflect"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestShadowBindingBindsARedeemedAnchorReceipt: the caller redeems the anchor
// receipt a previous turn offered; the binding is that anchor, on the
// caller's receipt.
func TestShadowBindingBindsARedeemedAnchorReceipt(t *testing.T) {
	h := newNeedTurnHarness(t, nil, func(d *EngineDependencies) {
		d.AnchorVerifier = func(context.Context, string, contractsv1.ContextFabricSubjectKind, string, string) (bool, AnchorVerificationReason) {
			return true, AnchorVerificationValid
		}
		d.AnchorMembershipVerifier = func(context.Context, storage.Principal, RequestedScope, ResolvedGraphBinding, contractsv1.ContextFabricSubjectKind, string, string) (bool, AnchorVerificationReason) {
			return true, AnchorVerificationValid
		}
	})
	offer := candidateOfferingNeedResponse()
	offer.material.Missing = []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedSubjectAnchor}
	offer.material.CandidateOptions = nil
	offer.material.AnchorOptions = []AnchorOption{{Label: "need-r2", Kind: SubjectRepository, CanonicalID: "repository:need-r2", MatchedTermHash: "aa11bb22cc33dd44ee55ff66", OfferSource: "engine"}}
	mark := len(h.telemetry.anchorBindingTransitions)
	one := h.turn(needTurnRequest("request_bind_receipt_offer", true), offer)
	if one.result.StructureNeeds == nil || len(one.result.StructureNeeds.AnchorOptions) != 1 {
		t.Fatalf("fixture: expected one anchor offer, got %+v", one.result.StructureNeeds)
	}
	request := needTurnRequest("request_bind_receipt_redeem", true)
	request.PriorAnchorReceipts = []BoundSubjectReceipt{{ResultID: one.result.ResultID, ReceiptID: one.result.StructureNeeds.AnchorOptions[0].ReceiptID}}
	two := h.turn(request, committingNeedResponse())
	lines := h.telemetry.anchorBindingTransitions[mark:]
	if len(lines) != 2 {
		t.Fatalf("transition lines = %d, want one per turn", len(lines))
	}
	line := lines[1]
	want := AnchorBinding{State: AnchorBindingBound, Kind: SubjectRepository, CanonicalID: "repository:need-r2", Proof: AnchorBindingProofCallerReceipt,
		Reason: AnchorBindingReasonCallerReceipt, OriginResultID: two.result.ResultID}
	if !reflect.DeepEqual(line.To, want) || two.saved == nil || !reflect.DeepEqual(*two.saved.AnchorBinding, want) {
		t.Fatalf("binding = %+v (persisted %+v), want %+v", line.To, two.saved, want)
	}
	if line.ReceiptAnchor != (anchorRef{Kind: SubjectRepository, ID: "repository:need-r2"}) || line.EffectiveKind != SubjectRepository {
		t.Fatalf("line receipt/effective kind = %+v/%s", line.ReceiptAnchor, line.EffectiveKind)
	}
	if line.Agreement != AnchorBindingAgree || line.ServedAnchor.ID != "repository:need-r2" {
		t.Fatalf("line agreement = %s served=%+v, want agree with the served ledger anchor", line.Agreement, line.ServedAnchor)
	}
}

// TestShadowBindingOnAGateThatResolvesNothing: the window gate fires with
// clarification off, so no offers-only resolution runs; the line still says
// the turn was window gated.
func TestShadowBindingOnAGateThatResolvesNothing(t *testing.T) {
	rig := newAnchorProbeRig(t, false)
	rig.interpreter.read("", true)
	request := needTurnRequest("request_bind_gate_quiet", false)
	request.Options.AllowClarification = false
	turn := rig.turn(t, request, identityProvenResponse(probeAlpha))
	if len(rig.graph.calls) != 0 {
		t.Fatalf("premise: the gate resolved %d times with clarification off", len(rig.graph.calls))
	}
	if len(turn.transitions) != 1 {
		t.Fatalf("transition lines = %d", len(turn.transitions))
	}
	line := turn.transitions[0]
	if line.Evaluation != AnchorBindingEvaluationWindowGated || line.To.State != AnchorBindingUnbound || line.To.Reason != AnchorBindingReasonNoProof {
		t.Fatalf("line = %+v, want window_gated, unbound, no_proof", line)
	}
}

// TestAnchorBindingDecideReadsTheSavedDocumentAndTheReading: the decision
// uses only commits the saved document still carries, and the line carries
// the reading's stated kinds and the caller's hints in canonical order.
func TestAnchorBindingDecideReadsTheSavedDocumentAndTheReading(t *testing.T) {
	resolution, bases := proofOf(CommitBasisAuthoritativeIdentity, bindAlpha)
	frame := countingFrame(SubjectTeam)
	named := QuestionFrame{SubjectExpression: SubjectExpression{Kind: SubjectExpressionNamed, Named: &NamedSubjectExpression{Terms: []string{"a"}, ExpectedKind: ptrTo(SubjectProject)}}}
	tracker := &anchorBindingTracker{parent: anchorBindingParent{Status: AnchorBindingParentNoReference}, epoch: 3}
	tracker.observeReading(QuestionFamilyOutcome{Frame: &named, WinningSample: FamilySample{ScopeAnchorKind: SubjectRepository}}, []SubjectHint{{Kind: SubjectTeam, ID: "team:z"}, {Kind: SubjectProject, ID: "project:a"}})
	if tracker.namedKind != SubjectProject || tracker.modelAnchorKind != SubjectRepository {
		t.Fatalf("reading kinds = %s/%s", tracker.namedKind, tracker.modelAnchorKind)
	}
	tracker.frame = frame
	tracker.observeResolution(AnchorBindingEvaluationResolved, resolution, bases)

	kept := InvestigationResult{ResultID: "result_kept", SubjectResolution: SubjectResolution{Committed: []SubjectRef{{Kind: bindAlpha.Kind, CanonicalID: bindAlpha.ID}}}}
	binding, line := tracker.decide(BudgetAssertDecisive, kept, nil)
	if binding.State != AnchorBindingBound || binding.CanonicalID != bindAlpha.ID || binding.GraphEpoch != 3 || binding.OriginResultID != "result_kept" {
		t.Fatalf("kept: binding = %+v", binding)
	}
	if !reflect.DeepEqual(line.CallerHintIDs, []string{"project:project:a", "team:team:z"}) || line.NamedExpectedKind != SubjectProject || line.ModelAnchorKind != SubjectRepository {
		t.Fatalf("kept: line = %+v", line)
	}
	if line.Agreement != AnchorBindingNotEvaluated || line.DisagreementField != AnchorBindingFieldNone {
		t.Fatalf("kept, no snapshot: agreement = %s/%s", line.Agreement, line.DisagreementField)
	}

	dropped := InvestigationResult{ResultID: "result_dropped", SubjectResolution: SubjectResolution{Committed: []SubjectRef{}}}
	if binding, _ := tracker.decide(BudgetAssertSubjectlessTerminal, dropped, nil); binding.State != AnchorBindingUnbound {
		t.Fatalf("dropped: a commit the saved document no longer carries was proof: %+v", binding)
	}

	tracker.observeResolution(AnchorBindingEvaluationWindowGated, resolution, bases)
	if binding, _ := tracker.decide(BudgetAssertWindowConfirmationRequired, dropped, nil); binding.State != AnchorBindingPendingWindowConfirmation {
		t.Fatalf("gated: the offers-only proof must stand without a saved commit: %+v", binding)
	}

	receipt := confirmedStructureMember{Member: contractsv1.ContextFabricStructureNeedExpectedKind, AppliedValue: string(SubjectTeam)}
	anchor := confirmedStructureMember{Member: contractsv1.ContextFabricStructureNeedSubjectAnchor, AppliedKind: SubjectProject, AppliedValue: "project:r"}
	tracker.observeReceipt([]confirmedStructureMember{receipt, anchor})
	if tracker.receipt == nil || *tracker.receipt != anchor {
		t.Fatalf("receipt = %+v, want the subject_anchor member only", tracker.receipt)
	}
	tracker.observeReceipt([]confirmedStructureMember{receipt})
	if tracker.receipt == nil {
		t.Fatalf("a later batch without an anchor erased the observed receipt")
	}
	fresh := &anchorBindingTracker{}
	fresh.observeReceipt([]confirmedStructureMember{receipt})
	if fresh.receipt != nil {
		t.Fatalf("an expected_kind member was taken as an anchor receipt: %+v", fresh.receipt)
	}

	var off *anchorBindingTracker
	off.observeReceipt([]confirmedStructureMember{anchor})
	off.observeReading(QuestionFamilyOutcome{}, nil)
	off.observeResolution(AnchorBindingEvaluationResolved, resolution, bases)
	off.observeServedCount(CountPopulationScope{})
}

func ptrTo[T any](value T) *T { return &value }

func TestServedResolutionProofKeepsOnlySavedCommits(t *testing.T) {
	resolution, _ := proofOf(CommitBasisAuthoritativeIdentity, bindAlpha, bindBeta)
	served := SubjectResolution{Committed: []SubjectRef{{Kind: bindBeta.Kind, CanonicalID: bindBeta.ID}, {Kind: SubjectProject, CanonicalID: bindAlpha.ID}}}
	got := servedResolutionProof(resolution, served)
	if len(got.Committed) != 1 || got.Committed[0].CanonicalID != bindBeta.ID || len(got.Candidates) != 2 {
		t.Fatalf("proof = %+v, want beta alone with every candidate", got)
	}
}

// unrecordedTelemetry records transition lines without the recorder's sweep.
type unrecordedTelemetry struct {
	*recordingTelemetry
	lines []AnchorBindingTransitionEvent
}

func (u *unrecordedTelemetry) RecordAnchorBindingTransition(_ context.Context, _ storage.Principal, event AnchorBindingTransitionEvent) {
	u.lines = append(u.lines, event)
}

// TestASaveWithNoBindingDecisionIsReportedUnrecorded: a capture that reaches
// saveResult without a tracker, with the shadow on, reports unrecorded; with
// the shadow off it reports nothing.
func TestASaveWithNoBindingDecisionIsReportedUnrecorded(t *testing.T) {
	for _, off := range []bool{false, true} {
		telemetry := &unrecordedTelemetry{recordingTelemetry: &recordingTelemetry{}}
		engine := &Engine{results: &staticResultStore{results: map[string]InvestigationResult{}}, telemetry: telemetry, anchorBindingShadowDisabled: off}
		if err := engine.saveResult(context.Background(), acceptancePrincipal(), BudgetAssertContinuationRefusal, InvestigationResult{ResultID: "result_untracked"}, nil, nil, "", 0, "", absentSemanticState(SemanticStateAbsenceContinuationRefused)); err != nil {
			t.Fatalf("saveResult: %v", err)
		}
		if off {
			if len(telemetry.lines) != 0 {
				t.Fatalf("shadow off reported %+v", telemetry.lines)
			}
			continue
		}
		if len(telemetry.lines) != 1 || telemetry.lines[0].To.Reason != AnchorBindingReasonUnrecorded || telemetry.lines[0].Site != BudgetAssertContinuationRefusal || telemetry.lines[0].Persisted != AnchorBindingPersistence(SemanticStatePersisted) {
			t.Fatalf("lines = %+v, want one unrecorded line for the continuation refusal save", telemetry.lines)
		}
	}
}

// storedReuseGate serves one stored row, snapshot included.
type storedReuseGate struct{ stored StoredInvestigationResult }

func (g storedReuseGate) FindReusable(context.Context, storage.Principal, ReuseKey) (StoredInvestigationResult, bool, ReuseMissReason, error) {
	return g.stored, true, "", nil
}

// TestAReuseServeReportsTheStoredBinding: the line reports the stored row's
// own binding, never a decision of its own, and nothing is saved.
func TestAReuseServeReportsTheStoredBinding(t *testing.T) {
	project, candidate := reusableCandidate()
	stored := heldBinding(AnchorBindingBound, anchorRef{Kind: project.Kind, ID: project.CanonicalID})
	state := BuildSemanticState(SemanticStateInput{
		Outcome:         QuestionFamilyOutcome{Family: QuestionFamilyUnclassified, Source: QuestionFamilySourceNone},
		FamilyVersion:   QuestionFamilyTableVersion,
		RequestIdentity: SemanticRequestIdentityOf(validInvestigationRequest(), ""),
	})
	state.AnchorBinding = &stored
	for _, tc := range []struct {
		name    string
		binding *AnchorBinding
		want    AnchorBinding
	}{
		{"stored binding", &stored, func() AnchorBinding { b := stored; b.Reason = AnchorBindingReasonReusedStored; return b }()},
		{"no stored binding", nil, AnchorBinding{State: AnchorBindingUnbound, Proof: AnchorBindingProofNone, Reason: AnchorBindingReasonReusedStored}},
		{"invalid stored binding", &AnchorBinding{State: "unknown_state"}, AnchorBinding{State: AnchorBindingUnbound, Proof: AnchorBindingProofNone, Reason: AnchorBindingReasonReusedStored}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rowState := cloneSemanticState(state)
			rowState.AnchorBinding = tc.binding
			telemetry := &recordingTelemetry{}
			engine, err := NewEngine(EngineDependencies{
				Graph: graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}}},
				Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
					t.Fatal("interpreted")
					return InterpretedQuestion{}, nil
				}),
				Facts: failingFactReader{t: t},
				Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
					t.Fatal("synthesized")
					return InvestigationResult{}, nil
				}),
				Results:   &resultStoreStub{},
				ReuseGate: storedReuseGate{stored: StoredInvestigationResult{Result: candidate, SemanticState: rowState, SemanticStateRead: SemanticStateReadAvailable}},
				Telemetry: telemetry,
			}, EngineOptions{ServiceVersion: "acr-test", Now: func() time.Time { return time.Unix(200, 0).UTC() }, NewResultID: func() string { return "result_fresh_00001" }})
			if err != nil {
				t.Fatalf("NewEngine: %v", err)
			}
			result := mustInvestigate(t, engine, reusePrincipal(), validInvestigationRequest())
			if !result.Reused {
				t.Fatalf("premise: not a reuse hit")
			}
			if len(telemetry.anchorBindingTransitions) != 1 || len(telemetry.semanticStatePersistences) != 0 {
				t.Fatalf("lines=%d saves=%d", len(telemetry.anchorBindingTransitions), len(telemetry.semanticStatePersistences))
			}
			line := telemetry.anchorBindingTransitions[0]
			if !reflect.DeepEqual(line.To, tc.want) || line.Persisted != AnchorBindingNotSaved || line.Site != BudgetAssertReuse || line.ResultID != candidate.ResultID {
				t.Fatalf("line = %+v, want to=%+v not_saved at reuse", line, tc.want)
			}
		})
	}
}
