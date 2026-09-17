package contextfabric

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
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
	if !reflect.DeepEqual(line.To, want) || two.saved == nil || bindingMember(two.saved) == nil || !reflect.DeepEqual(*bindingMember(two.saved), want) {
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
	if tracker.modelAnchorKind != SubjectRepository {
		t.Fatalf("model anchor kind = %s", tracker.modelAnchorKind)
	}
	if _, line := tracker.decide(BudgetAssertDecisive, InvestigationResult{ResultID: "result_named"}, nil); line.NamedExpectedKind != SubjectProject || line.FrameExpressionKind != SubjectExpressionNamed {
		t.Fatalf("named reading: line kind %s frame %s", line.NamedExpectedKind, line.FrameExpressionKind)
	}
	tracker.frame = frame
	tracker.observeResolution(AnchorBindingEvaluationResolved, resolution, bases)

	kept := InvestigationResult{ResultID: "result_kept", SubjectResolution: SubjectResolution{Committed: []SubjectRef{{Kind: bindAlpha.Kind, CanonicalID: bindAlpha.ID}}}}
	binding, line := tracker.decide(BudgetAssertDecisive, kept, nil)
	if binding.State != AnchorBindingBound || binding.CanonicalID != bindAlpha.ID || binding.GraphEpoch != 3 || binding.OriginResultID != "result_kept" {
		t.Fatalf("kept: binding = %+v", binding)
	}
	if !reflect.DeepEqual(line.CallerHintIDs, []string{"project:project:a", "team:team:z"}) || line.NamedExpectedKind != "" || line.ModelAnchorKind != SubjectRepository {
		t.Fatalf("kept: line = %+v", line)
	}
	if line.Agreement != AnchorBindingNotEvaluated || line.DisagreementField != AnchorBindingFieldNone {
		t.Fatalf("kept, no snapshot: agreement = %s/%s", line.Agreement, line.DisagreementField)
	}

	dropped := InvestigationResult{ResultID: "result_dropped", SubjectResolution: SubjectResolution{Committed: []SubjectRef{}}}
	if binding, _ := tracker.decide(BudgetAssertSubjectlessTerminal, dropped, nil); binding.State != AnchorBindingUnbound {
		t.Fatalf("dropped: a commit absent from the saved document was taken as proof: %+v", binding)
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

// TestAReuseServeDecidesFromThisRequestAndTheReplayedProof: a reuse serve is
// decided like a resolved turn over the replayed row's resolution, with this
// request's own hints; a hint the replayed row never committed contests the
// anchor instead of being ignored, and the line shows the hints.
func TestAReuseServeDecidesFromThisRequestAndTheReplayedProof(t *testing.T) {
	project, candidate := reusableCandidate()
	candidate.SubjectResolution.Candidates = []SubjectCandidate{{
		ReceiptID: "receipt_reuse", Subject: project, State: ResolutionCommitted,
		MatchedTerms: []string{"a"}, MatchReasons: []string{"matched"}, Confidence: 1, EvidenceRefIDs: []string{},
	}}
	held := anchorRef{Kind: project.Kind, ID: project.CanonicalID}
	other := SubjectHint{Kind: project.Kind, ID: "project_other", Label: "Other", Source: "ask-dev"}
	framed := BuildSemanticState(SemanticStateInput{
		Outcome:         QuestionFamilyOutcome{Family: QuestionFamilyScopedCohortStatus, Source: QuestionFamilySourceModel, Frame: countingFrame(SubjectTeam), Gate: FrameGate{Outcome: FrameGatePassed}},
		EmittedShape:    ShapeOpen,
		FamilyVersion:   QuestionFamilyTableVersion,
		RequestIdentity: SemanticRequestIdentityOf(validInvestigationRequest(), ""),
	})
	unframed := BuildSemanticState(SemanticStateInput{
		Outcome:         QuestionFamilyOutcome{Family: QuestionFamilyUnclassified, Source: QuestionFamilySourceNone},
		FamilyVersion:   QuestionFamilyTableVersion,
		RequestIdentity: SemanticRequestIdentityOf(validInvestigationRequest(), ""),
	})
	if !framed.FramePresent {
		t.Fatalf("fixture defect: the framed row carries no frame")
	}
	proven := func(proof AnchorBindingProof, reason AnchorBindingReason) AnchorBinding {
		return AnchorBinding{State: AnchorBindingBound, Kind: held.Kind, CanonicalID: held.ID, Proof: proof, Reason: reason, OriginResultID: candidate.ResultID}
	}
	contested := proven(AnchorBindingProofIdentityProven, AnchorBindingReasonAmbiguousProof)
	contested.State, contested.ContenderKind, contested.ContenderID = AnchorBindingContested, other.Kind, other.ID
	for _, tc := range []struct {
		name       string
		row        *PersistedSemanticState
		hints      []SubjectHint
		want       AnchorBinding
		wantProven []string
	}{
		{"replayed proof, no hints", framed, nil, proven(AnchorBindingProofIdentityProven, AnchorBindingReasonIdentityProven), []string{"project:project_ask_dev"}},
		{"a hint naming the replayed anchor", framed, []SubjectHint{{Kind: held.Kind, ID: held.ID, Label: "Ask Dev", Source: "ask-dev"}}, proven(AnchorBindingProofCallerHint, AnchorBindingReasonCallerHint), []string{"project:project_ask_dev"}},
		{"a hint naming another identity of the anchor kind", framed, []SubjectHint{other}, contested, []string{"project:project_ask_dev"}},
		{"a hint of another kind", framed, []SubjectHint{{Kind: SubjectTeam, ID: "team_x", Label: "Team X", Source: "ask-dev"}}, proven(AnchorBindingProofIdentityProven, AnchorBindingReasonIdentityProven), []string{"project:project_ask_dev"}},
		{"a row with no counting frame proves nothing", unframed, []SubjectHint{other}, AnchorBinding{State: AnchorBindingUnbound, Proof: AnchorBindingProofNone, Reason: AnchorBindingReasonNoProof}, []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
				ReuseGate: storedReuseGate{stored: StoredInvestigationResult{Result: candidate, SemanticState: cloneSemanticState(tc.row), SemanticStateRead: SemanticStateReadAvailable}},
				Telemetry: telemetry,
			}, EngineOptions{ServiceVersion: "acr-test", Now: func() time.Time { return time.Unix(200, 0).UTC() }, NewResultID: func() string { return "result_fresh_00001" }})
			if err != nil {
				t.Fatalf("NewEngine: %v", err)
			}
			request := validInvestigationRequest()
			request.RequestedScope.SubjectHints = tc.hints
			result := mustInvestigate(t, engine, reusePrincipal(), request)
			if !result.Reused {
				t.Fatalf("premise: not a reuse hit")
			}
			if len(telemetry.anchorBindingTransitions) != 1 || len(telemetry.semanticStatePersistences) != 0 {
				t.Fatalf("lines=%d saves=%d", len(telemetry.anchorBindingTransitions), len(telemetry.semanticStatePersistences))
			}
			line := telemetry.anchorBindingTransitions[0]
			if !reflect.DeepEqual(line.To, tc.want) || line.Persisted != AnchorBindingNotSaved || line.Site != BudgetAssertReuse || line.Evaluation != AnchorBindingEvaluationReused || line.ResultID != candidate.ResultID {
				t.Fatalf("line = %+v, want to=%+v not_saved at reuse", line, tc.want)
			}
			if !reflect.DeepEqual(line.CallerHintIDs, hintIDs(tc.hints)) || !reflect.DeepEqual(line.ProvenAnchorIDs, tc.wantProven) {
				t.Fatalf("line hints=%v proven=%v, want hints=%v proven=%v", line.CallerHintIDs, line.ProvenAnchorIDs, hintIDs(tc.hints), tc.wantProven)
			}
			if !reflect.DeepEqual(line.CommittedSubjects, []string{"project:project_ask_dev=authoritative_identity"}) {
				t.Fatalf("committed subjects = %v", line.CommittedSubjects)
			}
		})
	}
}

