package contextfabric

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// anchorBindingCertifyScenarios are the scripted conversations the transition
// line is certified from. Only the LAST turn's output is returned.
func anchorBindingCertifyScenarios() map[string][]anchorProbeStep {
	return map[string][]anchorProbeStep{
		"decisive identity proven": {
			{request: firstTurnRequest("request_cert_decisive", true), response: identityProvenResponse(probeAlpha)},
		},
		"window gated pending": {
			{request: firstTurnRequest("request_cert_gated", false), windowed: true, response: identityProvenResponse(probeAlpha)},
		},
		"natural follow-up carried silent": {
			{request: firstTurnRequest("request_cert_silent_one", true), response: identityProvenResponse(probeAlpha)},
			{request: followUp("request_cert_silent_two", "And how many teams contribute to it?", nil), response: emptyProbeResponse()},
		},
		"follow-up alias contested": {
			{request: firstTurnRequest("request_cert_alias_one", true), response: identityProvenResponse(probeAlpha)},
			{request: followUp("request_cert_alias_two", "And how many teams contribute to it?", nil), response: identityProvenResponse(probeBeta)},
		},
		"model kind conflict": {
			{request: firstTurnRequest("request_cert_kind_one", true), anchorKind: SubjectRepository, response: identityProvenResponse(probeAlpha)},
			{request: followUp("request_cert_kind_two", "", nil), anchorKind: SubjectProject, response: needTurnResponse{
				resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{probeAlpha}},
				bases:      provenCommitBases(probeAlpha),
			}},
		},
	}
}

// RunAnchorBindingScenarioForTest drives one certify scenario through the real
// engine with the production slog JSON handler and returns the last turn's
// Info output and request id.
func RunAnchorBindingScenarioForTest(t *testing.T, scenario string) (log []byte, requestID string) {
	t.Helper()
	steps, ok := anchorBindingCertifyScenarios()[scenario]
	if !ok {
		t.Fatalf("unknown anchor binding scenario %q", scenario)
	}
	var buf bytes.Buffer
	rig := newAnchorProbeRig(t, false)
	rig.engine.telemetry = NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	var turns []anchorProbeTurn
	for i, step := range steps {
		rig.interpreter.read(step.anchorKind, step.windowed)
		rig.graph.response = step.response
		request := step.request(turns)
		buf.Reset()
		requestID = fmt.Sprintf("req_%032x", 0x58840000+i)
		result, err := rig.engine.Investigate(observability.WithRequestID(context.Background(), requestID), acceptancePrincipal(), request)
		if err != nil {
			t.Fatalf("Investigate(%s) error = %v", request.RequestID, err)
		}
		rig.store.results[result.ResultID] = *rig.store.saved
		if rig.store.savedSemantic != nil && rig.store.savedSemantic.State != nil {
			rig.store.states[result.ResultID] = rig.store.savedSemantic.State
		}
		turns = append(turns, anchorProbeTurn{result: result})
	}
	return append([]byte(nil), buf.Bytes()...), requestID
}

// AnchorBindingVocabularyLog is one driver's production Info output.
type AnchorBindingVocabularyLog struct {
	Name string
	Log  []byte
}

// mutateStoredBindings edits every stored binding before a turn reads it.
func mutateStoredBindings(edit func(*PersistedSemanticState)) func(*staticResultStore) {
	return func(store *staticResultStore) {
		for _, state := range store.states {
			edit(state)
		}
	}
}

