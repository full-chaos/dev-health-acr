package contextfabric

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

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
		"same question carried silent": {
			{request: firstTurnRequest("request_cert_silent_one", true), response: identityProvenResponse(probeAlpha)},
			{request: followUp("request_cert_silent_two", "", nil), verified: true, response: emptyProbeResponse()},
		},
		"changed question refused": {
			{request: firstTurnRequest("request_cert_changed_one", true), response: identityProvenResponse(probeAlpha)},
			{request: followUp("request_cert_changed_two", "And how many teams contribute to it?", nil), verified: true, response: emptyProbeResponse()},
		},
		"follow-up alias contested": {
			{request: firstTurnRequest("request_cert_alias_one", true), response: identityProvenResponse(probeAlpha)},
			{request: followUp("request_cert_alias_two", "", nil), verified: true, response: identityProvenResponse(probeBeta)},
		},
		"model kind conflict": {
			{request: firstTurnRequest("request_cert_kind_one", true), anchorKind: SubjectRepository, response: identityProvenResponse(probeAlpha)},
			{request: followUp("request_cert_kind_two", "", nil), anchorKind: SubjectProject, verified: true, response: needTurnResponse{
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
		if step.ownership != nil {
			step.ownership(rig.ownership)
		}
		if step.verified {
			rig.verify()
		}
		if step.countOn {
			rig.engine.anchorBindingCountEnabled = true
		}
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
		return anchorProbeStep{request: followUp(id, "", nil), seed: seed, verified: true, response: emptyProbeResponse()}
	}
	scenarios := []anchorSiteScenario{
		{name: "caller hint", run: anchorProbeScenario(anchorProbeStep{request: withMutate("request_vocab_hint", alphaHint), response: needTurnResponse{
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{probeAlpha}}, bases: provenCommitBases(probeAlpha)}})},
		{name: "replaced by caller", run: anchorProbeScenario(first, anchorProbeStep{request: followUp("request_vocab_replace", "", betaHint), verified: true, response: needTurnResponse{
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{probeBeta}}, bases: provenCommitBases(probeBeta)}})},
		{name: "ambiguous proof", run: anchorProbeScenario(anchorProbeStep{request: firstTurnRequest("request_vocab_ambiguous", true), response: identityProvenResponse(probeAlpha, probeBeta)})},
		{name: "single subject count", run: anchorProbeScenario(anchorProbeStep{request: firstTurnRequest("request_vocab_single_subject", true), response: needTurnResponse{
			resolution: SubjectResolution{Candidates: []SubjectCandidate{scopeAnchorMatch(probeTeam)}, Committed: []SubjectRef{probeTeam}}, bases: provenCommitBases(probeTeam)}})},
		{name: "carried not evaluated", run: anchorProbeScenario(first, anchorProbeStep{request: followUp("request_vocab_refused", "", nil), gate: &refused, verified: true, response: emptyProbeResponse()})},
		{name: "window confirmed", run: anchorProbeScenario(
			anchorProbeStep{request: firstTurnRequest("request_vocab_gate_one", false), windowed: true, response: identityProvenResponse(probeAlpha)},
			anchorProbeStep{request: followUp("request_vocab_gate_two", "", nil), verified: true, response: emptyProbeResponse()})},
		{name: "carried reconfirmed", run: anchorProbeScenario(first, anchorProbeStep{request: followUp("request_vocab_reconfirm", "", nil), anchorKind: SubjectProject, verified: true, response: needTurnResponse{
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{probeAlpha}}, bases: provenCommitBases(probeAlpha)}})},
		{name: "contested then silent", run: anchorProbeScenario(first,
			anchorProbeStep{request: followUp("request_vocab_contest", "", nil), verified: true, response: identityProvenResponse(probeBeta)},
			anchorProbeStep{request: followUp("request_vocab_contest_silent", "", nil), verified: true, response: emptyProbeResponse()})},
		{name: "parent unloadable", run: anchorProbeScenario(anchorProbeStep{request: withMutate("request_vocab_unloadable", func(r *InvestigationRequest) {
			r.ParentResultID = "result_vocab_missing"
		}), response: emptyProbeResponse()})},
		{name: "parent absent", run: anchorProbeScenario(first, follow("request_vocab_absent", mutateStoredBindings(func(s *PersistedSemanticState) { setBindingMember(s, nil) })))},
		{name: "parent invalid", run: anchorProbeScenario(first, follow("request_vocab_invalid", mutateStoredBindings(func(s *PersistedSemanticState) {
			editBindingMember(s, func(b *AnchorBinding) { b.State = "unknown_state" })
		})))},
		{name: "parent stale epoch", run: anchorProbeScenario(first, follow("request_vocab_stale", mutateStoredBindings(func(s *PersistedSemanticState) {
			editBindingMember(s, func(b *AnchorBinding) { b.GraphEpoch = 5 })
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
	// Every ownership read outcome, and every comparison of a held binding's
	// proven freshness with this turn's, through the real engine.
	reads := func(read AnchorFreshnessRead, err error) func(*probeOwnership) {
		return func(o *probeOwnership) { o.read, o.err = read, err }
	}
	w2 := probeOwnershipW1
	w2.OwnershipWatermark = "ownership-w2"
	absent := AnchorFreshnessRead{ServingEpoch: 0, Recorded: false}
	otherEpoch := probeOwnershipW1
	otherEpoch.ServingEpoch = 3
	oversize := probeOwnershipW1
	oversize.OwnershipWatermark = strings.Repeat("w", SemanticStateMaxTermBytes+1)
	firstUnder := func(id string, read AnchorFreshnessRead, err error) anchorProbeStep {
		return anchorProbeStep{request: firstTurnRequest(id, true), ownership: reads(read, err), response: identityProvenResponse(probeAlpha)}
	}
	followUnder := func(id string, read AnchorFreshnessRead, err error) anchorProbeStep {
		step := follow(id, nil)
		step.ownership = reads(read, err)
		return step
	}
	scenarios = append(scenarios,
		anchorSiteScenario{name: "ownership read failed", run: anchorProbeScenario(firstUnder("request_vocab_own_failed", AnchorFreshnessRead{}, errors.New("checkpoint read failed")))},
		anchorSiteScenario{name: "ownership source absent", run: anchorProbeScenario(firstUnder("request_vocab_own_absent", absent, nil))},
		anchorSiteScenario{name: "ownership epoch mismatch", run: anchorProbeScenario(firstUnder("request_vocab_own_epoch", otherEpoch, nil))},
		anchorSiteScenario{name: "ownership unstorable", run: anchorProbeScenario(firstUnder("request_vocab_own_oversize", oversize, nil))},
		anchorSiteScenario{name: "ownership unmoved", run: anchorProbeScenario(firstUnder("request_vocab_own_unmoved_one", probeOwnershipW1, nil), followUnder("request_vocab_own_unmoved_two", probeOwnershipW1, nil))},
		anchorSiteScenario{name: "ownership moved", run: anchorProbeScenario(firstUnder("request_vocab_own_moved_one", probeOwnershipW1, nil), followUnder("request_vocab_own_moved_two", w2, nil))},
		anchorSiteScenario{name: "ownership not acquired", run: anchorProbeScenario(firstUnder("request_vocab_own_lost_one", probeOwnershipW1, nil), followUnder("request_vocab_own_lost_two", AnchorFreshnessRead{}, errors.New("checkpoint read failed")))},
		anchorSiteScenario{name: "ownership proven unrecorded", run: anchorProbeScenario(firstUnder("request_vocab_own_unrec_one", absent, nil), followUnder("request_vocab_own_unrec_two", probeOwnershipW1, nil))},
	)
	// Every admission outcome of a held parent binding, through the real
	// engine.
	edited := func(id string, edit func(*AnchorBinding)) anchorProbeStep {
		return follow(id, mutateStoredBindings(func(s *PersistedSemanticState) { editBindingMember(s, edit) }))
	}
	unverified := follow("request_vocab_adm_unwired", nil)
	unverified.verified = false
	scenarios = append(scenarios,
		anchorSiteScenario{name: "admission refused policy", run: anchorProbeScenario(first, edited("request_vocab_adm_policy", func(b *AnchorBinding) { b.PolicyVersion = "" }))},
		anchorSiteScenario{name: "admission refused question changed", run: anchorProbeScenario(first, anchorProbeStep{request: followUp("request_vocab_adm_changed", "And how many teams contribute to it?", nil), verified: true, response: emptyProbeResponse()})},
		anchorSiteScenario{name: "admission refused question indeterminate", run: anchorProbeScenario(first, anchorProbeStep{request: followUp("request_vocab_adm_indeterminate", "??", nil), verified: true, response: emptyProbeResponse()})},
		anchorSiteScenario{name: "admission refused origin unverifiable", run: anchorProbeScenario(first, edited("request_vocab_adm_origin", func(b *AnchorBinding) { b.OriginResultID = "result_vocab_origin_missing" }))},
		anchorSiteScenario{name: "admission refused expired", run: anchorProbeScenario(first, edited("request_vocab_adm_expired", func(b *AnchorBinding) { b.ProvenAt = b.ProvenAt.Add(-DefaultAnchorBindingMaxAge - time.Second) }))},
		anchorSiteScenario{name: "admission refused subject unreadable unwired", run: anchorProbeScenario(first, unverified)},
		anchorSiteScenario{name: "admission refused subject unreadable refused", run: func(t *testing.T, sink EngineTelemetry, rec *recordingTelemetry, off bool) InvestigationResult {
			rig := newAnchorProbeRig(t, off)
			rig.telemetry, rig.engine.telemetry = rec, sink
			rig.interpreter.read("", false)
			one := rig.turn(t, needTurnRequest("request_vocab_adm_refused_one", true), identityProvenResponse(probeAlpha))
			rig.engine.candidateVerifier = func(context.Context, storage.Principal, RequestedScope, ResolvedGraphBinding, contractsv1.ContextFabricSubjectKind, string) (bool, CandidateVerificationReason) {
				return false, CandidateVerificationClaimLost
			}
			return rig.turn(t, continuingNeedTurn(needTurnRequest("request_vocab_adm_refused_two", true), one.result.ResultID), emptyProbeResponse()).result
		}},
	)
	// Every bound-anchor-kind decision with the count switch on, through the
	// real engine.
	committedAlpha := needTurnResponse{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{probeAlpha}}, bases: provenCommitBases(probeAlpha)}
	firstOn := first
	firstOn.countOn = true
	scenarios = append(scenarios,
		anchorSiteScenario{name: "bound kind none", run: anchorProbeScenario(firstOn)},
		anchorSiteScenario{name: "bound kind agrees", run: anchorProbeScenario(firstOn, anchorProbeStep{request: followUp("request_vocab_kind_agrees", "", nil), anchorKind: SubjectRepository, verified: true, response: committedAlpha})},
		anchorSiteScenario{name: "bound kind applied carried", run: anchorProbeScenario(firstOn, anchorProbeStep{request: followUp("request_vocab_kind_carried", "", nil), anchorKind: SubjectProject, verified: true, response: committedAlpha})},
		// A held binding of the frame's own member kind: the frame refuses it
		// as the anchor, so the model's kind stands.
		anchorSiteScenario{name: "bound kind rejected by frame", run: anchorProbeScenario(firstOn,
			edited("request_vocab_kind_member", func(b *AnchorBinding) {
				b.Kind, b.CanonicalID, b.Witness = probeTeam.Kind, probeTeam.CanonicalID, []string{string(probeTeam.Kind) + ":" + probeTeam.CanonicalID}
			}))},
	)
	// The subject-substitution guard's firing and redeemed outcomes, through
	// the real engine, so the line's substitution_guard key reaches each one.
	scenarios = append(scenarios,
		anchorSiteScenario{name: "guard clarified", run: guardedProbeScenario(true, true, nil)},
		anchorSiteScenario{name: "guard refused", run: guardedProbeScenario(true, false, nil)},
		anchorSiteScenario{name: "guard refused remembered unavailable", run: guardedProbeScenario(false, false, nil)},
		anchorSiteScenario{name: "guard clarified remembered unavailable", run: guardedProbeScenario(false, true, nil)},
		// The same question, an admitted carry, and the caller redeeming the
		// parent's own offer for another identity: the caller's choice
		// replaces the held anchor.
		anchorSiteScenario{name: "replaced by a redeemed choice", run: func(t *testing.T, sink EngineTelemetry, rec *recordingTelemetry, off bool) InvestigationResult {
			rig := newAnchorProbeRig(t, off)
			rig.telemetry, rig.engine.telemetry = rec, sink
			rig.verify()
			offering := identityProvenResponse(probeAlpha)
			offering.resolution.Candidates = append(offering.resolution.Candidates, SubjectCandidate{
				ReceiptID: "receipt_probe_beta_offer", Subject: probeBeta, State: ResolutionAmbiguous,
				MatchedTerms: []string{"a"}, MatchReasons: []string{"matched"}, Confidence: 0.4, EvidenceRefIDs: []string{},
			})
			rig.interpreter.read("", false)
			one := rig.turn(t, needTurnRequest("request_vocab_replace_one", true), offering)
			follow := continuingNeedTurn(needTurnRequest("request_vocab_replace_two", true), one.result.ResultID)
			follow.PriorSubjectReceipts = []BoundSubjectReceipt{{ResultID: one.result.ResultID, ReceiptID: "receipt_probe_beta_offer"}}
			rig.interpreter.read("", false)
			return rig.turn(t, follow, needTurnResponse{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{probeBeta}}, bases: provenCommitBases(probeBeta)}).result
		}},
		anchorSiteScenario{name: "guard redeemed choice", run: guardedProbeScenario(false, true, func(first InvestigationResult, r *InvestigationRequest) {
			r.PriorSubjectReceipts = []BoundSubjectReceipt{{ResultID: first.ResultID, ReceiptID: "receipt_probe_beta_offer"}}
		})},
	)
	// The parent row's binding under every persistable reason and every held
	// proof, so the pre-entry keys reach each member through the real read.
	for _, reason := range anchorBindingReasons() {
		if reason == AnchorBindingReasonUnrecorded {
			continue
		}
		reason := reason
		scenarios = append(scenarios, anchorSiteScenario{name: "parent reason " + string(reason), run: anchorProbeScenario(first, follow("request_vocab_from_"+string(reason), mutateStoredBindings(func(s *PersistedSemanticState) {
			editBindingMember(s, func(b *AnchorBinding) { b.Reason = reason })
		})))})
	}
	for _, proof := range []AnchorBindingProof{AnchorBindingProofCallerReceipt, AnchorBindingProofCallerHint} {
		proof := proof
		scenarios = append(scenarios, anchorSiteScenario{name: "parent proof " + string(proof), run: anchorProbeScenario(first, follow("request_vocab_proof_"+string(proof), mutateStoredBindings(func(s *PersistedSemanticState) {
			editBindingMember(s, func(b *AnchorBinding) { b.Proof = proof })
		})))})
	}
	return scenarios
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
	// The admission re-read cancelled before and during the verifier: no
	// Investigate driver controls the turn's context, so the tracker is
	// built as admission leaves it and saved through saveResult itself.
	for i, check := range []SubjectSubstitutionRememberedCheck{SubjectSubstitutionRememberedCancelledBefore, SubjectSubstitutionRememberedCancelledDuring} {
		held := heldBinding(AnchorBindingBound, bindAlpha)
		cancelled := &anchorBindingTracker{
			parent:     anchorBindingParent{ResultID: "result_vocab_parent", Status: AnchorBindingParentPresent, Binding: held, StoredEpoch: held.GraphEpoch},
			evaluation: AnchorBindingEvaluationNotResolved, epoch: held.GraphEpoch,
			admission: AnchorBindingAdmissionRefusedSubjectUnreadable, admissionRemembered: rememberedSubjectCheck{Check: check, ContextError: context.Canceled.Error()},
		}
		_ = engine.saveResult(context.Background(), acceptancePrincipal(), BudgetAssertDecisive, InvestigationResult{ResultID: fmt.Sprintf("result_vocab_cancelled_%d", i)}, nil, nil, "", 0, "", semanticStateCapture{Write: SemanticStateOf(state)}.withAnchorShadow(cancelled))
	}
	// A redeemed anchor receipt's kind applied to the reading: the probe rig
	// redeems no anchor receipt over a scoped frame, so the tracker is built
	// as applyBoundAnchorKind leaves it (its decision table is
	// TestApplyBoundAnchorKindDecisionTable) and saved through saveResult.
	appliedReceipt := &anchorBindingTracker{parent: anchorBindingParent{Status: AnchorBindingParentNoReference}, evaluation: AnchorBindingEvaluationNotResolved, epoch: 7, boundKind: BoundAnchorKindAppliedReceipt,
		receipt: &confirmedStructureMember{Member: contractsv1.ContextFabricStructureNeedSubjectAnchor, AppliedKind: bindAlpha.Kind, AppliedValue: bindAlpha.ID}}
	_ = engine.saveResult(context.Background(), acceptancePrincipal(), BudgetAssertDecisive, InvestigationResult{ResultID: "result_vocab_applied_receipt"}, nil, nil, "", 0, "", semanticStateCapture{Write: SemanticStateOf(state)}.withAnchorShadow(appliedReceipt))
	return append(out, AnchorBindingVocabularyLog{Name: "direct saves", Log: direct.Bytes()})
}

// guardedProbeScenario is the follow-up substitution the guard decides on:
// turn one commits alpha and offers beta; turn two names it and commits
// beta. verified wires a candidate verifier that re-reads alpha as valid,
// clarify is the follow-up's AllowClarification, and redeem, when set,
// edits the follow-up to redeem the parent's own offer.
func guardedProbeScenario(verified, clarify bool, redeem func(InvestigationResult, *InvestigationRequest)) func(*testing.T, EngineTelemetry, *recordingTelemetry, bool) InvestigationResult {
	return func(t *testing.T, sink EngineTelemetry, rec *recordingTelemetry, off bool) InvestigationResult {
		rig := newAnchorProbeRig(t, off)
		rig.telemetry = rec
		rig.engine.telemetry = sink
		if verified {
			rig.engine.candidateVerifier = func(context.Context, storage.Principal, RequestedScope, ResolvedGraphBinding, contractsv1.ContextFabricSubjectKind, string) (bool, CandidateVerificationReason) {
				return true, CandidateVerificationValid
			}
		}
		offering := identityProvenResponse(probeAlpha)
		offering.resolution.Candidates = append(offering.resolution.Candidates, SubjectCandidate{
			ReceiptID: "receipt_probe_beta_offer", Subject: probeBeta, State: ResolutionAmbiguous,
			MatchedTerms: []string{"a"}, MatchReasons: []string{"matched"}, Confidence: 0.4, EvidenceRefIDs: []string{},
		})
		rig.interpreter.read("", false)
		one := rig.turn(t, needTurnRequest("request_vocab_guard_one", true), offering)
		follow := continuingNeedTurn(needTurnRequest("request_vocab_guard_two", true), one.result.ResultID)
		follow.Question = "And how many teams contribute to it?"
		follow.Options.AllowClarification = clarify
		if redeem != nil {
			redeem(one.result, &follow)
		}
		rig.interpreter.read("", false)
		return rig.turn(t, follow, identityProvenResponse(probeBeta)).result
	}
}