// TestAnchorBindingDecideComparesTheHeldAnchorWithWhatWasServed: the
// comparison reads the ledger's subject_anchor entry wherever it sits, never
// treats a contested binding as effective, and takes the served count's
// anchor only from a committed decision.
func TestAnchorBindingDecideComparesTheHeldAnchorWithWhatWasServed(t *testing.T) {
	alpha := ConfirmedNeedEntry{Member: contractsv1.ContextFabricStructureNeedSubjectAnchor, AppliedKind: bindAlpha.Kind, AppliedValue: bindAlpha.ID}
	expected := ConfirmedNeedEntry{Member: contractsv1.ContextFabricStructureNeedExpectedKind, AppliedValue: string(SubjectTeam)}
	carried := func(binding AnchorBinding) *anchorBindingTracker {
		return &anchorBindingTracker{parent: anchorBindingParent{ResultID: "result_bind_parent", Status: AnchorBindingParentPresent, Binding: binding}, evaluation: AnchorBindingEvaluationNotResolved, epoch: 7}
	}
	result := InvestigationResult{ResultID: "result_compare"}
	scoped := func(decision CountPopulationScopeDecision) CountPopulationScope {
		return CountPopulationScope{ExpressionKind: SubjectExpressionChildrenOfScope, Decision: decision, AnchorID: bindAlpha.ID, AnchorSubjectKind: bindAlpha.Kind}
	}
	for _, tc := range []struct {
		name      string
		binding   AnchorBinding
		ledger    []ConfirmedNeedEntry
		count     *CountPopulationScope
		wantState AnchorBindingState
		wantAgree AnchorBindingAgreement
		wantField AnchorBindingDisagreementField
		wantCount anchorRef
	}{
		{"the anchor entry after another member", heldBinding(AnchorBindingBound, bindAlpha), []ConfirmedNeedEntry{expected, alpha}, nil,
			AnchorBindingBound, AnchorBindingAgree, AnchorBindingFieldNone, anchorRef{}},
		{"a contested binding is not effective", contestedBinding(bindAlpha, bindBeta), []ConfirmedNeedEntry{alpha}, nil,
			AnchorBindingContested, AnchorBindingDisagree, AnchorBindingFieldCarriedAnchor, anchorRef{}},
		{"a committed count names its anchor", heldBinding(AnchorBindingBound, bindAlpha), []ConfirmedNeedEntry{alpha}, ptrTo(scoped(CountPopulationScopeAnchorCommitted)),
			AnchorBindingBound, AnchorBindingAgree, AnchorBindingFieldNone, bindAlpha},
		{"an ambiguous count names no anchor", heldBinding(AnchorBindingBound, bindAlpha), []ConfirmedNeedEntry{alpha}, ptrTo(scoped(CountPopulationScopeAnchorAmbiguous)),
			AnchorBindingBound, AnchorBindingDisagree, AnchorBindingFieldCountAnchor, anchorRef{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tracker := carried(tc.binding)
			if tc.count != nil {
				tracker.observeServedCount(*tc.count)
			}
			binding, line := tracker.decide(BudgetAssertDecisive, result, &PersistedSemanticState{ConfirmedNeeds: tc.ledger})
			if binding.State != tc.wantState || binding.CanonicalID != bindAlpha.ID {
				t.Fatalf("binding = %+v, want %s on alpha", binding, tc.wantState)
			}
			if line.ServedAnchor != bindAlpha {
				t.Fatalf("served anchor = %+v, want alpha", line.ServedAnchor)
			}
			if line.Agreement != tc.wantAgree || line.DisagreementField != tc.wantField || line.ServedCountAnchor != tc.wantCount {
				t.Fatalf("line = %s/%s count anchor %+v, want %s/%s %+v", line.Agreement, line.DisagreementField, line.ServedCountAnchor, tc.wantAgree, tc.wantField, tc.wantCount)
			}
		})
	}
}