// anchorVocabularyScenarios are the drivers, beyond the save-site
// scenarios, that reach the remaining closed-vocabulary members through the
// real engine.
func anchorVocabularyScenarios() []anchorSiteScenario {
	alphaHint := func(r *InvestigationRequest) {
		r.RequestedScope.SubjectHints = []SubjectHint{{Kind: probeAlpha.Kind, ID: probeAlpha.CanonicalID, Label: probeAlpha.Label, Source: "ask-dev"}}
	}
	betaHint := func(r *InvestigationRequest) {
		r.RequestedScope.SubjectHints = []SubjectHint{{Kind: probeBeta.Kind, ID: probeBeta.CanonicalID, Label: probeBeta.Label, Source: "ask-dev"}}
	}
	withMutate := func(id string, mutate func(*InvestigationRequest)) func([]anchorProbeTurn) InvestigationRequest {
		return func([]anchorProbeTurn) InvestigationRequest {
			request := needTurnRequest(id, true)
			mutate(&request)
			return request
		}
	}
	first := anchorProbeStep{request: firstTurnRequest("request_vocab_first", true), response: identityProvenResponse(probeAlpha)}
	refused := FrameGate{Outcome: FrameGateRefusedBasis, RefuseBasis: CohortMemberKindUnservable, DeclaredMemberKind: SubjectTeam}
	follow := func(id string, seed func(*staticResultStore)) anchorProbeStep {
		return anchorProbeStep{request: followUp(id, "", nil), seed: seed, response: emptyProbeResponse()}
	}
	return []anchorSiteScenario{
		{name: "caller hint", run: anchorProbeScenario(anchorProbeStep{request: withMutate("request_vocab_hint", alphaHint), response: needTurnResponse{
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{probeAlpha}}, bases: provenCommitBases(probeAlpha)}})},
		{name: "replaced by caller", run: anchorProbeScenario(first, anchorProbeStep{request: followUp("request_vocab_replace", "", betaHint), response: needTurnResponse{
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{probeBeta}}, bases: provenCommitBases(probeBeta)}})},
		{name: "ambiguous proof", run: anchorProbeScenario(anchorProbeStep{request: firstTurnRequest("request_vocab_ambiguous", true), response: identityProvenResponse(probeAlpha, probeBeta)})},
		{name: "carried not evaluated", run: anchorProbeScenario(first, anchorProbeStep{request: followUp("request_vocab_refused", "", nil), gate: &refused, response: emptyProbeResponse()})},
		{name: "window confirmed", run: anchorProbeScenario(
			anchorProbeStep{request: firstTurnRequest("request_vocab_gate_one", false), windowed: true, response: identityProvenResponse(probeAlpha)},
			anchorProbeStep{request: followUp("request_vocab_gate_two", "", nil), response: emptyProbeResponse()})},
		{name: "carried reconfirmed", run: anchorProbeScenario(first, anchorProbeStep{request: followUp("request_vocab_reconfirm", "", nil), anchorKind: SubjectProject, response: needTurnResponse{
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{probeAlpha}}, bases: provenCommitBases(probeAlpha)}})},
		{name: "contested then silent", run: anchorProbeScenario(first,
			anchorProbeStep{request: followUp("request_vocab_contest", "And how many teams contribute to it?", nil), response: identityProvenResponse(probeBeta)},
			anchorProbeStep{request: followUp("request_vocab_contest_silent", "And how many teams contribute to it?", nil), response: emptyProbeResponse()})},
		{name: "parent unloadable", run: anchorProbeScenario(anchorProbeStep{request: withMutate("request_vocab_unloadable", func(r *InvestigationRequest) {
			r.ParentResultID = "result_vocab_missing"
		}), response: emptyProbeResponse()})},
		{name: "parent absent", run: anchorProbeScenario(first, follow("request_vocab_absent", mutateStoredBindings(func(s *PersistedSemanticState) { s.AnchorBinding = nil })))},
		{name: "parent invalid", run: anchorProbeScenario(first, follow("request_vocab_invalid", mutateStoredBindings(func(s *PersistedSemanticState) {
			if s.AnchorBinding != nil {
				s.AnchorBinding.State = "unknown_state"
			}
		})))},
		{name: "parent stale epoch", run: anchorProbeScenario(first, follow("request_vocab_stale", mutateStoredBindings(func(s *PersistedSemanticState) {
			if s.AnchorBinding != nil {
				s.AnchorBinding.GraphEpoch = 5
			}
		})))},
		{name: "caller receipt", run: func(t *testing.T, sink EngineTelemetry, rec *recordingTelemetry, off bool) InvestigationResult {
			h := newNeedTurnHarness(t, nil, func(d *EngineDependencies) {
				d.AnchorVerifier = func(context.Context, string, contractsv1.ContextFabricSubjectKind, string, string) (bool, AnchorVerificationReason) {
					return true, AnchorVerificationValid
				}
				d.AnchorMembershipVerifier = func(context.Context, storage.Principal, RequestedScope, ResolvedGraphBinding, contractsv1.ContextFabricSubjectKind, string, string) (bool, AnchorVerificationReason) {
					return true, AnchorVerificationValid
				}
			})
			h.telemetry, h.engine.telemetry, h.engine.anchorBindingShadowDisabled = rec, sink, off
			offer := candidateOfferingNeedResponse()
			offer.material.Missing = []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedSubjectAnchor}
			offer.material.CandidateOptions = nil
			offer.material.AnchorOptions = []AnchorOption{{Label: "need-r2", Kind: SubjectRepository, CanonicalID: "repository:need-r2", MatchedTermHash: "aa11bb22cc33dd44ee55ff66", OfferSource: "engine"}}
			one := h.turn(needTurnRequest("request_vocab_receipt_offer", true), offer)
			request := needTurnRequest("request_vocab_receipt_redeem", true)
			request.PriorAnchorReceipts = []BoundSubjectReceipt{{ResultID: one.result.ResultID, ReceiptID: one.result.StructureNeeds.AnchorOptions[0].ReceiptID}}
			return h.turn(request, committingNeedResponse()).result
		}},
	}
}