// brokenReadStore attaches a decoded snapshot beside a read status that says
// it is unavailable -- a pairing DecodeSemanticState never returns, so the
// parent reader must not trust the snapshot on the status's word.
type brokenReadStore struct{ *staticResultStore }

func (s brokenReadStore) Get(ctx context.Context, principal storage.Principal, resultID string) (StoredInvestigationResult, error) {
	stored, err := s.staticResultStore.Get(ctx, principal, resultID)
	stored.SemanticStateRead = SemanticStateReadMalformed
	return stored, err
}

func TestReadAnchorBindingParentRefusesASnapshotBesideAnUnavailableRead(t *testing.T) {
	bound := heldBinding(AnchorBindingBound, bindAlpha)
	state := BuildSemanticState(SemanticStateInput{
		Outcome:         QuestionFamilyOutcome{Family: QuestionFamilyUnclassified, Source: QuestionFamilySourceNone},
		FamilyVersion:   QuestionFamilyTableVersion,
		RequestIdentity: SemanticRequestIdentityOf(validInvestigationRequest(), ""),
	})
	setBindingMember(state, &bound)
	store := brokenReadStore{&staticResultStore{results: map[string]InvestigationResult{"result_broken": validInvestigationResult()}, states: map[string]*PersistedSemanticState{"result_broken": state}}}
	ctx := withCarryResultCache(context.Background())
	stored, err := carryLoadResult(ctx, store, acceptancePrincipal(), "result_broken")
	if err != nil || stored.SemanticState == nil || stored.SemanticStateRead == SemanticStateReadAvailable {
		t.Fatalf("premise: the memo must hold a snapshot beside an unavailable read, got state=%v read=%s err=%v", stored.SemanticState != nil, stored.SemanticStateRead, err)
	}
	request := validInvestigationRequest()
	request.ParentResultID = "result_broken"
	if got := readAnchorBindingParent(ctx, request, 7); got.Status != AnchorBindingParentUnloadable {
		t.Fatalf("status = %s, want unloadable", got.Status)
	}
}