// anchorStoreErrorDriver runs one decisive turn against a store whose Save
// fails with err, through the real engine.
func anchorStoreErrorDriver(t *testing.T, sink EngineTelemetry, err error) {
	t.Helper()
	rig := newAnchorProbeRig(t, false)
	rig.engine.telemetry = sink
	rig.engine.results = failingSaveStore{staticResultStore: rig.store, saveErr: err}
	rig.interpreter.read("", false)
	rig.graph.response = identityProvenResponse(probeAlpha)
	_, _ = rig.engine.Investigate(context.Background(), acceptancePrincipal(), needTurnRequest("request_vocab_store_error", true))
}

// RunAnchorBindingVocabularyForTest runs every driver of the transition line
// with the production slog JSON handler and returns each driver's output.
func RunAnchorBindingVocabularyForTest(t *testing.T) []AnchorBindingVocabularyLog {
	t.Helper()
	var out []AnchorBindingVocabularyLog
	for _, scenario := range append(anchorSiteScenarios(), anchorVocabularyScenarios()...) {
		var buf bytes.Buffer
		anchorSiteArm(t, scenario, false, &buf)
		out = append(out, AnchorBindingVocabularyLog{Name: scenario.name, Log: buf.Bytes()})
	}
	slogSink := func(buf *bytes.Buffer) SlogEngineTelemetry {
		return NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	}
	for name, err := range map[string]error{
		"store replay conflict":  fmt.Errorf("store: %w", ErrSemanticStateReplayConflict),
		"store failure":          errors.New("store: connection reset by peer"),
		"store payload rejected": errors.Join(errWorkItemTuplePayloadRejected, errors.New("foreign evidence")),
	} {
		var buf bytes.Buffer
		anchorStoreErrorDriver(t, slogSink(&buf), err)
		out = append(out, AnchorBindingVocabularyLog{Name: name, Log: buf.Bytes()})
	}
	// saveResult itself, for the two outcomes no Investigate exit reaches: a
	// Save with no decision attached, and a binding that cannot encode.
	var direct bytes.Buffer
	engine := &Engine{results: &staticResultStore{results: map[string]InvestigationResult{}}, telemetry: slogSink(&direct)}
	_ = engine.saveResult(context.Background(), acceptancePrincipal(), BudgetAssertContinuationRefusal, InvestigationResult{ResultID: "result_vocab_untracked"}, nil, nil, "", 0, "", absentSemanticState(SemanticStateAbsenceContinuationRefused))
	state := BuildSemanticState(SemanticStateInput{
		Outcome:         QuestionFamilyOutcome{Family: QuestionFamilyUnclassified, Source: QuestionFamilySourceNone},
		FamilyVersion:   QuestionFamilyTableVersion,
		RequestIdentity: SemanticRequestIdentityOf(validInvestigationRequest(), ""),
	})
	huge := heldBinding(AnchorBindingBound, anchorRef{Kind: SubjectRepository, ID: strings.Repeat(`"`, SemanticStateMaxEncodedBytes)})
	tracker := &anchorBindingTracker{parent: anchorBindingParent{ResultID: "result_vocab_parent", Status: AnchorBindingParentPresent, Binding: huge}, evaluation: AnchorBindingEvaluationNotResolved, epoch: 7}
	_ = engine.saveResult(context.Background(), acceptancePrincipal(), BudgetAssertDecisive, InvestigationResult{ResultID: "result_vocab_unencodable"}, nil, nil, "", 0, "", semanticStateCapture{Write: SemanticStateOf(state)}.withAnchorShadow(tracker))
	return append(out, AnchorBindingVocabularyLog{Name: "direct saves", Log: direct.Bytes()})
}