// TestAnchorBindingLineWritesAbsentListsAsEmptyLists: a line built from an
// event whose lists were never set writes empty lists, never null.
func TestAnchorBindingLineWritesAbsentListsAsEmptyLists(t *testing.T) {
	args := AnchorBindingTransitionLogArgs(AnchorBindingTransitionEvent{}, "org_x")
	checked := 0
	for i := 0; i+1 < len(args); i += 2 {
		key := args[i].(string)
		if key != "caller_hint_ids" && key != "proven_anchor_ids" {
			continue
		}
		checked++
		if list, ok := args[i+1].([]string); !ok || list == nil || len(list) != 0 {
			t.Errorf("%s = %#v, want an empty non-nil list", key, args[i+1])
		}
	}
	if checked != 2 {
		t.Fatalf("found %d list keys, want 2", checked)
	}
}

// TestEveryBinderInputIsOnTheTransitionLine: each field bindAnchor reads, and
// each field of the binding it starts from, is written under a named key;
// a field added without one fails here.
func TestEveryBinderInputIsOnTheTransitionLine(t *testing.T) {
	inputKeys := map[string][]string{
		"From":            {"from_state", "from_kind", "from_id", "from_proof", "from_reason", "from_origin_result_id", "from_graph_epoch", "from_contender_kind", "from_contender_id", "parent_binding", "parent_graph_epoch", "carry_checks"},
		"Evaluation":      {"evaluation"},
		"Frame":           {"frame_expression_kind", "anchor_term_count", "named_expected_kind"},
		"ModelAnchorKind": {"model_anchor_kind"},
		"Receipt":         {"receipt_anchor_kind", "receipt_anchor_id"},
		"CallerHints":     {"caller_hint_ids"},
		"Resolution":      {"committed_subjects", "proven_anchor_ids"},
		"Bases":           {"committed_subjects"},
		"ResultID":        {"result_id"},
		"GraphEpoch":      {"graph_epoch"},
	}
	bindingKeys := map[string]string{
		"State": "from_state", "Kind": "from_kind", "CanonicalID": "from_id", "Proof": "from_proof", "Reason": "from_reason",
		"OriginResultID": "from_origin_result_id", "GraphEpoch": "from_graph_epoch", "ContenderKind": "from_contender_kind", "ContenderID": "from_contender_id",
	}
	args := AnchorBindingTransitionLogArgs(AnchorBindingTransitionEvent{}, "org_x")
	written := map[string]bool{}
	for i := 0; i+1 < len(args); i += 2 {
		written[args[i].(string)] = true
	}
	inputType := reflect.TypeOf(anchorBindingInput{})
	for i := 0; i < inputType.NumField(); i++ {
		name := inputType.Field(i).Name
		keys, ok := inputKeys[name]
		if !ok {
			t.Errorf("binder input %s has no line key", name)
		}
		for _, key := range keys {
			if !written[key] {
				t.Errorf("binder input %s: key %s is not written", name, key)
			}
		}
	}
	bindingType := reflect.TypeOf(AnchorBinding{})
	for i := 0; i < bindingType.NumField(); i++ {
		name := bindingType.Field(i).Name
		key, ok := bindingKeys[name]
		if !ok || !written[key] {
			t.Errorf("binding field %s is not written as a from_ key", name)
		}
		if to := strings.Replace(key, "from_", "to_", 1); name == "GraphEpoch" && !written[to] {
			t.Errorf("the decided binding's graph epoch is not written")
		}
	}
}

// TestAnchorBindingLineCarriesEveryEpochTheDecisionRead: the turn's epoch,
// the parent row's binding epoch and the decided binding's epoch are all on
// the line, with distinct values.
func TestAnchorBindingLineCarriesEveryEpochTheDecisionRead(t *testing.T) {
	stale := heldBinding(AnchorBindingBound, bindAlpha)
	stale.GraphEpoch = 5
	resolution, bases := proofOf(CommitBasisAuthoritativeIdentity, bindBeta)
	parentState := &PersistedSemanticState{}
	setBindingMember(parentState, &stale)
	tracker := &anchorBindingTracker{parent: anchorBindingParentOf("result_bind_parent", parentState, 9), epoch: 9, frame: countingFrame(SubjectTeam)}
	tracker.observeResolution(AnchorBindingEvaluationResolved, resolution, bases)
	result := InvestigationResult{ResultID: "result_epochs", SubjectResolution: SubjectResolution{Committed: resolution.Committed}}
	to, line := tracker.decide(BudgetAssertDecisive, result, nil)
	if line.ParentBinding != AnchorBindingParentStaleGraphEpoch || line.ParentGraphEpoch != 5 || line.GraphEpoch != 9 || to.GraphEpoch != 9 || line.From.GraphEpoch != 0 {
		t.Fatalf("epochs: parent %s/%d turn %d decided %d from %d", line.ParentBinding, line.ParentGraphEpoch, line.GraphEpoch, to.GraphEpoch, line.From.GraphEpoch)
	}
	fields := map[string]any{}
	args := AnchorBindingTransitionLogArgs(line, "org_x")
	for i := 0; i+1 < len(args); i += 2 {
		fields[args[i].(string)] = args[i+1]
	}
	if fields["parent_graph_epoch"] != int64(5) || fields["graph_epoch"] != int64(9) || fields["to_graph_epoch"] != int64(9) || fields["from_graph_epoch"] != int64(0) {
		t.Fatalf("written epochs = %v/%v/%v/%v", fields["parent_graph_epoch"], fields["graph_epoch"], fields["to_graph_epoch"], fields["from_graph_epoch"])
	}
	if got := fields["committed_subjects"]; !reflect.DeepEqual(got, []string{"repository:repository:bind-beta=authoritative_identity"}) {
		t.Fatalf("committed_subjects = %v", got)
	}
	absent := &anchorBindingTracker{parent: anchorBindingParent{Status: AnchorBindingParentAbsent, StoredEpoch: 4}}
	if _, line := absent.decide(BudgetAssertDecisive, InvestigationResult{}, nil); line.ParentGraphEpoch != -1 {
		t.Fatalf("a parent with no stored binding wrote epoch %d", line.ParentGraphEpoch)
	}
	// A member that does not decode has no epoch to report, and the line
	// says so rather than publishing a zero.
	undecodable := &PersistedSemanticState{Extensions: SemanticStateExtensions{anchorBindingExtension: json.RawMessage(`{"graph_epoch":"7"}`)}}
	parent := anchorBindingParentOf("result_bind_parent", undecodable, 9)
	if parent.Status != AnchorBindingParentInvalid || parent.storedEpoch() != -1 {
		t.Fatalf("an undecodable member: status %s epoch %d, want invalid and -1", parent.Status, parent.storedEpoch())
	}
	// The counting frame's anchor terms are counted on the line; the terms
	// themselves are corpus text and never published.
	if line.AnchorTermCount != len(countingFrame(SubjectTeam).SubjectExpression.Scoped.AnchorTerms) || line.AnchorTermCount == 0 {
		t.Fatalf("anchor term count = %d", line.AnchorTermCount)
	}
}
