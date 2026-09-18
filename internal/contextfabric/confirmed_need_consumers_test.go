package contextfabric

// Pins for the subject_candidate, subject_handle and window consumers of the
// per-need confirmation ledger (CHAOS-5734). The multi-turn tests drive the
// real Engine.Investigate for every turn and persist each turn's saved result
// and semantic state into the store the next turn reads, so a turn-three
// assertion reads what turn two actually wrote.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

var needTurnProject = SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}

type needTurnResponse struct {
	resolution SubjectResolution
	material   StructureOfferMaterial
	bases      CommitBasisSet
	// err, when set, is what ResolveSubjects itself reports for this turn --
	// e.g. a wrapped ErrGraphNotProjected -- instead of the scripted
	// resolution succeeding. The zero value (nil) is every existing
	// scenario's own unchanged behavior.
	err error
}

type needTurnCall struct {
	request InvestigationRequest
	kind    *ConfirmedExpectedKind
	anchor  *ConfirmedAnchorSelection
}

// needTurnGraph returns the scripted response for the current turn and
// records every ResolveSubjects call, with what the engine handed the port.
type needTurnGraph struct {
	response needTurnResponse
	calls    []needTurnCall
}

func (g *needTurnGraph) ResolveInvestigationBinding(context.Context, storage.Principal) (ResolvedGraphBinding, error) {
	return ResolvedGraphBinding{GraphKey: "need-turn-key", Epoch: 0}, nil
}

func (g *needTurnGraph) ResolveSubjects(_ context.Context, _ storage.Principal, request InvestigationRequest, _ InterpretedQuestion, _ ResolvedGraphBinding, kind *ConfirmedExpectedKind, anchor *ConfirmedAnchorSelection, _ *QuestionFrame, _ SubjectKind) (SubjectResolution, StructureOfferMaterial, CommitBasisSet, CommitDecisionDigestSet, error) {
	g.calls = append(g.calls, needTurnCall{request: request, kind: kind, anchor: anchor})
	return g.response.resolution, g.response.material, g.response.bases, nil, g.response.err
}

func (g *needTurnGraph) DiscoverContext(context.Context, storage.Principal, GraphDiscoveryRequest) (GraphContext, error) {
	return emptyGraphContext(), nil
}

type needVerifierCall struct {
	kind      contractsv1.ContextFabricSubjectKind
	patternID string
	value     string
}

type needTurnHarness struct {
	t          *testing.T
	engine     *Engine
	graph      *needTurnGraph
	store      *staticResultStore
	telemetry  *recordingTelemetry
	next       int
	candidates []needVerifierCall
	handles    []needVerifierCall
	// refuseCandidates/refuseHandles make the verifier report the value no
	// longer holds, from the moment they are set.
	refuseCandidates bool
	refuseHandles    bool
	// historical makes the interpreter move the axis off current, which a
	// request carrying a window receipt answers with the axis-conflict veto.
	historical bool
	// windowless makes the interpreter return a question with no window class,
	// so a turn with no request-side window gets no inferred default at all.
	windowless bool
	// saveKeys is every time-axis reuse key Save received, in order.
	saveKeys []string
}

// needKeyStore records the time-axis reuse key of every Save.
type needKeyStore struct {
	*staticResultStore
	h *needTurnHarness
}

func (s *needKeyStore) Save(ctx context.Context, principal storage.Principal, result InvestigationResult, watermarks SourceWatermarkSnapshot, epoch RebuildEpoch, timeAxisKey string, retrieval ReuseRetrievalIdentity, prompts ReusePromptVersions, authorities ReuseVersionAuthorities, graphEpoch int64, ancestryParent string, semantic SemanticStateWrite) error {
	s.h.saveKeys = append(s.h.saveKeys, timeAxisKey)
	return s.staticResultStore.Save(ctx, principal, result, watermarks, epoch, timeAxisKey, retrieval, prompts, authorities, graphEpoch, ancestryParent, semantic)
}

// needTurnOutcome is one turn's served result plus everything the turn
// emitted, sliced to THIS turn so a later turn cannot satisfy an earlier
// turn's assertion.
type needTurnOutcome struct {
	result       InvestigationResult
	calls        []needTurnCall
	ledgers      []ConfirmedNeedLedgerEvent
	windows      []confirmedNeedLedgerWindowRecord
	kindCarries  []kindCarryRecord
	windowCarry  []windowCarryRecord
	windowCanons []WindowCanonicalizationOutcome
	reuseBypass  []AnswerReuseBypassReason
	saved        *PersistedSemanticState
	saveKey      string
}

type needHarnessOption func(*EngineDependencies)

func withoutNeedVerifiers() needHarnessOption {
	return func(deps *EngineDependencies) {
		deps.CandidateVerifier = nil
		deps.HandleVerifier = nil
	}
}

func newNeedTurnHarness(t *testing.T, store *staticResultStore, options ...needHarnessOption) *needTurnHarness {
	t.Helper()
	if store == nil {
		store = &staticResultStore{}
	}
	if store.results == nil {
		store.results = map[string]InvestigationResult{}
	}
	if store.states == nil {
		store.states = map[string]*PersistedSemanticState{}
	}
	h := &needTurnHarness{t: t, graph: &needTurnGraph{}, store: store, telemetry: &recordingTelemetry{}}
	fresh := validInvestigationResult()
	deps := EngineDependencies{
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			if h.historical {
				asOf := time.Unix(100, 0).UTC()
				return InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalValidTime, AsOf: &asOf}}, nil
			}
			if h.windowless {
				return InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}}, nil
			}
			// A trend class gives a turn with no window of its own a class
			// default, which the window gate answers with a clarification.
			return InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}, WindowClass: WindowClassTrendAssessment}, nil
		}),
		Graph: h.graph,
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{Facts: []CanonicalFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}, Version: "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{}}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return fresh, nil
		}),
		Results:   &needKeyStore{staticResultStore: store, h: h},
		Telemetry: h.telemetry,
		CandidateVerifier: func(_ context.Context, _ storage.Principal, _ RequestedScope, _ ResolvedGraphBinding, kind contractsv1.ContextFabricSubjectKind, canonicalID string) (bool, CandidateVerificationReason) {
			h.candidates = append(h.candidates, needVerifierCall{kind: kind, value: canonicalID})
			if h.refuseCandidates {
				return false, CandidateVerificationClaimLost
			}
			return true, CandidateVerificationValid
		},
		HandleVerifier: func(_ context.Context, _ string, kind contractsv1.ContextFabricSubjectKind, patternID, value string) (bool, HandleVerificationReason) {
			h.handles = append(h.handles, needVerifierCall{kind: kind, patternID: patternID, value: value})
			if h.refuseHandles {
				return false, HandleVerificationCensusUnavailable
			}
			return true, HandleVerificationValid
		},
	}
	for _, option := range options {
		option(&deps)
	}
	engine, err := NewEngine(deps, EngineOptions{
		ServiceVersion: "need-consumers-test",
		Now:            func() time.Time { return time.Unix(500, 0).UTC() },
		NewResultID: func() string {
			h.next++
			return fmt.Sprintf("result_need_turn_%04d", h.next)
		},
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	h.engine = engine
	return h
}

func (h *needTurnHarness) turn(request InvestigationRequest, response needTurnResponse) needTurnOutcome {
	h.t.Helper()
	h.graph.response = response
	callsMark, ledgerMark, windowMark := len(h.graph.calls), len(h.telemetry.confirmedNeedLedgers), len(h.telemetry.confirmedNeedLedgerWindows)
	kindMark, windowCarryMark, canonMark := len(h.telemetry.kindCarries), len(h.telemetry.windowCarries), len(h.telemetry.windowCanonicalizationOutcomes)
	reuseMark, keyMark := len(h.telemetry.answerReuseBypasses), len(h.saveKeys)
	h.store.saved, h.store.savedSemantic = nil, nil
	result, err := h.engine.Investigate(context.Background(), acceptancePrincipal(), request)
	if err != nil {
		h.t.Fatalf("Investigate(%s) error = %v", request.RequestID, err)
	}
	if h.store.saved == nil || len(h.saveKeys) != keyMark+1 {
		h.t.Fatalf("fixture defect: turn %s must save exactly once (saves=%d)", request.RequestID, len(h.saveKeys)-keyMark)
	}
	h.store.results[h.store.saved.ResultID] = *h.store.saved
	var saved *PersistedSemanticState
	if h.store.savedSemantic != nil && h.store.savedSemantic.State != nil {
		saved = h.store.savedSemantic.State
		h.store.states[h.store.saved.ResultID] = saved
	}
	return needTurnOutcome{
		result:       result,
		calls:        append([]needTurnCall(nil), h.graph.calls[callsMark:]...),
		ledgers:      append([]ConfirmedNeedLedgerEvent(nil), h.telemetry.confirmedNeedLedgers[ledgerMark:]...),
		windows:      append([]confirmedNeedLedgerWindowRecord(nil), h.telemetry.confirmedNeedLedgerWindows[windowMark:]...),
		kindCarries:  append([]kindCarryRecord(nil), h.telemetry.kindCarries[kindMark:]...),
		windowCarry:  append([]windowCarryRecord(nil), h.telemetry.windowCarries[windowCarryMark:]...),
		windowCanons: append([]WindowCanonicalizationOutcome(nil), h.telemetry.windowCanonicalizationOutcomes[canonMark:]...),
		reuseBypass:  append([]AnswerReuseBypassReason(nil), h.telemetry.answerReuseBypasses[reuseMark:]...),
		saved:        saved,
		saveKey:      h.saveKeys[keyMark],
	}
}

func needTurnRequest(requestID string, statedWindow bool) InvestigationRequest {
	request := validInvestigationRequest()
	if statedWindow {
		request = validInvestigationRequestWithConfirmedWindow()
	}
	request.RequestID = requestID
	return request
}

// continuingNeedTurn names parentID as the turn being continued and appends
// that exchange to the conversation -- the exchange SemanticRequestIdentityOf
// drops, so the digest equals the one the parent turn stored.
func continuingNeedTurn(request InvestigationRequest, parentID string) InvestigationRequest {
	request.ParentResultID = parentID
	request.Conversation = []contractsv1.ContextFabricConversationTurn{
		{TurnID: "turn_need_q", Role: contractsv1.ContextFabricConversationUser, Content: request.Question, CreatedAt: time.Unix(480, 0).UTC()},
		{TurnID: "turn_need_a", Role: contractsv1.ContextFabricConversationAssistant, Content: "Ask Dev is not release-ready.", CreatedAt: time.Unix(481, 0).UTC()},
	}
	return request
}

// continuingNeedTurnAgain continues a turn that was itself a continuation:
// its conversation is the parent's, plus the parent's own exchange, which is
// the exchange SemanticRequestIdentityOf drops -- so the digest equals the one
// the parent stored.
func continuingNeedTurnAgain(request, parentRequest InvestigationRequest, parentID string) InvestigationRequest {
	request.ParentResultID = parentID
	request.Conversation = append(append([]contractsv1.ContextFabricConversationTurn{}, parentRequest.Conversation...),
		contractsv1.ContextFabricConversationTurn{TurnID: "turn_need_q2", Role: contractsv1.ContextFabricConversationUser, Content: request.Question, CreatedAt: time.Unix(490, 0).UTC()},
		contractsv1.ContextFabricConversationTurn{TurnID: "turn_need_a2", Role: contractsv1.ContextFabricConversationAssistant, Content: "Ask Dev is still not release-ready.", CreatedAt: time.Unix(491, 0).UTC()},
	)
	return request
}

func committingNeedResponse() needTurnResponse {
	return needTurnResponse{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{needTurnProject}},
		bases:      provenCommitBases(needTurnProject),
	}
}

func needPoolCandidate(receiptID, canonicalID string) SubjectCandidate {
	return SubjectCandidate{
		ReceiptID:    receiptID,
		Subject:      SubjectRef{Kind: SubjectRepository, CanonicalID: canonicalID, Label: canonicalID},
		State:        contractsv1.ContextFabricResolutionAmbiguous,
		MatchReasons: []string{"semantic"},
		Confidence:   0.5,
	}
}

// candidateOfferingNeedResponse raises the subject_candidate need with two
// options, nothing committed.
func candidateOfferingNeedResponse() needTurnResponse {
	return needTurnResponse{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{needPoolCandidate("subr_need_pool_0001", "repository:need-r2"), needPoolCandidate("subr_need_pool_0002", "repository:need-r3")}, Committed: []SubjectRef{}},
		material: StructureOfferMaterial{
			Missing: []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedSubjectCandidate},
			CandidateOptions: []contractsv1.ContextFabricCandidateOption{
				{ReceiptID: "candr_needoffer0001", OptionID: "opt_need_cand_a", Label: "need-r2", Kind: SubjectRepository, CanonicalID: "repository:need-r2", OfferSource: contractsv1.ContextFabricStructureOfferEngine},
				{ReceiptID: "candr_needoffer0002", OptionID: "opt_need_cand_b", Label: "need-r3", Kind: SubjectRepository, CanonicalID: "repository:need-r3", OfferSource: contractsv1.ContextFabricStructureOfferEngine},
			},
		},
	}
}

// handleOfferingNeedResponse raises the subject_handle need, nothing
// committed.
func handleOfferingNeedResponse() needTurnResponse {
	return needTurnResponse{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{needPoolCandidate("subr_need_pool_0003", "repository:need-r4")}, Committed: []SubjectRef{}},
		material: StructureOfferMaterial{
			Missing: []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedSubjectHandle},
			HandleOptions: []contractsv1.ContextFabricHandleOption{
				{ReceiptID: "handr_needoffer0001", OptionID: "opt_need_handle", Label: "PR #532", Kind: SubjectPullRequest, PatternID: "pull_request_number", Value: "532", SourceColumn: "pull_requests.number", OfferSource: contractsv1.ContextFabricStructureOfferEngine},
			},
		},
	}
}

func soleLedgerEvent(t *testing.T, outcome needTurnOutcome) ConfirmedNeedLedgerEvent {
	t.Helper()
	if len(outcome.ledgers) != 1 {
		t.Fatalf("confirmed need ledger events = %#v, want exactly one for the turn", outcome.ledgers)
	}
	return outcome.ledgers[0]
}

func memberEntries(result InvestigationResult, member contractsv1.ContextFabricStructureNeedKind) []ConfirmedStructureEntry {
	var out []ConfirmedStructureEntry
	for _, entry := range result.ConfirmedStructure {
		if entry.Member == member {
			out = append(out, entry)
		}
	}
	return out
}

func carriedNeedEntry(member contractsv1.ContextFabricStructureNeedKind, value, parentID string) ConfirmedStructureEntry {
	return ConfirmedStructureEntry{
		Member: member, AppliedValue: value,
		Source:        contractsv1.ContextFabricStructureSourceCarried,
		PriorResultID: parentID,
		Provenance:    contractsv1.ContextFabricStructureClarificationConfirmed,
		Disposition:   contractsv1.ContextFabricStructureDispositionApplied,
	}
}

func lastPortCall(t *testing.T, outcome needTurnOutcome) needTurnCall {
	t.Helper()
	if len(outcome.calls) == 0 {
		t.Fatalf("ResolveSubjects was never called on this turn (status=%s)", outcome.result.Status)
	}
	return outcome.calls[len(outcome.calls)-1]
}

// candidateTurnsOneAndTwo drives turn one (raises subject_candidate) and turn
// two (redeems the first offer's receipt).
func candidateTurnsOneAndTwo(t *testing.T, h *needTurnHarness) (needTurnOutcome, needTurnOutcome, contractsv1.ContextFabricCandidateOption) {
	t.Helper()
	one := h.turn(needTurnRequest("request_need_candidate_one", true), candidateOfferingNeedResponse())
	if one.result.Status != InvestigationClarificationRequired || one.result.StructureNeeds == nil || len(one.result.StructureNeeds.CandidateOptions) != 2 {
		t.Fatalf("fixture defect: turn one must raise the candidate need with two options; status=%s needs=%#v", one.result.Status, one.result.StructureNeeds)
	}
	offer := one.result.StructureNeeds.CandidateOptions[0]
	request := needTurnRequest("request_need_candidate_two", true)
	request.PriorCandidateReceipts = []BoundSubjectReceipt{{ResultID: one.result.ResultID, ReceiptID: offer.ReceiptID}}
	two := h.turn(request, committingNeedResponse())
	return one, two, offer
}

// TestConfirmedNeedConsumers_CandidateThreeTurns is the three-turn
// subject-candidate-only continuation: turn one raises the need, turn two
// confirms it by receipt, turn three resends nothing and is served with the
// remembered candidate disclosed, carried forward and reverified -- and a
// turn three that changes its identity gets none of that.
func TestConfirmedNeedConsumers_CandidateThreeTurns(t *testing.T) {
	t.Parallel()
	h := newNeedTurnHarness(t, nil)
	_, two, offer := candidateTurnsOneAndTwo(t, h)
	want := []ConfirmedNeedEntry{{Member: contractsv1.ContextFabricStructureNeedSubjectCandidate, AppliedKind: offer.Kind, AppliedValue: offer.CanonicalID}}
	if two.saved == nil || !reflect.DeepEqual(two.saved.ConfirmedNeeds, want) {
		t.Fatalf("turn two ledger = %#v, want %#v: a redeemed candr_ receipt must be captured", two.saved, want)
	}

	t.Run("same identity: remembered, disclosed, carried forward, not re-raised", func(t *testing.T) {
		verified := len(h.candidates)
		three := h.turn(continuingNeedTurn(needTurnRequest("request_need_candidate_three", true), two.result.ResultID), committingNeedResponse())
		event := soleLedgerEvent(t, three)
		if event.Outcome != ConfirmedNeedLedgerHit || !reflect.DeepEqual(event.AppliedMembers, []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedSubjectCandidate}) ||
			event.AppliedCandidateKind != offer.Kind || event.AppliedCandidateValueHash != confirmedNeedValueHash(offer.CanonicalID) || len(event.Dropped) != 0 {
			t.Fatalf("ledger event = %#v, want a hit applying subject_candidate (kind %s, hashed value) with no drops", event, offer.Kind)
		}
		if got := memberEntries(three.result, contractsv1.ContextFabricStructureNeedSubjectCandidate); !reflect.DeepEqual(got, []ConfirmedStructureEntry{carriedNeedEntry(contractsv1.ContextFabricStructureNeedSubjectCandidate, offer.CanonicalID, two.result.ResultID)}) {
			t.Fatalf("turn three candidate disclosure = %#v, want exactly the carried entry naming turn two", got)
		}
		if three.result.StructureNeeds != nil {
			t.Fatalf("turn three StructureNeeds = %#v, want none: nothing re-raised", three.result.StructureNeeds)
		}
		if three.saved == nil || !reflect.DeepEqual(three.saved.ConfirmedNeeds, want) {
			t.Fatalf("turn three ledger = %#v, want %#v carried forward for turn four", three.saved, want)
		}
		if got := h.candidates[verified:]; !reflect.DeepEqual(got, []needVerifierCall{{kind: offer.Kind, value: offer.CanonicalID}}) {
			t.Fatalf("CandidateVerifier calls on turn three = %#v, want the remembered (kind, id) reverified exactly once", got)
		}
		// A remembered confirmation is not a receipt this turn: the answer
		// reuse lookup is bypassed for the parent reference, never reported as
		// confirmed structure.
		if !reflect.DeepEqual(three.reuseBypass, []AnswerReuseBypassReason{AnswerReuseBypassPriorResultReference}) {
			t.Fatalf("reuse bypass reasons = %#v, want exactly prior_result_reference", three.reuseBypass)
		}
		call := lastPortCall(t, three)
		if len(call.request.RequestedScope.SubjectHints) != 0 || len(call.request.SubjectHandles) != 0 || call.kind != nil || call.anchor != nil {
			t.Fatalf("ResolveSubjects received %#v, want no candidate-derived input: a fresh candr_ receipt reaches no resolution parameter, so a remembered one reaches none", call)
		}
	})

	t.Run("changed identity: ledger dropped, nothing disclosed, nothing carried", func(t *testing.T) {
		request := continuingNeedTurn(needTurnRequest("request_need_candidate_three_b", true), two.result.ResultID)
		request.RequestedScope.RepositorySlugs = []string{"full-chaos/dev-health-acr"}
		three := h.turn(request, committingNeedResponse())
		event := soleLedgerEvent(t, three)
		if event.Outcome != ConfirmedNeedLedgerDroppedIdentityChanged || len(event.AppliedMembers) != 0 {
			t.Fatalf("ledger event = %#v, want dropped_identity_changed applying nothing", event)
		}
		if got := memberEntries(three.result, contractsv1.ContextFabricStructureNeedSubjectCandidate); len(got) != 0 {
			t.Fatalf("turn three candidate disclosure = %#v, want none under a changed identity", got)
		}
		if three.saved != nil && len(three.saved.ConfirmedNeeds) != 0 {
			t.Fatalf("turn three ledger = %#v, want empty under a changed identity", three.saved.ConfirmedNeeds)
		}
	})
}

// TestConfirmedNeedConsumers_ARememberedCandidateNeverChangesTheOffers pins
// the parity limit: re-raising is resolution's decision, and neither a fresh
// candr_ receipt nor a remembered one suppresses or adds an offer. The same
// candidate-offering resolution on a same-identity and a changed-identity
// turn three serves identical offer material.
func TestConfirmedNeedConsumers_ARememberedCandidateNeverChangesTheOffers(t *testing.T) {
	t.Parallel()
	h := newNeedTurnHarness(t, nil)
	_, two, _ := candidateTurnsOneAndTwo(t, h)
	same := h.turn(continuingNeedTurn(needTurnRequest("request_need_offers_same", true), two.result.ResultID), candidateOfferingNeedResponse())
	changedRequest := continuingNeedTurn(needTurnRequest("request_need_offers_changed", true), two.result.ResultID)
	changedRequest.RequestedScope.RepositorySlugs = []string{"full-chaos/dev-health-acr"}
	changed := h.turn(changedRequest, candidateOfferingNeedResponse())
	if soleLedgerEvent(t, same).Outcome != ConfirmedNeedLedgerHit || soleLedgerEvent(t, changed).Outcome != ConfirmedNeedLedgerDroppedIdentityChanged {
		t.Fatalf("fixture defect: the two turns must differ in admission only")
	}
	if same.result.StructureNeeds == nil || changed.result.StructureNeeds == nil ||
		len(same.result.StructureNeeds.CandidateOptions) != 2 || len(changed.result.StructureNeeds.CandidateOptions) != 2 ||
		!reflect.DeepEqual(same.result.StructureNeeds.Missing, changed.result.StructureNeeds.Missing) {
		t.Fatalf("offers differ: same=%#v changed=%#v, want identical candidate offers on both", same.result.StructureNeeds, changed.result.StructureNeeds)
	}
}

// TestConfirmedNeedConsumers_HandleThreeTurns is the subject_handle twin of
// the candidate three-turn test, plus the explicit-handle supersession case.
func TestConfirmedNeedConsumers_HandleThreeTurns(t *testing.T) {
	t.Parallel()
	h := newNeedTurnHarness(t, nil)
	one := h.turn(needTurnRequest("request_need_handle_one", true), handleOfferingNeedResponse())
	if one.result.StructureNeeds == nil || len(one.result.StructureNeeds.HandleOptions) != 1 {
		t.Fatalf("fixture defect: turn one must raise the handle need; status=%s needs=%#v", one.result.Status, one.result.StructureNeeds)
	}
	offer := one.result.StructureNeeds.HandleOptions[0]
	twoRequest := needTurnRequest("request_need_handle_two", true)
	twoRequest.PriorHandleReceipts = []BoundSubjectReceipt{{ResultID: one.result.ResultID, ReceiptID: offer.ReceiptID}}
	two := h.turn(twoRequest, committingNeedResponse())
	want := []ConfirmedNeedEntry{{Member: contractsv1.ContextFabricStructureNeedSubjectHandle, AppliedKind: offer.Kind, AppliedValue: offer.Value, PatternID: offer.PatternID}}
	if two.saved == nil || !reflect.DeepEqual(two.saved.ConfirmedNeeds, want) {
		t.Fatalf("turn two ledger = %#v, want %#v: a redeemed handr_ receipt is captured with its pattern", two.saved, want)
	}

	t.Run("same identity: remembered, reverified with its pattern, disclosed, carried forward", func(t *testing.T) {
		verified := len(h.handles)
		three := h.turn(continuingNeedTurn(needTurnRequest("request_need_handle_three", true), two.result.ResultID), committingNeedResponse())
		event := soleLedgerEvent(t, three)
		if event.Outcome != ConfirmedNeedLedgerHit || !reflect.DeepEqual(event.AppliedMembers, []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedSubjectHandle}) ||
			event.AppliedHandleKind != offer.Kind || event.AppliedHandleValueHash != confirmedNeedValueHash(offer.Value) {
			t.Fatalf("ledger event = %#v, want a hit applying subject_handle", event)
		}
		if got := h.handles[verified:]; !reflect.DeepEqual(got, []needVerifierCall{{kind: offer.Kind, patternID: offer.PatternID, value: offer.Value}}) {
			t.Fatalf("HandleVerifier calls on turn three = %#v, want the remembered (kind, pattern, value) replayed once", got)
		}
		if got := memberEntries(three.result, contractsv1.ContextFabricStructureNeedSubjectHandle); !reflect.DeepEqual(got, []ConfirmedStructureEntry{carriedNeedEntry(contractsv1.ContextFabricStructureNeedSubjectHandle, offer.Value, two.result.ResultID)}) {
			t.Fatalf("turn three handle disclosure = %#v, want exactly the carried entry naming turn two", got)
		}
		if three.result.StructureNeeds != nil {
			t.Fatalf("turn three StructureNeeds = %#v, want none", three.result.StructureNeeds)
		}
		if three.saved == nil || !reflect.DeepEqual(three.saved.ConfirmedNeeds, want) {
			t.Fatalf("turn three ledger = %#v, want %#v carried forward", three.saved, want)
		}
		if call := lastPortCall(t, three); len(call.request.SubjectHandles) != 0 {
			t.Fatalf("ResolveSubjects received handles %#v, want none", call.request.SubjectHandles)
		}
	})

	t.Run("changed identity: ledger dropped, nothing disclosed", func(t *testing.T) {
		request := continuingNeedTurn(needTurnRequest("request_need_handle_three_b", true), two.result.ResultID)
		request.RequestedScope.RepositorySlugs = []string{"full-chaos/dev-health-acr"}
		three := h.turn(request, committingNeedResponse())
		if event := soleLedgerEvent(t, three); event.Outcome != ConfirmedNeedLedgerDroppedIdentityChanged || len(event.AppliedMembers) != 0 {
			t.Fatalf("ledger event = %#v, want dropped_identity_changed", event)
		}
		if got := memberEntries(three.result, contractsv1.ContextFabricStructureNeedSubjectHandle); len(got) != 0 {
			t.Fatalf("handle disclosure = %#v, want none", got)
		}
	})

	t.Run("an explicit handle this turn supersedes the remembered one", func(t *testing.T) {
		request := continuingNeedTurn(needTurnRequest("request_need_handle_three_c", true), two.result.ResultID)
		request.SubjectHandles = []contractsv1.ContextFabricRequestedHandle{{Kind: SubjectPullRequest, PatternID: "pull_request_number", Value: "777"}}
		three := h.turn(request, committingNeedResponse())
		if event := soleLedgerEvent(t, three); event.Outcome != ConfirmedNeedLedgerHit || len(event.AppliedMembers) != 0 {
			t.Fatalf("ledger event = %#v, want a hit applying nothing: the caller stated a handle this turn", event)
		}
		got := memberEntries(three.result, contractsv1.ContextFabricStructureNeedSubjectHandle)
		if len(got) != 1 || got[0].Source == contractsv1.ContextFabricStructureSourceCarried || got[0].AppliedValue != "777" {
			t.Fatalf("handle disclosure = %#v, want only the caller's own explicit 777", got)
		}
	})
}

// TestConfirmedNeedConsumers_CandidateSupersededByAFreshReceipt: a fresh
// candr_ receipt this turn wins over the remembered candidate -- disclosed as
// a receipt, never also as a carry, and the ledger moves to the new value.
func TestConfirmedNeedConsumers_CandidateSupersededByAFreshReceipt(t *testing.T) {
	t.Parallel()
	h := newNeedTurnHarness(t, nil)
	one, two, _ := candidateTurnsOneAndTwo(t, h)
	second := one.result.StructureNeeds.CandidateOptions[1]
	request := continuingNeedTurn(needTurnRequest("request_need_candidate_supersede", true), two.result.ResultID)
	request.PriorCandidateReceipts = []BoundSubjectReceipt{{ResultID: one.result.ResultID, ReceiptID: second.ReceiptID}}
	three := h.turn(request, committingNeedResponse())
	if event := soleLedgerEvent(t, three); event.Outcome != ConfirmedNeedLedgerHit || len(event.AppliedMembers) != 0 {
		t.Fatalf("ledger event = %#v, want a hit applying nothing", event)
	}
	got := memberEntries(three.result, contractsv1.ContextFabricStructureNeedSubjectCandidate)
	if len(got) != 1 || got[0].Source != contractsv1.ContextFabricStructureSourceReceipt || got[0].AppliedValue != second.CanonicalID {
		t.Fatalf("candidate disclosure = %#v, want only the fresh receipt entry for %s", got, second.CanonicalID)
	}
	want := []ConfirmedNeedEntry{{Member: contractsv1.ContextFabricStructureNeedSubjectCandidate, AppliedKind: second.Kind, AppliedValue: second.CanonicalID}}
	if three.saved == nil || !reflect.DeepEqual(three.saved.ConfirmedNeeds, want) {
		t.Fatalf("ledger = %#v, want %#v", three.saved, want)
	}
}

// TestConfirmedNeedConsumers_ReverifyDropsTheMember: a remembered candidate
// or handle that no longer verifies is dropped alone, with an event, never
// served blindly -- and the drop leaves the outgoing ledger too.
func TestConfirmedNeedConsumers_ReverifyDropsTheMember(t *testing.T) {
	t.Parallel()
	t.Run("candidate no longer visible", func(t *testing.T) {
		t.Parallel()
		h := newNeedTurnHarness(t, nil)
		_, two, _ := candidateTurnsOneAndTwo(t, h)
		h.refuseCandidates = true
		three := h.turn(continuingNeedTurn(needTurnRequest("request_need_candidate_refused", true), two.result.ResultID), committingNeedResponse())
		event := soleLedgerEvent(t, three)
		wantDrops := []ConfirmedNeedMemberDrop{{Member: contractsv1.ContextFabricStructureNeedSubjectCandidate, Reason: ConfirmedNeedMemberDropReverifyNotConfirmed}}
		if event.Outcome != ConfirmedNeedLedgerHit || len(event.AppliedMembers) != 0 || !reflect.DeepEqual(event.Dropped, wantDrops) {
			t.Fatalf("ledger event = %#v, want a hit dropping subject_candidate:reverify_not_confirmed", event)
		}
		if got := memberEntries(three.result, contractsv1.ContextFabricStructureNeedSubjectCandidate); len(got) != 0 {
			t.Fatalf("candidate disclosure = %#v, want none for a dropped member", got)
		}
		if three.saved != nil && len(three.saved.ConfirmedNeeds) != 0 {
			t.Fatalf("ledger = %#v, want the dropped member gone", three.saved.ConfirmedNeeds)
		}
	})
	t.Run("handle no longer resolves", func(t *testing.T) {
		t.Parallel()
		h := newNeedTurnHarness(t, nil)
		one := h.turn(needTurnRequest("request_need_handle_refused_one", true), handleOfferingNeedResponse())
		if one.result.StructureNeeds == nil || len(one.result.StructureNeeds.HandleOptions) != 1 {
			t.Fatalf("fixture defect: turn one must raise the handle need")
		}
		twoRequest := needTurnRequest("request_need_handle_refused_two", true)
		twoRequest.PriorHandleReceipts = []BoundSubjectReceipt{{ResultID: one.result.ResultID, ReceiptID: one.result.StructureNeeds.HandleOptions[0].ReceiptID}}
		two := h.turn(twoRequest, committingNeedResponse())
		h.refuseHandles = true
		three := h.turn(continuingNeedTurn(needTurnRequest("request_need_handle_refused_three", true), two.result.ResultID), committingNeedResponse())
		wantDrops := []ConfirmedNeedMemberDrop{{Member: contractsv1.ContextFabricStructureNeedSubjectHandle, Reason: ConfirmedNeedMemberDropReverifyNotConfirmed}}
		if event := soleLedgerEvent(t, three); !reflect.DeepEqual(event.Dropped, wantDrops) || len(event.AppliedMembers) != 0 {
			t.Fatalf("ledger event = %#v, want subject_handle:reverify_not_confirmed", event)
		}
		if got := memberEntries(three.result, contractsv1.ContextFabricStructureNeedSubjectHandle); len(got) != 0 {
			t.Fatalf("handle disclosure = %#v, want none", got)
		}
	})
}

// ledgerOnlyParent stores a parent turn whose ledger holds entries, for the
// reverify-availability and comparator cases that need a precise ledger.
func ledgerOnlyParent(t *testing.T, store *staticResultStore, parentID string, confirmed []contractsv1.ContextFabricConfirmedStructureEntry, ledger []ConfirmedNeedEntry) {
	t.Helper()
	parentRequest := needTurnRequest("request_need_parent", true)
	parent := validInvestigationResult()
	parent.ResultID = parentID
	parent.Question = parentRequest.Question
	parent.ConfirmedStructure = confirmed
	state := framelessCarrierState(InvestigationResult{Question: parent.Question, AnswerPlan: &contractsv1.ContextFabricAnswerPlan{Family: QuestionFamilySubjectInvestigation}})
	state.RequestIdentity = SemanticRequestIdentityOf(parentRequest, "")
	state.ConfirmedNeeds = ledger
	if _, err := EncodeSemanticState(state); err != nil {
		t.Fatalf("fixture defect: parent snapshot does not encode: %v", err)
	}
	store.results[parentID] = parent
	store.states[parentID] = state
}

// TestConfirmedNeedConsumers_UnverifiableMembersAreDroppedWithTheirReason
// sweeps the reverify_unavailable arm across the three verified members,
// through Investigate.
func TestConfirmedNeedConsumers_UnverifiableMembersAreDroppedWithTheirReason(t *testing.T) {
	t.Parallel()
	ledger := []ConfirmedNeedEntry{
		{Member: contractsv1.ContextFabricStructureNeedSubjectAnchor, AppliedKind: SubjectTeam, AppliedValue: "team_need", MatchedTermHash: "hash_need"},
		{Member: contractsv1.ContextFabricStructureNeedSubjectHandle, AppliedKind: SubjectPullRequest, AppliedValue: "532", PatternID: "pull_request_number"},
		{Member: contractsv1.ContextFabricStructureNeedSubjectCandidate, AppliedKind: SubjectRepository, AppliedValue: "repository:need-r2"},
	}
	t.Run("no verifier wired", func(t *testing.T) {
		t.Parallel()
		store := &staticResultStore{results: map[string]InvestigationResult{}, states: map[string]*PersistedSemanticState{}}
		ledgerOnlyParent(t, store, "result_need_unverifiable", nil, ledger)
		h := newNeedTurnHarness(t, store, withoutNeedVerifiers())
		three := h.turn(continuingNeedTurn(needTurnRequest("request_need_unverifiable", true), "result_need_unverifiable"), committingNeedResponse())
		want := []ConfirmedNeedMemberDrop{
			{Member: contractsv1.ContextFabricStructureNeedSubjectAnchor, Reason: ConfirmedNeedMemberDropReverifyUnavailable},
			{Member: contractsv1.ContextFabricStructureNeedSubjectHandle, Reason: ConfirmedNeedMemberDropReverifyUnavailable},
			{Member: contractsv1.ContextFabricStructureNeedSubjectCandidate, Reason: ConfirmedNeedMemberDropReverifyUnavailable},
		}
		if event := soleLedgerEvent(t, three); !reflect.DeepEqual(event.Dropped, want) || len(event.AppliedMembers) != 0 {
			t.Fatalf("ledger event = %#v, want every verified member dropped as reverify_unavailable", event)
		}
	})
	t.Run("a handle with no pattern cannot be replayed", func(t *testing.T) {
		t.Parallel()
		store := &staticResultStore{results: map[string]InvestigationResult{}, states: map[string]*PersistedSemanticState{}}
		ledgerOnlyParent(t, store, "result_need_no_pattern", nil, []ConfirmedNeedEntry{
			{Member: contractsv1.ContextFabricStructureNeedSubjectHandle, AppliedKind: SubjectPullRequest, AppliedValue: "532"},
		})
		h := newNeedTurnHarness(t, store)
		three := h.turn(continuingNeedTurn(needTurnRequest("request_need_no_pattern", true), "result_need_no_pattern"), committingNeedResponse())
		want := []ConfirmedNeedMemberDrop{{Member: contractsv1.ContextFabricStructureNeedSubjectHandle, Reason: ConfirmedNeedMemberDropReverifyUnavailable}}
		if event := soleLedgerEvent(t, three); !reflect.DeepEqual(event.Dropped, want) {
			t.Fatalf("ledger event = %#v, want subject_handle:reverify_unavailable", event)
		}
		if len(h.handles) != 0 {
			t.Fatalf("HandleVerifier called %#v, want never: there is no pattern to replay", h.handles)
		}
	})
}

// TestConfirmedNeedConsumers_KindCarryComparator: a remembered candidate
// joins the kind-carry comparator exactly as its fresh receipt would -- it
// stands a carried kind down on disagreement, keeps it on agreement, does
// nothing under a changed identity, and never drops a kind that is itself
// remembered.
func TestConfirmedNeedConsumers_KindCarryComparator(t *testing.T) {
	t.Parallel()
	carriedTeam := []contractsv1.ContextFabricConfirmedStructureEntry{confirmedKindEntry(SubjectTeam, "result_need_turn_zero", "kindr_need_zero_01")}
	for _, tc := range []struct {
		name          string
		ledger        []ConfirmedNeedEntry
		changeScope   bool
		wantKind      *contractsv1.ContextFabricSubjectKind
		wantCarry     KindCarryOutcome
		wantDisclosed bool
	}{
		{
			name:          "a remembered candidate of another kind stands the carried kind down",
			ledger:        []ConfirmedNeedEntry{{Member: contractsv1.ContextFabricStructureNeedSubjectCandidate, AppliedKind: SubjectRepository, AppliedValue: "repository:need-r2"}},
			wantCarry:     KindCarryDroppedRedeemedKindDiffers,
			wantDisclosed: true,
		},
		{
			name:          "a remembered candidate of the same kind keeps it",
			ledger:        []ConfirmedNeedEntry{{Member: contractsv1.ContextFabricStructureNeedSubjectCandidate, AppliedKind: SubjectTeam, AppliedValue: "team_need"}},
			wantKind:      ptrSubjectKind(SubjectTeam),
			wantCarry:     KindCarryHit,
			wantDisclosed: true,
		},
		{
			name:        "a changed identity drops the ledger, so nothing compares",
			ledger:      []ConfirmedNeedEntry{{Member: contractsv1.ContextFabricStructureNeedSubjectCandidate, AppliedKind: SubjectRepository, AppliedValue: "repository:need-r2"}},
			changeScope: true,
			wantKind:    ptrSubjectKind(SubjectTeam),
			wantCarry:   KindCarryHit,
		},
		{
			name: "a remembered kind is never dropped by a remembered candidate",
			ledger: []ConfirmedNeedEntry{
				{Member: contractsv1.ContextFabricStructureNeedExpectedKind, AppliedValue: string(SubjectTeam)},
				{Member: contractsv1.ContextFabricStructureNeedSubjectCandidate, AppliedKind: SubjectRepository, AppliedValue: "repository:need-r2"},
			},
			wantKind:      ptrSubjectKind(SubjectTeam),
			wantCarry:     KindCarryHit,
			wantDisclosed: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := &staticResultStore{results: map[string]InvestigationResult{}, states: map[string]*PersistedSemanticState{}}
			ledgerOnlyParent(t, store, "result_need_comparator", carriedTeam, tc.ledger)
			h := newNeedTurnHarness(t, store)
			request := continuingNeedTurn(needTurnRequest("request_need_comparator", true), "result_need_comparator")
			if tc.changeScope {
				request.RequestedScope.RepositorySlugs = []string{"full-chaos/dev-health-acr"}
			}
			three := h.turn(request, committingNeedResponse())
			if len(three.kindCarries) != 1 || three.kindCarries[0].outcome != tc.wantCarry {
				t.Fatalf("kind carries = %#v, want exactly one %s", three.kindCarries, tc.wantCarry)
			}
			call := lastPortCall(t, three)
			switch {
			case tc.wantKind == nil && call.kind != nil:
				t.Fatalf("ResolveSubjects confirmed kind = %v, want none", call.kind.Kind)
			case tc.wantKind != nil && (call.kind == nil || call.kind.Kind != *tc.wantKind):
				t.Fatalf("ResolveSubjects confirmed kind = %v, want %s", call.kind, *tc.wantKind)
			}
			if got := len(memberEntries(three.result, contractsv1.ContextFabricStructureNeedSubjectCandidate)) == 1; got != tc.wantDisclosed {
				t.Fatalf("candidate disclosed = %v, want %v", got, tc.wantDisclosed)
			}
		})
	}
}

func ptrSubjectKind(kind contractsv1.ContextFabricSubjectKind) *contractsv1.ContextFabricSubjectKind {
	return &kind
}

// windowTurnOne drives a turn with no window of its own, which the
// class-default gate answers with a window clarification.
func windowTurnOne(t *testing.T, h *needTurnHarness, requestID string) (needTurnOutcome, contractsv1.ContextFabricWindowOption) {
	t.Helper()
	one := h.turn(needTurnRequest(requestID, false), committingNeedResponse())
	if one.result.WindowClarification == nil || len(one.result.WindowClarification.Options) == 0 {
		t.Fatalf("fixture defect: turn one must raise the window need; status=%s", one.result.Status)
	}
	for _, option := range one.result.WindowClarification.Options {
		if option.RelativeID == RelativeWindowTrailing90D {
			return one, option
		}
	}
	return one, one.result.WindowClarification.Options[0]
}

func sameWindowBounds(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}

// TestConfirmedNeedConsumers_WindowThreeTurns covers the one population where
// the carrier misses and the ledger applies: turn two redeems the window
// together with a structure receipt that vetoes, so its terminal echoes the
// window as applied and persists no effective window of its own.
func TestConfirmedNeedConsumers_WindowThreeTurns(t *testing.T) {
	t.Parallel()
	h := newNeedTurnHarness(t, nil)
	one, option := windowTurnOne(t, h, "request_need_window_one")
	twoRequest := needTurnRequest("request_need_window_two", false)
	twoRequest.PriorWindowReceipts = []BoundSubjectReceipt{{ResultID: one.result.ResultID, ReceiptID: option.ReceiptID}}
	twoRequest.PriorKindReceipts = []BoundSubjectReceipt{{ResultID: one.result.ResultID, ReceiptID: "kindr_needmissing0001"}}
	two := h.turn(twoRequest, committingNeedResponse())
	if two.result.EffectiveEvidenceWindow != nil {
		t.Fatalf("fixture defect: the structure-veto parent must persist no effective window; got %#v", two.result.EffectiveEvidenceWindow)
	}
	if got := memberEntries(two.result, contractsv1.ContextFabricStructureNeedWindow); len(got) != 1 || got[0].Disposition != contractsv1.ContextFabricStructureDispositionApplied {
		t.Fatalf("fixture defect: turn two must echo the redeemed window as applied; got %#v", got)
	}
	wantLedger := []ConfirmedNeedEntry{{Member: contractsv1.ContextFabricStructureNeedWindow, AppliedValue: string(option.RelativeID), WindowStart: option.Start, WindowEnd: option.End}}
	if two.saved == nil || len(two.saved.ConfirmedNeeds) != 1 || two.saved.ConfirmedNeeds[0].AppliedValue != wantLedger[0].AppliedValue ||
		!sameWindowBounds(two.saved.ConfirmedNeeds[0].WindowStart, option.Start) || !sameWindowBounds(two.saved.ConfirmedNeeds[0].WindowEnd, option.End) {
		t.Fatalf("turn two ledger = %#v, want the redeemed window with its frozen bounds", two.saved)
	}

	t.Run("same identity: the carrier misses and the remembered window applies", func(t *testing.T) {
		three := h.turn(continuingNeedTurn(needTurnRequest("request_need_window_three", false), two.result.ResultID), committingNeedResponse())
		if len(three.windowCarry) != 0 {
			t.Fatalf("window carries = %#v, want none attempted: the remembered window is this turn's request-side window, as the receipt's would be", three.windowCarry)
		}
		if !reflect.DeepEqual(three.windows, []confirmedNeedLedgerWindowRecord{{ConfirmedNeedLedgerWindowApplied, two.result.ResultID, string(option.RelativeID)}}) {
			t.Fatalf("ledger window events = %#v, want exactly one applied", three.windows)
		}
		window := three.result.EffectiveEvidenceWindow
		if three.result.Status == InvestigationClarificationRequired || window == nil || window.Provenance != WindowClarificationConfirmed || window.RelativeID != option.RelativeID ||
			!sameWindowBounds(window.Start, option.Start) || !sameWindowBounds(window.End, option.End) {
			t.Fatalf("turn three status=%s window=%#v, want served under the remembered clarification_confirmed window with frozen bounds", three.result.Status, window)
		}
		if got := memberEntries(three.result, contractsv1.ContextFabricStructureNeedWindow); !reflect.DeepEqual(got, []ConfirmedStructureEntry{carriedNeedEntry(contractsv1.ContextFabricStructureNeedWindow, string(option.RelativeID), two.result.ResultID)}) {
			t.Fatalf("turn three window disclosure = %#v, want exactly the carried entry naming turn two", got)
		}
		if len(three.windowCanons) == 0 || three.windowCanons[len(three.windowCanons)-1] != WindowCanonicalizationCarried {
			t.Fatalf("window canonicalization outcomes = %#v, want the served outcome carried", three.windowCanons)
		}
		if three.saved == nil || len(three.saved.ConfirmedNeeds) != 1 || three.saved.ConfirmedNeeds[0].Member != contractsv1.ContextFabricStructureNeedWindow {
			t.Fatalf("turn three ledger = %#v, want the window carried forward", three.saved)
		}
	})

	t.Run("changed identity: the ledger is dropped and the window need is re-raised", func(t *testing.T) {
		request := continuingNeedTurn(needTurnRequest("request_need_window_three_b", false), two.result.ResultID)
		request.RequestedScope.RepositorySlugs = []string{"full-chaos/dev-health-acr"}
		three := h.turn(request, committingNeedResponse())
		if len(three.windows) != 0 {
			t.Fatalf("ledger window events = %#v, want none: no window was admitted", three.windows)
		}
		if three.result.Status != InvestigationClarificationRequired || three.result.WindowClarification == nil {
			t.Fatalf("turn three status=%s, want the window need re-raised", three.result.Status)
		}
		if got := memberEntries(three.result, contractsv1.ContextFabricStructureNeedWindow); len(got) != 0 {
			t.Fatalf("window disclosure = %#v, want none", got)
		}
	})

	t.Run("a window stated this turn: not applicable", func(t *testing.T) {
		three := h.turn(continuingNeedTurn(needTurnRequest("request_need_window_three_c", true), two.result.ResultID), committingNeedResponse())
		if !reflect.DeepEqual(three.windows, []confirmedNeedLedgerWindowRecord{{ConfirmedNeedLedgerWindowNotApplicable, two.result.ResultID, string(option.RelativeID)}}) {
			t.Fatalf("ledger window events = %#v, want exactly one not_applicable", three.windows)
		}
		if window := three.result.EffectiveEvidenceWindow; window == nil || window.Provenance != WindowQuestionStated {
			t.Fatalf("effective window = %#v, want the caller's own stated window", window)
		}
		if got := memberEntries(three.result, contractsv1.ContextFabricStructureNeedWindow); len(got) != 0 {
			t.Fatalf("window disclosure = %#v, want none from the ledger", got)
		}
		if three.saved == nil {
			t.Fatalf("turn three saved no semantic state")
		}
		for _, entry := range three.saved.ConfirmedNeeds {
			if entry.Member == contractsv1.ContextFabricStructureNeedWindow {
				t.Fatalf("turn three ledger = %#v, want the remembered window retired by the window stated this turn", three.saved.ConfirmedNeeds)
			}
		}
	})
}

// TestConfirmedNeedConsumers_RememberedWindowStandsTheCarryDown: when the
// parent persisted its confirmed window AND remembers it, the remembered
// window is this turn's request-side window, exactly as a re-echoed receipt
// would be, so the same-conversation carry is never attempted -- one window
// value, one entry, keyed on its frozen bounds. Control: under a changed
// identity the ledger is refused and the carry supplies the same window, also
// keyed on its effective window's frozen bounds.
func TestConfirmedNeedConsumers_RememberedWindowStandsTheCarryDown(t *testing.T) {
	t.Parallel()
	h := newNeedTurnHarness(t, nil)
	one, option := windowTurnOne(t, h, "request_need_precedence_one")
	twoRequest := needTurnRequest("request_need_precedence_two", false)
	twoRequest.PriorWindowReceipts = []BoundSubjectReceipt{{ResultID: one.result.ResultID, ReceiptID: option.ReceiptID}}
	two := h.turn(twoRequest, committingNeedResponse())
	if two.result.EffectiveEvidenceWindow == nil || two.saved == nil || len(two.saved.ConfirmedNeeds) != 1 {
		t.Fatalf("fixture defect: turn two must persist its confirmed window and ledger; window=%#v saved=%#v", two.result.EffectiveEvidenceWindow, two.saved)
	}
	frozenKey := composeTimeAxisKey(TimeAxisKeyFor(TimeContext{Axis: TemporalCurrent}), windowKeyComponent(*two.result.EffectiveEvidenceWindow, windowKeyFrozen))
	if two.saveKey != frozenKey {
		t.Fatalf("fixture defect: the receipt turn keyed %q, want %q", two.saveKey, frozenKey)
	}

	three := h.turn(continuingNeedTurn(needTurnRequest("request_need_precedence_three", false), two.result.ResultID), committingNeedResponse())
	if len(three.windowCarry) != 0 {
		t.Fatalf("window carries = %#v, want none attempted", three.windowCarry)
	}
	if !reflect.DeepEqual(three.windows, []confirmedNeedLedgerWindowRecord{{ConfirmedNeedLedgerWindowApplied, two.result.ResultID, string(option.RelativeID)}}) {
		t.Fatalf("ledger window events = %#v, want exactly one applied", three.windows)
	}
	if got := memberEntries(three.result, contractsv1.ContextFabricStructureNeedWindow); !reflect.DeepEqual(got, []ConfirmedStructureEntry{carriedNeedEntry(contractsv1.ContextFabricStructureNeedWindow, string(option.RelativeID), two.result.ResultID)}) {
		t.Fatalf("window disclosure = %#v, want exactly the ledger's carried entry", got)
	}
	if window := three.result.EffectiveEvidenceWindow; window == nil || window.RelativeID != option.RelativeID || !sameWindowBounds(window.Start, option.Start) || !sameWindowBounds(window.End, option.End) {
		t.Fatalf("effective window = %#v, want the remembered confirmed window", window)
	}
	if three.saveKey != frozenKey {
		t.Fatalf("ledger turn keyed %q, want the receipt turn's %q", three.saveKey, frozenKey)
	}

	changedRequest := continuingNeedTurn(needTurnRequest("request_need_precedence_three_b", false), two.result.ResultID)
	changedRequest.RequestedScope.RepositorySlugs = []string{"full-chaos/dev-health-acr"}
	changed := h.turn(changedRequest, committingNeedResponse())
	if len(changed.windows) != 0 || len(changed.windowCarry) != 1 || changed.windowCarry[0].outcome != WindowCarryHit {
		t.Fatalf("control: ledger windows=%#v carries=%#v, want no ledger decision and one carry hit", changed.windows, changed.windowCarry)
	}
	if entries := memberEntries(changed.result, contractsv1.ContextFabricStructureNeedWindow); len(entries) != 1 || entries[0].Source != contractsv1.ContextFabricStructureSourceCarried {
		t.Fatalf("control: window disclosure = %#v, want the carry's one entry", entries)
	}
	if changed.saveKey != frozenKey {
		t.Fatalf("control: carried turn keyed %q, want its effective window's %q", changed.saveKey, frozenKey)
	}

	// The subjectless terminal keys the same way: a resolution committing
	// nothing, on the ledger turn and on the carried turn.
	nothing := needTurnResponse{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}}}
	terminalLedger := h.turn(continuingNeedTurn(needTurnRequest("request_need_precedence_terminal", false), two.result.ResultID), nothing)
	terminalChangedRequest := continuingNeedTurn(needTurnRequest("request_need_precedence_terminal_b", false), two.result.ResultID)
	terminalChangedRequest.RequestedScope.RepositorySlugs = []string{"full-chaos/dev-health-acr"}
	terminalChanged := h.turn(terminalChangedRequest, nothing)
	for name, turn := range map[string]needTurnOutcome{"ledger": terminalLedger, "carried": terminalChanged} {
		if len(turn.calls) != 1 || len(turn.result.SubjectResolution.Committed) != 0 || turn.result.EffectiveEvidenceWindow == nil {
			t.Fatalf("fixture defect: %s terminal must resolve once, commit nothing and keep the window; calls=%d window=%#v", name, len(turn.calls), turn.result.EffectiveEvidenceWindow)
		}
		if turn.saveKey != frozenKey {
			t.Fatalf("%s subjectless terminal keyed %q, want %q", name, turn.saveKey, frozenKey)
		}
	}
}

// TestConfirmedNeedConsumers_AxisConflictVetoCapturesNoWindow: a window
// receipt redeemed on a turn the axis-conflict veto refuses is echoed as
// nothing and claimed by nothing, so it never enters the ledger that turn
// saves.
func TestConfirmedNeedConsumers_AxisConflictVetoCapturesNoWindow(t *testing.T) {
	t.Parallel()
	h := newNeedTurnHarness(t, nil)
	one, option := windowTurnOne(t, h, "request_need_axis_one")
	h.historical = true
	request := needTurnRequest("request_need_axis_two", false)
	request.PriorWindowReceipts = []BoundSubjectReceipt{{ResultID: one.result.ResultID, ReceiptID: option.ReceiptID}}
	// A parent reference takes this turn outside the window-only continuation
	// shape, whose carried axis would otherwise override the moved one.
	request.ParentResultID = one.result.ResultID
	two := h.turn(request, committingNeedResponse())
	if two.result.Status != InvestigationNoMatch || len(memberEntries(two.result, contractsv1.ContextFabricStructureNeedWindow)) != 0 {
		t.Fatalf("fixture defect: turn two must be the axis-conflict veto echoing no window; status=%s structure=%#v", two.result.Status, two.result.ConfirmedStructure)
	}
	if two.saved != nil {
		for _, entry := range two.saved.ConfirmedNeeds {
			if entry.Member == contractsv1.ContextFabricStructureNeedWindow {
				t.Fatalf("saved ledger = %#v, want no window: the veto refused it", two.saved.ConfirmedNeeds)
			}
		}
	}
}

// TestAxisConflictConfirmedNeeds_CarriesOnlyTheRememberedLedger: a turn whose
// window veto echoes none of this turn's confirmations persists only the
// remembered ledger.
func TestAxisConflictConfirmedNeeds_CarriesOnlyTheRememberedLedger(t *testing.T) {
	t.Parallel()
	remembered := []confirmedStructureMember{{Member: contractsv1.ContextFabricStructureNeedSubjectCandidate, AppliedKind: SubjectRepository, AppliedValue: "repository:need-r2"}}
	got := axisConflictConfirmedNeeds(remembered, validInvestigationRequest())
	want := []ConfirmedNeedEntry{{Member: contractsv1.ContextFabricStructureNeedSubjectCandidate, AppliedKind: SubjectRepository, AppliedValue: "repository:need-r2"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("axisConflictConfirmedNeeds() = %#v, want %#v", got, want)
	}
	handle := append(remembered, confirmedStructureMember{Member: contractsv1.ContextFabricStructureNeedSubjectHandle, AppliedKind: SubjectPullRequest, AppliedValue: "532", PatternID: "p"})
	explicit := validInvestigationRequest()
	explicit.SubjectHandles = []contractsv1.ContextFabricRequestedHandle{{Kind: SubjectPullRequest, PatternID: "p", Value: "777"}}
	if got := axisConflictConfirmedNeeds(handle, explicit); !reflect.DeepEqual(got, want) {
		t.Fatalf("axisConflictConfirmedNeeds() with an explicit handle = %#v, want %#v: the explicit value retires the remembered one", got, want)
	}
}

func TestDecideLedgerWindow(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	relative := confirmedStructureMember{Member: contractsv1.ContextFabricStructureNeedWindow, AppliedValue: string(RelativeWindowTrailing90D), WindowStart: &start, WindowEnd: &end}
	absolute := confirmedStructureMember{Member: contractsv1.ContextFabricStructureNeedWindow, AppliedValue: windowAbsoluteAppliedValuePrefix + "1:2", WindowStart: &start, WindowEnd: &end}
	ledger := func(entries ...confirmedStructureMember) confirmedNeedLedgerResult {
		return confirmedNeedLedgerResult{Outcome: ConfirmedNeedLedgerHit, Entries: entries, SourceResultID: "result_need_parent"}
	}
	silent := validInvestigationRequest()
	silentCanon := requestWindowCanonicalization{}
	stated := validInvestigationRequest()
	stated.TimeContext.EvidenceWindow = validConfirmedWindow()
	historical := validInvestigationRequest()
	historical.TimeContext = TimeContext{Axis: TemporalValidTime}
	statedWindow := &contractsv1.ContextFabricEffectiveEvidenceWindow{RelativeID: RelativeWindowTrailing30D, Provenance: WindowQuestionStated}

	if got := decideLedgerWindow(ledger(), silent, silentCanon); got.Present {
		t.Fatalf("no window entry: got %#v, want not present", got)
	}
	if got := decideLedgerWindow(ledger(confirmedStructureMember{Member: contractsv1.ContextFabricStructureNeedWindow}), silent, silentCanon); got.Present {
		t.Fatalf("empty-valued entry: got %#v, want not present", got)
	}
	for name, cell := range map[string]struct {
		request InvestigationRequest
		canon   requestWindowCanonicalization
	}{
		"explicit window stated this turn":   {stated, requestWindowCanonicalization{Effective: statedWindow, KeyComponent: "rel:trailing_30d"}},
		"explicit window, canon not yet set": {stated, silentCanon},
		"window receipt confirmed this turn": {silent, requestWindowCanonicalization{ConfirmedMember: &confirmedStructureMember{Member: contractsv1.ContextFabricStructureNeedWindow, AppliedValue: "trailing_30d"}}},
		"request-side window resolved":       {silent, requestWindowCanonicalization{Effective: statedWindow}},
		"request-side veto":                  {silent, requestWindowCanonicalization{Veto: windowVetoConfirmationUnresolved}},
		"request not on the current axis":    {historical, silentCanon},
	} {
		got := decideLedgerWindow(ledger(relative), cell.request, cell.canon)
		if !got.Present || got.Decision != ConfirmedNeedLedgerWindowNotApplicable || got.Applied() || composeLedgerWindowEntry(got) != nil {
			t.Fatalf("%s: got %#v, want present, not_applicable, nothing applied or disclosed", name, got)
		}
		if after := cell.canon.withRememberedWindow(got); !reflect.DeepEqual(after, cell.canon) {
			t.Fatalf("%s: withRememberedWindow changed the canonicalization: %#v", name, after)
		}
	}
	applied := decideLedgerWindow(ledger(relative), silent, silentCanon)
	if !applied.Applied() || applied.Window.RelativeID != RelativeWindowTrailing90D || applied.Window.Provenance != WindowClarificationConfirmed ||
		!applied.Window.Start.Equal(start) || !applied.Window.End.Equal(end) || applied.Window.Start == relative.WindowStart {
		t.Fatalf("applied: got %#v, want the remembered window rebuilt with copied bounds", applied)
	}
	if entry := composeLedgerWindowEntry(applied); entry == nil || *entry != carriedNeedEntry(contractsv1.ContextFabricStructureNeedWindow, string(RelativeWindowTrailing90D), "result_need_parent") {
		t.Fatalf("applied disclosure = %#v", entry)
	}
	canon := silentCanon.withRememberedWindow(applied)
	if canon.Effective != applied.Window || canon.KeyComponent != windowKeyComponent(*applied.Window, windowKeyFrozen) || canon.KeyEncoding != windowKeyFrozen ||
		canon.KeyComponent == windowKeyComponent(*applied.Window, windowKeyRederivable) || canon.ConfirmedMember != nil {
		t.Fatalf("withRememberedWindow = %#v, want the window keyed on its frozen bounds, with no confirmed receipt member", canon)
	}
	abs := decideLedgerWindow(ledger(absolute), silent, silentCanon)
	if !abs.Applied() || abs.Window.RelativeID != "" || observableLedgerWindowValue(abs.AppliedValue) != confirmedNeedLedgerWindowAbsolute {
		t.Fatalf("absolute: got %#v, want no relative id and the absolute token", abs)
	}
}

func TestKindCarryComparators(t *testing.T) {
	t.Parallel()
	thisTurn := []confirmedStructureMember{{Member: contractsv1.ContextFabricStructureNeedSubjectHandle, AppliedKind: SubjectPullRequest, AppliedValue: "1"}}
	candidate := confirmedStructureMember{Member: contractsv1.ContextFabricStructureNeedSubjectCandidate, AppliedKind: SubjectRepository, AppliedValue: "r"}
	handle := confirmedStructureMember{Member: contractsv1.ContextFabricStructureNeedSubjectHandle, AppliedKind: SubjectWorkItem, AppliedValue: "h"}
	anchor := confirmedStructureMember{Member: contractsv1.ContextFabricStructureNeedSubjectAnchor, AppliedKind: SubjectTeam, AppliedValue: "a"}
	kind := confirmedStructureMember{Member: contractsv1.ContextFabricStructureNeedExpectedKind, AppliedValue: string(SubjectTeam)}

	got := kindCarryComparators(thisTurn, map[contractsv1.ContextFabricStructureNeedKind]confirmedStructureMember{
		contractsv1.ContextFabricStructureNeedSubjectCandidate: candidate, contractsv1.ContextFabricStructureNeedSubjectHandle: handle, contractsv1.ContextFabricStructureNeedSubjectAnchor: anchor,
	})
	if want := []confirmedStructureMember{thisTurn[0], candidate, handle}; !reflect.DeepEqual(got, want) {
		t.Fatalf("comparators = %#v, want this turn's receipts then the remembered candidate and handle (never the anchor)", got)
	}
	got = kindCarryComparators(thisTurn, map[contractsv1.ContextFabricStructureNeedKind]confirmedStructureMember{
		contractsv1.ContextFabricStructureNeedExpectedKind: kind, contractsv1.ContextFabricStructureNeedSubjectCandidate: candidate,
	})
	if !reflect.DeepEqual(got, thisTurn) {
		t.Fatalf("comparators with a remembered kind = %#v, want this turn's receipts only", got)
	}
}

// TestAppliedNeedLedgerEntries_StatedThisTurnSupersedes sweeps the
// supersession rule over every applying member and both statement routes.
func TestAppliedNeedLedgerEntries_StatedThisTurnSupersedes(t *testing.T) {
	t.Parallel()
	remembered := []confirmedStructureMember{
		{Member: contractsv1.ContextFabricStructureNeedExpectedKind, AppliedValue: string(SubjectTeam)},
		{Member: contractsv1.ContextFabricStructureNeedSubjectAnchor, AppliedKind: SubjectTeam, AppliedValue: "a"},
		{Member: contractsv1.ContextFabricStructureNeedSubjectCandidate, AppliedKind: SubjectRepository, AppliedValue: "c"},
		{Member: contractsv1.ContextFabricStructureNeedSubjectHandle, AppliedKind: SubjectPullRequest, AppliedValue: "h", PatternID: "p"},
	}
	all := appliedNeedLedgerEntries(remembered, nil, validInvestigationRequest())
	if len(all) != 4 {
		t.Fatalf("applied = %#v, want all four members with nothing stated this turn", all)
	}
	for _, member := range []contractsv1.ContextFabricStructureNeedKind{
		contractsv1.ContextFabricStructureNeedExpectedKind, contractsv1.ContextFabricStructureNeedSubjectAnchor,
		contractsv1.ContextFabricStructureNeedSubjectCandidate, contractsv1.ContextFabricStructureNeedSubjectHandle,
	} {
		got := appliedNeedLedgerEntries(remembered, []confirmedStructureMember{{Member: member, AppliedValue: "fresh"}}, validInvestigationRequest())
		if _, ok := got[member]; ok || len(got) != 3 {
			t.Fatalf("receipt for %s this turn: applied = %#v, want that member alone excluded", member, got)
		}
	}
	for name, request := range map[string]InvestigationRequest{
		"singular explicit handle": func() InvestigationRequest {
			r := validInvestigationRequest()
			r.SubjectHandles = []contractsv1.ContextFabricRequestedHandle{{Kind: SubjectPullRequest, PatternID: "p", Value: "9"}}
			return r
		}(),
		"plural explicit handles": func() InvestigationRequest {
			r := validInvestigationRequest()
			r.SubjectHandles = []contractsv1.ContextFabricRequestedHandle{{Kind: SubjectPullRequest, PatternID: "p", Value: "9"}, {Kind: SubjectPullRequest, PatternID: "p", Value: "10"}}
			return r
		}(),
	} {
		if got := appliedNeedLedgerEntries(remembered, nil, request); len(got) != 3 {
			t.Fatalf("%s: applied = %#v, want subject_handle alone excluded", name, got)
		} else if _, ok := got[contractsv1.ContextFabricStructureNeedSubjectHandle]; ok {
			t.Fatalf("%s: subject_handle applied", name)
		}
	}
	kinds := validInvestigationRequest()
	kinds.ExpectedKinds = []contractsv1.ContextFabricSubjectKind{SubjectRepository}
	if got := appliedNeedLedgerEntries(remembered, nil, kinds); len(got) != 3 {
		t.Fatalf("explicit kind: applied = %#v, want expected_kind alone excluded", got)
	} else if _, ok := got[contractsv1.ContextFabricStructureNeedExpectedKind]; ok {
		t.Fatalf("explicit kind: expected_kind applied")
	}
}

// TestSemanticStateCapture_WithoutSupersededNeeds: the refused members leave
// the capture's ledger, the rest stays, and the snapshot still encodes.
func TestSemanticStateCapture_WithoutSupersededNeeds(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	capture := captureSemanticState(SemanticStateInput{
		Outcome:         QuestionFamilyOutcome{Family: QuestionFamilyUnclassified, Source: QuestionFamilySourceNone},
		FamilyVersion:   QuestionFamilyTableVersion,
		RequestIdentity: SemanticRequestIdentityOf(validInvestigationRequest(), ""),
		ConfirmedNeeds: []ConfirmedNeedEntry{
			{Member: contractsv1.ContextFabricStructureNeedSubjectHandle, AppliedKind: SubjectPullRequest, AppliedValue: "532", PatternID: "pull_request_number"},
			{Member: contractsv1.ContextFabricStructureNeedWindow, AppliedValue: string(RelativeWindowTrailing90D), WindowStart: &start, WindowEnd: &end},
			{Member: contractsv1.ContextFabricStructureNeedSubjectCandidate, AppliedKind: SubjectRepository, AppliedValue: "repository:need-r2"},
		},
	})
	if capture.Write.State == nil {
		t.Fatalf("fixture defect: capture did not validate: %#v", capture)
	}
	stripped := capture.withoutSupersededNeeds([]contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedWindow, contractsv1.ContextFabricStructureNeedSubjectCandidate})
	if stripped.Write.State == nil || len(stripped.Write.State.ConfirmedNeeds) != 1 || stripped.Write.State.ConfirmedNeeds[0].Member != contractsv1.ContextFabricStructureNeedSubjectHandle || stripped.EncodedBytes >= capture.EncodedBytes {
		t.Fatalf("stripped = %#v, want only subject_handle left and a smaller encoding", stripped)
	}
	if len(capture.Write.State.ConfirmedNeeds) != 3 {
		t.Fatalf("the original capture was mutated: %#v", capture.Write.State.ConfirmedNeeds)
	}
	if got := capture.withoutSupersededNeeds([]contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedExpectedKind}); !reflect.DeepEqual(got, capture) {
		t.Fatalf("a refused member the ledger does not hold changed the capture")
	}
	absent := absentSemanticState(SemanticStateAbsenceSnapshotInvalid)
	if got := absent.withoutSupersededNeeds([]contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedWindow}); !reflect.DeepEqual(got, absent) {
		t.Fatalf("an absent capture changed: %#v", got)
	}
}

// TestStructureSupersessionVetoResult_PersistsNoRefusedMember drives the ONE
// site every lost claim race reaches: the veto terminal it saves carries no
// member the claim refused.
func TestStructureSupersessionVetoResult_PersistsNoRefusedMember(t *testing.T) {
	t.Parallel()
	store := &staticResultStore{results: map[string]InvestigationResult{}}
	engine := mustReuseTestEngine(t, EngineDependencies{Results: store})
	capture := captureSemanticState(SemanticStateInput{
		Outcome:         QuestionFamilyOutcome{Family: QuestionFamilyUnclassified, Source: QuestionFamilySourceNone},
		FamilyVersion:   QuestionFamilyTableVersion,
		RequestIdentity: SemanticRequestIdentityOf(validInvestigationRequest(), ""),
		ConfirmedNeeds: []ConfirmedNeedEntry{
			{Member: contractsv1.ContextFabricStructureNeedExpectedKind, AppliedValue: string(SubjectTeam)},
			{Member: contractsv1.ContextFabricStructureNeedSubjectCandidate, AppliedKind: SubjectRepository, AppliedValue: "repository:need-r2"},
		},
	})
	confirmed := []confirmedStructureMember{{Member: contractsv1.ContextFabricStructureNeedSubjectCandidate, AppliedKind: SubjectRepository, AppliedValue: "repository:need-r2", PriorResultID: "result_need_offer", ReceiptID: "candr_needoffer0001", OfferSource: contractsv1.ContextFabricStructureOfferEngine}}
	superseded := &ErrStructureOfferSuperseded{Members: []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedSubjectCandidate}}
	if _, err := engine.structureSupersessionVetoResult(context.Background(), reusePrincipal(), validInvestigationRequest(), confirmed, superseded, ResolvedGraphBinding{}, nil, nil, nil, "", capture); err != nil {
		t.Fatalf("structureSupersessionVetoResult() error = %v", err)
	}
	want := []ConfirmedNeedEntry{{Member: contractsv1.ContextFabricStructureNeedExpectedKind, AppliedValue: string(SubjectTeam)}}
	if store.savedSemantic == nil || store.savedSemantic.State == nil || !reflect.DeepEqual(store.savedSemantic.State.ConfirmedNeeds, want) {
		t.Fatalf("saved ledger = %#v, want %#v", store.savedSemantic, want)
	}
}

// TestSemanticState_ConfirmedNeedsNewFieldsRoundTrip: the persisted
// representation of all five members, pattern_id and frozen bounds included,
// reads back unchanged through the codec both stores use.
func TestSemanticState_ConfirmedNeedsNewFieldsRoundTrip(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	state := semanticFixture(t)
	state.ConfirmedNeeds = []ConfirmedNeedEntry{
		{Member: contractsv1.ContextFabricStructureNeedExpectedKind, AppliedValue: string(SubjectTeam)},
		{Member: contractsv1.ContextFabricStructureNeedSubjectAnchor, AppliedKind: SubjectProject, AppliedValue: "p", MatchedTermHash: "h", Basis: ConfirmedNeedBasisEngineCommitted},
		{Member: contractsv1.ContextFabricStructureNeedSubjectHandle, AppliedKind: SubjectPullRequest, AppliedValue: "532", PatternID: "pull_request_number"},
		{Member: contractsv1.ContextFabricStructureNeedWindow, AppliedValue: string(RelativeWindowTrailing90D), WindowStart: &start, WindowEnd: &end},
		{Member: contractsv1.ContextFabricStructureNeedSubjectCandidate, AppliedKind: SubjectRepository, AppliedValue: "repository:need-r2"},
	}
	encoded, err := EncodeSemanticState(state)
	if err != nil {
		t.Fatalf("EncodeSemanticState() error = %v", err)
	}
	decoded, status := DecodeSemanticState(encoded)
	if status != SemanticStateReadAvailable || decoded == nil || len(decoded.ConfirmedNeeds) != 5 {
		t.Fatalf("DecodeSemanticState() = %#v, %s", decoded, status)
	}
	for i, entry := range decoded.ConfirmedNeeds {
		original := state.ConfirmedNeeds[i]
		if entry.Member != original.Member || entry.AppliedKind != original.AppliedKind || entry.AppliedValue != original.AppliedValue ||
			entry.MatchedTermHash != original.MatchedTermHash || entry.PatternID != original.PatternID || entry.Basis != original.Basis ||
			!sameWindowBounds(entry.WindowStart, original.WindowStart) || !sameWindowBounds(entry.WindowEnd, original.WindowEnd) {
			t.Fatalf("entry %d read back as %#v, want %#v", i, entry, original)
		}
	}
}

// TestSemanticState_ConfirmedNeedsNewFieldsValidation is the input-domain
// table for the fields this change adds.
func TestSemanticState_ConfirmedNeedsNewFieldsValidation(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	handle := contractsv1.ContextFabricStructureNeedSubjectHandle
	window := contractsv1.ContextFabricStructureNeedWindow
	candidate := contractsv1.ContextFabricStructureNeedSubjectCandidate
	anchor := contractsv1.ContextFabricStructureNeedSubjectAnchor
	for _, tc := range []struct {
		name   string
		entry  ConfirmedNeedEntry
		accept bool
	}{
		{"handle without pattern_id", ConfirmedNeedEntry{Member: handle, AppliedKind: SubjectPullRequest, AppliedValue: "1"}, true},
		{"handle with pattern_id", ConfirmedNeedEntry{Member: handle, AppliedKind: SubjectPullRequest, AppliedValue: "1", PatternID: "p"}, true},
		{"pattern_id at the term bound", ConfirmedNeedEntry{Member: handle, AppliedValue: "1", PatternID: strings.Repeat("p", SemanticStateMaxTermBytes)}, true},
		{"pattern_id over the term bound", ConfirmedNeedEntry{Member: handle, AppliedValue: "1", PatternID: strings.Repeat("p", SemanticStateMaxTermBytes+1)}, false},
		{"pattern_id on a candidate", ConfirmedNeedEntry{Member: candidate, AppliedValue: "c", PatternID: "p"}, false},
		{"pattern_id on a window", ConfirmedNeedEntry{Member: window, AppliedValue: "trailing_90d", PatternID: "p"}, false},
		{"window relative id, both bounds", ConfirmedNeedEntry{Member: window, AppliedValue: "trailing_90d", WindowStart: &start, WindowEnd: &end}, true},
		{"window relative id, no bounds", ConfirmedNeedEntry{Member: window, AppliedValue: "all_time"}, true},
		{"window equal bounds", ConfirmedNeedEntry{Member: window, AppliedValue: "trailing_90d", WindowStart: &start, WindowEnd: &start}, true},
		{"window start only", ConfirmedNeedEntry{Member: window, AppliedValue: "trailing_90d", WindowStart: &start}, false},
		{"window end only", ConfirmedNeedEntry{Member: window, AppliedValue: "trailing_90d", WindowEnd: &end}, false},
		{"window reversed bounds", ConfirmedNeedEntry{Member: window, AppliedValue: "trailing_90d", WindowStart: &end, WindowEnd: &start}, false},
		{"window absolute with bounds", ConfirmedNeedEntry{Member: window, AppliedValue: "abs:1:2", WindowStart: &start, WindowEnd: &end}, true},
		{"window absolute without bounds", ConfirmedNeedEntry{Member: window, AppliedValue: "abs:1:2"}, false},
		{"window out-of-vocabulary relative id", ConfirmedNeedEntry{Member: window, AppliedValue: "trailing_91d"}, false},
		{"window empty value", ConfirmedNeedEntry{Member: window}, true},
		{"bounds on a candidate", ConfirmedNeedEntry{Member: candidate, AppliedValue: "c", WindowStart: &start, WindowEnd: &end}, false},
		{"bounds on a handle", ConfirmedNeedEntry{Member: handle, AppliedValue: "1", WindowStart: &start, WindowEnd: &end}, false},
		{"anchor without basis", ConfirmedNeedEntry{Member: anchor, AppliedKind: SubjectRepository, AppliedValue: "r"}, true},
		{"anchor, engine_committed basis", ConfirmedNeedEntry{Member: anchor, AppliedKind: SubjectRepository, AppliedValue: "r", Basis: ConfirmedNeedBasisEngineCommitted}, true},
		{"basis out of vocabulary", ConfirmedNeedEntry{Member: anchor, AppliedKind: SubjectRepository, AppliedValue: "r", Basis: ConfirmedNeedBasis("not_a_basis")}, false},
		{"basis on a candidate", ConfirmedNeedEntry{Member: candidate, AppliedValue: "c", Basis: ConfirmedNeedBasisEngineCommitted}, false},
		{"basis on a handle", ConfirmedNeedEntry{Member: handle, AppliedValue: "1", Basis: ConfirmedNeedBasisEngineCommitted}, false},
		{"basis on a window", ConfirmedNeedEntry{Member: window, AppliedValue: "all_time", Basis: ConfirmedNeedBasisEngineCommitted}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			state := semanticFixture(t)
			state.ConfirmedNeeds = []ConfirmedNeedEntry{tc.entry}
			_, err := EncodeSemanticState(state)
			if tc.accept && err != nil {
				t.Errorf("EncodeSemanticState() = %v, want accepted", err)
			}
			if !tc.accept && (err == nil || !strings.Contains(err.Error(), "confirmed_needs[0]")) {
				t.Errorf("EncodeSemanticState() = %v, want a confirmed_needs[0] rejection", err)
			}
		})
	}
}

// TestRecordConfirmedNeedLedger_EmittedLines asserts both production lines
// through the real slog handler, with every value distinct from every other,
// and no raw canonical id or handle value on the line.
func TestRecordConfirmedNeedLedger_EmittedLines(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	telemetry := NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	engine := mustReuseTestEngine(t, EngineDependencies{Results: &staticResultStore{results: map[string]InvestigationResult{}}, Telemetry: telemetry})
	applied := map[contractsv1.ContextFabricStructureNeedKind]confirmedStructureMember{
		contractsv1.ContextFabricStructureNeedExpectedKind:     {Member: contractsv1.ContextFabricStructureNeedExpectedKind, AppliedValue: string(SubjectTeam)},
		contractsv1.ContextFabricStructureNeedSubjectCandidate: {Member: contractsv1.ContextFabricStructureNeedSubjectCandidate, AppliedKind: SubjectRepository, AppliedValue: "repository:raw-candidate-id"},
		contractsv1.ContextFabricStructureNeedSubjectHandle:    {Member: contractsv1.ContextFabricStructureNeedSubjectHandle, AppliedKind: SubjectPullRequest, AppliedValue: "raw-handle-532"},
	}
	ledger := confirmedNeedLedgerResult{
		Outcome: ConfirmedNeedLedgerHit, SourceResultID: "result_need_parent_line",
		Dropped: []ConfirmedNeedMemberDrop{{Member: contractsv1.ContextFabricStructureNeedSubjectAnchor, Reason: ConfirmedNeedMemberDropReverifyNotConfirmed}},
	}
	engine.recordConfirmedNeedLedger(context.Background(), acceptancePrincipal(), ledger, applied, CountPopulationScopeAnchorUnresolved, CaptureSkipReasonNotApplicable, ConfirmedAnchorAgreementNotApplicable, contractsv1.ContextFabricStructureDispositionVetoedConflict, subjectSubstitutionDecision{Outcome: SubjectSubstitutionNotEvaluated, Origin: SubjectSubstitutionOriginNotApplicable})
	engine.recordConfirmedNeedLedgerWindow(context.Background(), acceptancePrincipal(), ledgerWindowApplication{Present: true, Decision: ConfirmedNeedLedgerWindowApplied, AppliedValue: windowAbsoluteAppliedValuePrefix + "1:2", SourceResultID: "result_need_window_line"})
	engine.recordConfirmedNeedLedgerWindow(context.Background(), acceptancePrincipal(), ledgerWindowApplication{})

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("emitted %d lines, want 2 (an absent window application emits nothing): %s", len(lines), buf.String())
	}
	if strings.Contains(lines[0], "raw-candidate-id") || strings.Contains(lines[0], "raw-handle-532") {
		t.Fatalf("a raw applied value reached the line: %s", lines[0])
	}
	var ledgerLine, windowLine map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &ledgerLine); err != nil {
		t.Fatalf("ledger line: %v", err)
	}
	if err := json.Unmarshal([]byte(lines[1]), &windowLine); err != nil {
		t.Fatalf("window line: %v", err)
	}
	wantLedger := map[string]any{
		"level": "INFO", "msg": "context fabric confirmed need ledger",
		"org_id": acceptancePrincipal().OrgID, "outcome": "hit", "source_result_id": "result_need_parent_line",
		"applied_members":       "expected_kind,subject_handle,subject_candidate",
		"applied_expected_kind": "team", "applied_anchor_kind": "", "applied_anchor_value_hash": "",
		"applied_candidate_kind": "repository", "applied_candidate_value_hash": confirmedNeedValueHash("repository:raw-candidate-id"),
		"applied_handle_kind": "pull_request", "applied_handle_value_hash": confirmedNeedValueHash("raw-handle-532"),
		"dropped_members":      "subject_anchor:reverify_not_confirmed",
		"applied_anchor_basis": "", "anchor_agreement": "not_applicable", "anchor_disposition": "vetoed_conflict", "capture_decision": "anchor_unresolved",
		"capture_skip_reason": "not_applicable",
	}
	for key, want := range wantLedger {
		if got, ok := ledgerLine[key]; !ok || got != want {
			t.Errorf("ledger line %s = %#v (present=%v), want %#v", key, got, ok, want)
		}
	}
	if ledgerLine["applied_candidate_value_hash"] == ledgerLine["applied_handle_value_hash"] || len(confirmedNeedValueHash("x")) != 12 {
		t.Fatalf("value hashes do not discriminate: %#v", ledgerLine)
	}
	wantWindow := map[string]any{
		"level": "INFO", "msg": "context fabric confirmed need ledger window",
		"org_id": acceptancePrincipal().OrgID, "decision": "applied", "source_result_id": "result_need_window_line", "applied_window": "absolute",
	}
	for key, want := range wantWindow {
		if got, ok := windowLine[key]; !ok || got != want {
			t.Errorf("window line %s = %#v (present=%v), want %#v", key, got, ok, want)
		}
	}
}

// TestRecordConfirmedNeedLedger_CaptureSkipReasonEmittedLines drives EVERY
// declared CaptureSkipReason value through the real production logger (not
// the event struct) -- a mutation dropping the emitter's own
// capture_skip_reason key from telemetry.go's arg list is caught here, on
// the ACTUAL JSON line, the same class of gap a struct-only assertion
// cannot close. Enumerated from captureSkipReasons(), the closed vocabulary
// itself, not a hand-copied list -- a value added to the type but not to
// that function fails TestCaptureSkipReasonVocabularyIsClosed instead of
// silently sitting outside this pin.
func TestRecordConfirmedNeedLedger_CaptureSkipReasonEmittedLines(t *testing.T) {
	t.Parallel()
	for _, reason := range captureSkipReasons() {
		t.Run(string(reason), func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			telemetry := NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
			engine := mustReuseTestEngine(t, EngineDependencies{Results: &staticResultStore{results: map[string]InvestigationResult{}}, Telemetry: telemetry})
			ledger := confirmedNeedLedgerResult{Outcome: ConfirmedNeedLedgerMissNoReference, SourceResultID: ""}
			engine.recordConfirmedNeedLedger(context.Background(), acceptancePrincipal(), ledger, nil, "", reason, ConfirmedAnchorAgreementNotApplicable, "", subjectSubstitutionDecision{Outcome: SubjectSubstitutionNotEvaluated, Origin: SubjectSubstitutionOriginNotApplicable})
			var rec map[string]any
			if err := json.Unmarshal(bytes.TrimRight(buf.Bytes(), "\n"), &rec); err != nil {
				t.Fatalf("captured line is not JSON: %v -- line: %s", err, buf.String())
			}
			if got, _ := rec["capture_skip_reason"].(string); got != string(reason) {
				t.Errorf("capture_skip_reason = %q, want %q -- the emitted line, not the event struct", got, reason)
			}
		})
	}
}

func TestConfirmedNeedVocabularies(t *testing.T) {
	t.Parallel()
	for _, reason := range confirmedNeedMemberDropReasons() {
		if !ValidConfirmedNeedMemberDropReason(reason) {
			t.Fatalf("%q not valid", reason)
		}
	}
	for _, decision := range confirmedNeedLedgerWindowDecisions() {
		if !ValidConfirmedNeedLedgerWindowDecision(decision) {
			t.Fatalf("%q not valid", decision)
		}
	}
	if ValidConfirmedNeedMemberDropReason("reverify_refused") || ValidConfirmedNeedLedgerWindowDecision("carried") ||
		len(confirmedNeedMemberDropReasons()) != 2 || len(confirmedNeedLedgerWindowDecisions()) != 2 {
		t.Fatal("vocabulary membership is not closed")
	}
	if got := observableConfirmedNeedDrops(nil); got != "none" {
		t.Fatalf("observableConfirmedNeedDrops(nil) = %q, want none", got)
	}
	if got := confirmedNeedValueHash(""); got != "" {
		t.Fatalf("confirmedNeedValueHash(\"\") = %q, want empty", got)
	}
}

// TestCaptureSkipReasonVocabularyIsClosed pins captureSkipReasons() as the
// one place CaptureSkipReason's membership is declared: every value the type
// carries validates, an unassigned string does not, and the count is exact --
// so a reason added to the const block without a matching addition here, or
// dropped from the const block while still listed here, fails this test
// rather than silently drifting the emitted-lines pin above out of sync with
// the type.
func TestCaptureSkipReasonVocabularyIsClosed(t *testing.T) {
	t.Parallel()
	for _, reason := range captureSkipReasons() {
		if !ValidCaptureSkipReason(reason) {
			t.Fatalf("%q not valid", reason)
		}
	}
	if ValidCaptureSkipReason("unassigned_exit") || ValidCaptureSkipReason("") || len(captureSkipReasons()) != 15 {
		t.Fatal("CaptureSkipReason vocabulary membership is not closed")
	}
}

// TestConfirmedNeedConsumers_ExplicitValueRetiresTheRememberedOne: a value the
// caller states explicitly this turn retires the remembered value for that
// member from the ledger this turn saves, so a later turn naming this turn as
// parent never gets the replaced value back. Swept over every member with an
// explicit request field (subject_handle, expected_kind, window). Control per
// member: the same four turns with nothing stated on turn three carry the
// remembered value to turn four.
func TestConfirmedNeedConsumers_ExplicitValueRetiresTheRememberedOne(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name   string
		member contractsv1.ContextFabricStructureNeedKind
		ledger []ConfirmedNeedEntry
		// state makes turn three state the member explicitly.
		state func(*InvestigationRequest)
		// statedWindow is the window flag every turn of the chain uses.
		statedWindow bool
		// remembered reports whether a turn applied the remembered member.
		remembered func(needTurnOutcome) bool
	}{
		{
			name:         "subject_handle",
			member:       contractsv1.ContextFabricStructureNeedSubjectHandle,
			ledger:       []ConfirmedNeedEntry{{Member: contractsv1.ContextFabricStructureNeedSubjectHandle, AppliedKind: SubjectPullRequest, AppliedValue: "532", PatternID: "pull_request_number"}},
			statedWindow: true,
			state: func(r *InvestigationRequest) {
				r.SubjectHandles = []contractsv1.ContextFabricRequestedHandle{{Kind: SubjectPullRequest, PatternID: "pull_request_number", Value: "777"}}
			},
			remembered: func(o needTurnOutcome) bool {
				got := memberEntries(o.result, contractsv1.ContextFabricStructureNeedSubjectHandle)
				return len(got) == 1 && got[0].Source == contractsv1.ContextFabricStructureSourceCarried && got[0].AppliedValue == "532"
			},
		},
		{
			name:         "expected_kind",
			member:       contractsv1.ContextFabricStructureNeedExpectedKind,
			ledger:       []ConfirmedNeedEntry{{Member: contractsv1.ContextFabricStructureNeedExpectedKind, AppliedValue: string(SubjectTeam)}},
			statedWindow: true,
			state: func(r *InvestigationRequest) {
				r.ExpectedKinds = []contractsv1.ContextFabricSubjectKind{SubjectRepository}
			},
			remembered: func(o needTurnOutcome) bool {
				return len(o.ledgers) == 1 && o.ledgers[0].AppliedExpectedKind == SubjectTeam
			},
		},
		{
			name:   "window",
			member: contractsv1.ContextFabricStructureNeedWindow,
			ledger: []ConfirmedNeedEntry{{Member: contractsv1.ContextFabricStructureNeedWindow, AppliedValue: string(RelativeWindowTrailing90D), WindowStart: &start, WindowEnd: &end}},
			state: func(r *InvestigationRequest) {
				r.TimeContext.EvidenceWindow = validConfirmedWindow()
			},
			remembered: func(o needTurnOutcome) bool {
				return len(o.windows) == 1 && o.windows[0].decision == ConfirmedNeedLedgerWindowApplied
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			for _, stated := range []bool{true, false} {
				store := &staticResultStore{results: map[string]InvestigationResult{}, states: map[string]*PersistedSemanticState{}}
				ledgerOnlyParent(t, store, "result_need_retire_parent", nil, tc.ledger)
				h := newNeedTurnHarness(t, store)
				threeRequest := continuingNeedTurn(needTurnRequest("request_need_retire_three", tc.statedWindow), "result_need_retire_parent")
				if stated {
					tc.state(&threeRequest)
				}
				three := h.turn(threeRequest, committingNeedResponse())
				if three.saved == nil {
					t.Fatalf("stated=%v: turn three saved no semantic state", stated)
				}
				inLedger := false
				for _, entry := range three.saved.ConfirmedNeeds {
					inLedger = inLedger || entry.Member == tc.member
				}
				four := h.turn(continuingNeedTurnAgain(needTurnRequest("request_need_retire_four", tc.statedWindow), threeRequest, three.result.ResultID), committingNeedResponse())
				if stated {
					if tc.remembered(three) {
						t.Fatalf("turn three applied the remembered %s while the caller stated one", tc.member)
					}
					if inLedger {
						t.Fatalf("turn three ledger = %#v, want %s retired by the value stated this turn", three.saved.ConfirmedNeeds, tc.member)
					}
					if tc.remembered(four) {
						t.Fatalf("turn four re-applied the retired remembered %s", tc.member)
					}
					continue
				}
				if !tc.remembered(three) || !inLedger || !tc.remembered(four) {
					t.Fatalf("control: remembered on three=%v, in three's ledger=%v, remembered on four=%v; want all true with nothing stated", tc.remembered(three), inLedger, tc.remembered(four))
				}
			}
		})
	}
}

// windowLedgerChain drives turn one (raises the window need), then turn two,
// which redeems the window beside a structure receipt that vetoes -- a parent
// whose ledger remembers the window and which persists no effective window.
func windowLedgerChain(t *testing.T, h *needTurnHarness, prefix string) (needTurnOutcome, needTurnOutcome, contractsv1.ContextFabricWindowOption) {
	t.Helper()
	one, option := windowTurnOne(t, h, "request_need_"+prefix+"_one")
	request := needTurnRequest("request_need_"+prefix+"_two", false)
	request.PriorWindowReceipts = []BoundSubjectReceipt{{ResultID: one.result.ResultID, ReceiptID: option.ReceiptID}}
	request.PriorKindReceipts = []BoundSubjectReceipt{{ResultID: one.result.ResultID, ReceiptID: "kindr_needmissing0001"}}
	two := h.turn(request, committingNeedResponse())
	if two.saved == nil || len(two.saved.ConfirmedNeeds) != 1 || two.result.EffectiveEvidenceWindow != nil {
		t.Fatalf("fixture defect: turn two must remember the window and persist none; saved=%#v window=%#v", two.saved, two.result.EffectiveEvidenceWindow)
	}
	return one, two, option
}

// TestConfirmedNeedConsumers_RememberedWindowAppliesWhereTheReceiptDoes pins
// application and reuse-key parity: a remembered window and a fresh receipt
// for the same option, on otherwise identical turns, produce the same
// effective window under the same save key and meet the same axis-conflict
// veto -- whatever Interpret infers. Controls: the same turn under a changed
// identity has no window at all (no inferred default either), keys
// unwindowed, and meets no veto.
func TestConfirmedNeedConsumers_RememberedWindowAppliesWhereTheReceiptDoes(t *testing.T) {
	t.Parallel()
	h := newNeedTurnHarness(t, nil)
	one, two, option := windowLedgerChain(t, h, "parity")
	changed := func(request InvestigationRequest) InvestigationRequest {
		request.RequestedScope.RepositorySlugs = []string{"full-chaos/dev-health-acr"}
		return request
	}
	fresh := func(requestID string) InvestigationRequest {
		request := needTurnRequest(requestID, false)
		request.PriorWindowReceipts = []BoundSubjectReceipt{{ResultID: one.result.ResultID, ReceiptID: option.ReceiptID}}
		// A parent reference keeps the receipt turn outside the window-only
		// continuation shape, as the ledger turn is.
		request.ParentResultID = one.result.ResultID
		return request
	}

	t.Run("no inferred default: applied, served and keyed like the receipt", func(t *testing.T) {
		h.windowless = true
		defer func() { h.windowless = false }()
		ledger := h.turn(continuingNeedTurn(needTurnRequest("request_need_parity_ledger", false), two.result.ResultID), committingNeedResponse())
		receipt := h.turn(fresh("request_need_parity_receipt"), committingNeedResponse())
		control := h.turn(changed(continuingNeedTurn(needTurnRequest("request_need_parity_control", false), two.result.ResultID)), committingNeedResponse())
		for name, turn := range map[string]needTurnOutcome{"ledger": ledger, "receipt": receipt} {
			window := turn.result.EffectiveEvidenceWindow
			if turn.result.Status == InvestigationClarificationRequired || turn.result.Status == InvestigationNoMatch || window == nil ||
				window.Provenance != WindowClarificationConfirmed || window.RelativeID != option.RelativeID ||
				!sameWindowBounds(window.Start, option.Start) || !sameWindowBounds(window.End, option.End) {
				t.Fatalf("%s turn: status=%s window=%#v, want served under the confirmed option's frozen window", name, turn.result.Status, window)
			}
		}
		if !reflect.DeepEqual(ledger.windows, []confirmedNeedLedgerWindowRecord{{ConfirmedNeedLedgerWindowApplied, two.result.ResultID, string(option.RelativeID)}}) {
			t.Fatalf("ledger window events = %#v, want exactly one applied", ledger.windows)
		}
		if ledger.saveKey != receipt.saveKey || ledger.saveKey == control.saveKey {
			t.Fatalf("save keys ledger=%q receipt=%q control=%q, want ledger == receipt != control", ledger.saveKey, receipt.saveKey, control.saveKey)
		}
		if control.result.EffectiveEvidenceWindow != nil || len(control.windows) != 0 || control.saveKey != TimeAxisKeyFor(TimeContext{Axis: TemporalCurrent}) {
			t.Fatalf("control: window=%#v ledger windows=%#v key=%q, want no window, no decision, the unwindowed key", control.result.EffectiveEvidenceWindow, control.windows, control.saveKey)
		}
		last := func(o needTurnOutcome) WindowCanonicalizationOutcome { return o.windowCanons[len(o.windowCanons)-1] }
		if last(ledger) != WindowCanonicalizationCarried || last(receipt) != WindowCanonicalizationReceiptConfirmed {
			t.Fatalf("canonicalization outcomes ledger=%s receipt=%s, want carried and receipt_confirmed", last(ledger), last(receipt))
		}
	})

	t.Run("interpretation moves the axis: the same veto as the receipt", func(t *testing.T) {
		h.historical = true
		defer func() { h.historical = false }()
		ledger := h.turn(continuingNeedTurn(needTurnRequest("request_need_parity_axis_ledger", false), two.result.ResultID), committingNeedResponse())
		receipt := h.turn(fresh("request_need_parity_axis_receipt"), committingNeedResponse())
		control := h.turn(changed(continuingNeedTurn(needTurnRequest("request_need_parity_axis_control", false), two.result.ResultID)), committingNeedResponse())
		vetoed := func(o needTurnOutcome) bool {
			for _, outcome := range o.windowCanons {
				if outcome == WindowCanonicalizationVetoAxisConflict {
					return o.result.Status == InvestigationNoMatch
				}
			}
			return false
		}
		if !vetoed(ledger) || !vetoed(receipt) || vetoed(control) {
			t.Fatalf("axis-conflict veto ledger=%v receipt=%v control=%v, want true, true, false", vetoed(ledger), vetoed(receipt), vetoed(control))
		}
		if ledger.saved == nil || len(ledger.saved.ConfirmedNeeds) != 1 || ledger.saved.ConfirmedNeeds[0].Member != contractsv1.ContextFabricStructureNeedWindow {
			t.Fatalf("ledger turn saved %#v, want the remembered window carried forward through the veto", ledger.saved)
		}
	})
}

// TestConfirmedNeedConsumers_LedgerLineOnEveryExit pins the telemetry axis:
// every exit that follows admission -- the window veto, the explicit-window
// gate and the structure veto included -- reports the admitted ledger exactly
// once, applying nothing on an exit that ran no consumer; the window decision
// is reported on those exits too, and a remembered window applied before the
// structure veto is echoed there as the receipt would be. Controls: the
// decisive path reports what applied, and a turn naming no parent reports
// miss_no_reference -- one line each.
func TestConfirmedNeedConsumers_LedgerLineOnEveryExit(t *testing.T) {
	t.Parallel()
	t.Run("candidate ledger", func(t *testing.T) {
		t.Parallel()
		h := newNeedTurnHarness(t, nil)
		_, two, offer := candidateTurnsOneAndTwo(t, h)
		want := []ConfirmedNeedEntry{{Member: contractsv1.ContextFabricStructureNeedSubjectCandidate, AppliedKind: offer.Kind, AppliedValue: offer.CanonicalID}}
		for _, cell := range []struct {
			name   string
			mutate func(*InvestigationRequest)
			status InvestigationStatus
		}{
			{"structure veto", func(r *InvestigationRequest) {
				r.PriorKindReceipts = []BoundSubjectReceipt{{ResultID: two.result.ResultID, ReceiptID: "kindr_needmissing0001"}}
			}, InvestigationNoMatch},
			{"window veto", func(r *InvestigationRequest) {
				r.PriorWindowReceipts = []BoundSubjectReceipt{{ResultID: two.result.ResultID, ReceiptID: "winr_needmissing0001"}}
			}, InvestigationNoMatch},
			{"explicit window gate", func(r *InvestigationRequest) {
				r.Consumer = ConsumerInfo{Name: "test", Version: "1.0.0", Surface: "mcp"}
				r.TimeContext.EvidenceWindow = &contractsv1.ContextFabricRequestedEvidenceWindow{RelativeID: RelativeWindowTrailing30D}
			}, InvestigationClarificationRequired},
		} {
			request := continuingNeedTurn(needTurnRequest("request_need_exit_"+strings.ReplaceAll(cell.name, " ", "_"), true), two.result.ResultID)
			cell.mutate(&request)
			out := h.turn(request, committingNeedResponse())
			if out.result.Status != cell.status || len(out.calls) != 0 {
				t.Fatalf("fixture defect: %s must end the turn before resolution with %s; got %s after %d resolutions", cell.name, cell.status, out.result.Status, len(out.calls))
			}
			if event := soleLedgerEvent(t, out); event.Outcome != ConfirmedNeedLedgerHit || event.SourceResultID != two.result.ResultID || len(event.AppliedMembers) != 0 {
				t.Fatalf("%s: ledger event = %#v, want one hit from turn two applying nothing", cell.name, event)
			}
			if out.saved == nil || !reflect.DeepEqual(out.saved.ConfirmedNeeds, want) {
				t.Fatalf("%s: saved ledger = %#v, want %#v carried forward", cell.name, out.saved, want)
			}
		}
		decisive := h.turn(continuingNeedTurn(needTurnRequest("request_need_exit_decisive", true), two.result.ResultID), committingNeedResponse())
		if event := soleLedgerEvent(t, decisive); !reflect.DeepEqual(event.AppliedMembers, []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedSubjectCandidate}) {
			t.Fatalf("control: decisive ledger event = %#v, want subject_candidate applied", event)
		}
		if event := soleLedgerEvent(t, h.turn(needTurnRequest("request_need_exit_no_parent", true), committingNeedResponse())); event.Outcome != ConfirmedNeedLedgerMissNoReference {
			t.Fatalf("control: no-parent ledger event = %#v, want miss_no_reference", event)
		}
	})
	t.Run("window ledger", func(t *testing.T) {
		t.Parallel()
		h := newNeedTurnHarness(t, nil)
		_, two, option := windowLedgerChain(t, h, "exit_window")
		vetoRequest := continuingNeedTurn(needTurnRequest("request_need_exit_window_structure", false), two.result.ResultID)
		vetoRequest.PriorKindReceipts = []BoundSubjectReceipt{{ResultID: two.result.ResultID, ReceiptID: "kindr_needmissing0001"}}
		veto := h.turn(vetoRequest, committingNeedResponse())
		if veto.result.Status != InvestigationNoMatch || soleLedgerEvent(t, veto).Outcome != ConfirmedNeedLedgerHit {
			t.Fatalf("fixture defect: structure veto status=%s ledger=%#v", veto.result.Status, veto.ledgers)
		}
		if !reflect.DeepEqual(veto.windows, []confirmedNeedLedgerWindowRecord{{ConfirmedNeedLedgerWindowApplied, two.result.ResultID, string(option.RelativeID)}}) {
			t.Fatalf("structure veto: ledger window events = %#v, want exactly one applied", veto.windows)
		}
		if got := memberEntries(veto.result, contractsv1.ContextFabricStructureNeedWindow); !reflect.DeepEqual(got, []ConfirmedStructureEntry{carriedNeedEntry(contractsv1.ContextFabricStructureNeedWindow, string(option.RelativeID), two.result.ResultID)}) {
			t.Fatalf("structure veto: window echo = %#v, want the remembered window's carried entry", got)
		}
		windowVetoRequest := continuingNeedTurn(needTurnRequest("request_need_exit_window_veto", false), two.result.ResultID)
		windowVetoRequest.PriorWindowReceipts = []BoundSubjectReceipt{{ResultID: two.result.ResultID, ReceiptID: "winr_needmissing0001"}}
		windowVeto := h.turn(windowVetoRequest, committingNeedResponse())
		if windowVeto.result.Status != InvestigationNoMatch || soleLedgerEvent(t, windowVeto).Outcome != ConfirmedNeedLedgerHit {
			t.Fatalf("fixture defect: window veto status=%s ledger=%#v", windowVeto.result.Status, windowVeto.ledgers)
		}
		if !reflect.DeepEqual(windowVeto.windows, []confirmedNeedLedgerWindowRecord{{ConfirmedNeedLedgerWindowNotApplicable, two.result.ResultID, string(option.RelativeID)}}) {
			t.Fatalf("window veto: ledger window events = %#v, want exactly one not_applicable", windowVeto.windows)
		}
		if got := memberEntries(windowVeto.result, contractsv1.ContextFabricStructureNeedWindow); len(got) != 0 {
			t.Fatalf("window veto: window echo = %#v, want none from the ledger", got)
		}
	})
}

// TestMergeConfirmedNeedsLedger_StatedThisTurnRetiresTheRemembered sweeps the
// outgoing-ledger half of supersession over every member and every statement
// route: a receipt replaces the remembered value, an explicit field retires it,
// and nothing else changes. Control: nothing stated keeps every remembered
// entry.
func TestConfirmedNeedConsumers_EarlyExitRetiresExplicitMember(t *testing.T) {
	t.Parallel()
	for _, member := range []contractsv1.ContextFabricStructureNeedKind{
		contractsv1.ContextFabricStructureNeedExpectedKind,
		contractsv1.ContextFabricStructureNeedSubjectHandle,
		contractsv1.ContextFabricStructureNeedWindow,
	} {
		for _, gate := range []string{"structure_veto", "window_veto", "explicit_window_gate"} {
			// The explicit-window gate itself states a window, so it cannot
			// provide a control that changes only whether one was stated.
			if member == contractsv1.ContextFabricStructureNeedWindow && gate == "explicit_window_gate" {
				continue
			}
			t.Run(string(member)+"/"+gate, func(t *testing.T) {
				t.Parallel()
				for _, stated := range []bool{false, true} {
					candidate := ConfirmedNeedEntry{Member: contractsv1.ContextFabricStructureNeedSubjectCandidate, AppliedKind: SubjectRepository, AppliedValue: "repository:need-r2"}
					entry := ConfirmedNeedEntry{Member: member}
					switch member {
					case contractsv1.ContextFabricStructureNeedExpectedKind:
						entry.AppliedValue = string(SubjectTeam)
					case contractsv1.ContextFabricStructureNeedSubjectHandle:
						entry.AppliedKind, entry.AppliedValue, entry.PatternID = SubjectPullRequest, "532", "pull_request_number"
					case contractsv1.ContextFabricStructureNeedWindow:
						entry.AppliedValue = string(RelativeWindowTrailing90D)
					}
					store := &staticResultStore{results: map[string]InvestigationResult{}, states: map[string]*PersistedSemanticState{}}
					ledgerOnlyParent(t, store, "result_need_early_parent", nil, []ConfirmedNeedEntry{entry, candidate})
					h := newNeedTurnHarness(t, store)
					request := continuingNeedTurn(needTurnRequest("request_need_early_retirement", member != contractsv1.ContextFabricStructureNeedWindow), "result_need_early_parent")
					if stated {
						switch member {
						case contractsv1.ContextFabricStructureNeedExpectedKind:
							request.ExpectedKinds = []SubjectKind{SubjectRepository}
						case contractsv1.ContextFabricStructureNeedSubjectHandle:
							request.SubjectHandles = []contractsv1.ContextFabricRequestedHandle{{Kind: SubjectPullRequest, PatternID: "pull_request_number", Value: "777"}}
						case contractsv1.ContextFabricStructureNeedWindow:
							request.TimeContext.EvidenceWindow = validConfirmedWindow()
						}
					}
					status := InvestigationNoMatch
					switch gate {
					case "structure_veto":
						request.PriorKindReceipts = []BoundSubjectReceipt{{ResultID: "result_need_early_parent", ReceiptID: "kindr_missing00001"}}
					case "window_veto":
						request.PriorWindowReceipts = []BoundSubjectReceipt{{ResultID: "result_need_early_parent", ReceiptID: "winr_missing000001"}}
					case "explicit_window_gate":
						request.Consumer = ConsumerInfo{Name: "test", Version: "1.0.0", Surface: "mcp"}
						request.TimeContext.EvidenceWindow = &contractsv1.ContextFabricRequestedEvidenceWindow{RelativeID: RelativeWindowTrailing30D}
						status = InvestigationClarificationRequired
					}
					out := h.turn(request, committingNeedResponse())
					if out.result.Status != status || len(out.calls) != 0 || soleLedgerEvent(t, out).Outcome != ConfirmedNeedLedgerHit || out.saved == nil {
						t.Fatalf("fixture: stated=%v status=%s calls=%d ledger=%#v", stated, out.result.Status, len(out.calls), out.saved)
					}
					retained, unrelated := false, false
					for _, saved := range out.saved.ConfirmedNeeds {
						retained = retained || saved.Member == member
						unrelated = unrelated || reflect.DeepEqual(saved, candidate)
					}
					if retained == stated || !unrelated {
						t.Fatalf("stated=%v retained=%v unrelated=%v ledger=%#v", stated, retained, unrelated, out.saved.ConfirmedNeeds)
					}
				}
			})
		}
	}
}

func TestMergeConfirmedNeedsLedger_StatedThisTurnRetiresTheRemembered(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	remembered := []confirmedStructureMember{
		{Member: contractsv1.ContextFabricStructureNeedExpectedKind, AppliedValue: string(SubjectTeam)},
		{Member: contractsv1.ContextFabricStructureNeedSubjectAnchor, AppliedKind: SubjectTeam, AppliedValue: "a"},
		{Member: contractsv1.ContextFabricStructureNeedSubjectCandidate, AppliedKind: SubjectRepository, AppliedValue: "c"},
		{Member: contractsv1.ContextFabricStructureNeedSubjectHandle, AppliedKind: SubjectPullRequest, AppliedValue: "h", PatternID: "p"},
		{Member: contractsv1.ContextFabricStructureNeedWindow, AppliedValue: string(RelativeWindowTrailing90D), WindowStart: &start, WindowEnd: &end},
	}
	members := func(entries []ConfirmedNeedEntry) map[contractsv1.ContextFabricStructureNeedKind]string {
		out := map[contractsv1.ContextFabricStructureNeedKind]string{}
		for _, entry := range entries {
			out[entry.Member] = entry.AppliedValue
		}
		return out
	}
	all := members(mergeConfirmedNeedsLedger(remembered, nil, validInvestigationRequest()))
	if len(all) != 5 {
		t.Fatalf("control: merged = %#v, want all five remembered members kept with nothing stated", all)
	}
	for _, r := range remembered {
		got := members(mergeConfirmedNeedsLedger(remembered, []confirmedStructureMember{{Member: r.Member, AppliedValue: "fresh"}}, validInvestigationRequest()))
		if len(got) != 5 || got[r.Member] != "fresh" {
			t.Fatalf("receipt for %s: merged = %#v, want that member replaced by the receipt's value", r.Member, got)
		}
	}
	explicit := map[string]struct {
		member contractsv1.ContextFabricStructureNeedKind
		state  func(*InvestigationRequest)
	}{
		"singular handle": {contractsv1.ContextFabricStructureNeedSubjectHandle, func(r *InvestigationRequest) {
			r.SubjectHandles = []contractsv1.ContextFabricRequestedHandle{{Kind: SubjectPullRequest, PatternID: "p", Value: "9"}}
		}},
		"plural handles": {contractsv1.ContextFabricStructureNeedSubjectHandle, func(r *InvestigationRequest) {
			r.SubjectHandles = []contractsv1.ContextFabricRequestedHandle{{Kind: SubjectPullRequest, PatternID: "p", Value: "9"}, {Kind: SubjectPullRequest, PatternID: "p", Value: "10"}}
		}},
		"singular kind": {contractsv1.ContextFabricStructureNeedExpectedKind, func(r *InvestigationRequest) {
			r.ExpectedKinds = []contractsv1.ContextFabricSubjectKind{SubjectRepository}
		}},
		"plural kinds": {contractsv1.ContextFabricStructureNeedExpectedKind, func(r *InvestigationRequest) {
			r.ExpectedKinds = []contractsv1.ContextFabricSubjectKind{SubjectRepository, SubjectProject}
		}},
		"evidence window": {contractsv1.ContextFabricStructureNeedWindow, func(r *InvestigationRequest) {
			r.TimeContext.EvidenceWindow = validConfirmedWindow()
		}},
	}
	for name, cell := range explicit {
		request := validInvestigationRequest()
		cell.state(&request)
		got := members(mergeConfirmedNeedsLedger(remembered, nil, request))
		if _, kept := got[cell.member]; kept || len(got) != 4 {
			t.Fatalf("explicit %s: merged = %#v, want %s alone retired", name, got, cell.member)
		}
		if applied := appliedNeedLedgerEntries(remembered, nil, request); cell.member != contractsv1.ContextFabricStructureNeedWindow {
			if _, ok := applied[cell.member]; ok {
				t.Fatalf("explicit %s: %s still applies", name, cell.member)
			}
		}
	}
}

func TestConfirmedNeedConsumers_StructureVetoEchoParity(t *testing.T) {
	for _, member := range []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedExpectedKind, contractsv1.ContextFabricStructureNeedSubjectAnchor, contractsv1.ContextFabricStructureNeedSubjectHandle, contractsv1.ContextFabricStructureNeedSubjectCandidate} {
		t.Run(string(member), func(t *testing.T) {
			h := newNeedTurnHarness(t, nil, func(d *EngineDependencies) {
				d.AnchorVerifier = func(context.Context, string, contractsv1.ContextFabricSubjectKind, string, string) (bool, AnchorVerificationReason) {
					return true, AnchorVerificationValid
				}
				d.AnchorMembershipVerifier = func(context.Context, storage.Principal, RequestedScope, ResolvedGraphBinding, contractsv1.ContextFabricSubjectKind, string, string) (bool, AnchorVerificationReason) {
					return true, AnchorVerificationValid
				}
			})
			response := candidateOfferingNeedResponse()
			response.material.Missing = []contractsv1.ContextFabricStructureNeedKind{member}
			switch member {
			case contractsv1.ContextFabricStructureNeedExpectedKind:
				response.material.CandidateOptions = nil
				response.material.KindOptions = []KindOption{{Label: "a repository", Kind: SubjectRepository, OfferSource: "engine"}}
			case contractsv1.ContextFabricStructureNeedSubjectAnchor:
				response.material.CandidateOptions = nil
				response.material.AnchorOptions = []AnchorOption{{Label: "need-r2", Kind: SubjectRepository, CanonicalID: "repository:need-r2", MatchedTermHash: "aa11bb22cc33dd44ee55ff66", OfferSource: "engine"}}
			case contractsv1.ContextFabricStructureNeedSubjectHandle:
				response = handleOfferingNeedResponse()
			}
			one := h.turn(needTurnRequest("request_need_veto_offer", true), response)
			if one.result.StructureNeeds == nil {
				t.Fatal("fixture: expected generated offer")
			}
			confirm := func(r *InvestigationRequest) {
				var receipt string
				switch member {
				case contractsv1.ContextFabricStructureNeedExpectedKind:
					receipt = one.result.StructureNeeds.KindOptions[0].ReceiptID
					r.PriorKindReceipts = []BoundSubjectReceipt{{ResultID: one.result.ResultID, ReceiptID: receipt}}
				case contractsv1.ContextFabricStructureNeedSubjectAnchor:
					receipt = one.result.StructureNeeds.AnchorOptions[0].ReceiptID
					r.PriorAnchorReceipts = []BoundSubjectReceipt{{ResultID: one.result.ResultID, ReceiptID: receipt}}
				case contractsv1.ContextFabricStructureNeedSubjectHandle:
					receipt = one.result.StructureNeeds.HandleOptions[0].ReceiptID
					r.PriorHandleReceipts = []BoundSubjectReceipt{{ResultID: one.result.ResultID, ReceiptID: receipt}}
				case contractsv1.ContextFabricStructureNeedSubjectCandidate:
					receipt = one.result.StructureNeeds.CandidateOptions[0].ReceiptID
					r.PriorCandidateReceipts = []BoundSubjectReceipt{{ResultID: one.result.ResultID, ReceiptID: receipt}}
				}
			}
			parentReq := needTurnRequest("request_need_veto_parent", true)
			confirm(&parentReq)
			parent := h.turn(parentReq, committingNeedResponse())
			if parent.saved == nil || len(parent.saved.ConfirmedNeeds) != 1 {
				t.Fatalf("fixture: one persisted confirmation required: %#v", parent.saved)
			}
			ledgerReq := continuingNeedTurn(needTurnRequest("request_need_veto_ledger", true), parent.result.ResultID)
			wantDisposition := contractsv1.ContextFabricStructureDispositionVetoedUnresolved
			if member == contractsv1.ContextFabricStructureNeedSubjectCandidate {
				// Candidate is last in the receipt loop. A conflict after that loop
				// proves it was confirmed before the veto, rather than never evaluated.
				handleOffer := h.turn(needTurnRequest("request_need_veto_handle_offer", true), handleOfferingNeedResponse())
				ledgerReq.PriorHandleReceipts = []BoundSubjectReceipt{{ResultID: handleOffer.result.ResultID, ReceiptID: handleOffer.result.StructureNeeds.HandleOptions[0].ReceiptID}}
				ledgerReq.SubjectHandles = []contractsv1.ContextFabricRequestedHandle{{Kind: SubjectPullRequest, PatternID: "pull_request_number", Value: "777"}}
				wantDisposition = contractsv1.ContextFabricStructureDispositionVetoedConflict
			} else {
				ledgerReq.PriorCandidateReceipts = []BoundSubjectReceipt{{ResultID: one.result.ResultID, ReceiptID: "candr_missing00001"}}
			}
			freshReq := ledgerReq
			freshReq.RequestID = "request_need_veto_fresh"
			confirm(&freshReq)
			fresh := h.turn(freshReq, committingNeedResponse())
			ledger := h.turn(ledgerReq, committingNeedResponse())
			f := memberEntries(fresh.result, member)
			l := memberEntries(ledger.result, member)
			if fresh.result.Status != InvestigationNoMatch || ledger.result.Status != InvestigationNoMatch || len(f) != 1 || f[0].Disposition != wantDisposition || len(ledger.calls) != 0 || soleLedgerEvent(t, ledger).Outcome != ConfirmedNeedLedgerHit {
				t.Fatalf("fixture: fresh=%#v ledger=%#v", f, l)
			}
			if len(l) != 1 || l[0].AppliedValue != f[0].AppliedValue || l[0].Disposition != f[0].Disposition || l[0].Source != contractsv1.ContextFabricStructureSourceCarried || l[0].PriorResultID != parent.result.ResultID || l[0].ReceiptID != "" {
				t.Errorf("ledger echo=%#v fresh=%#v: want same value/disposition with carried provenance", l, f)
			}
			if ledger.saved == nil || !reflect.DeepEqual(ledger.saved.ConfirmedNeeds, parent.saved.ConfirmedNeeds) {
				t.Error("veto lost the previously confirmed ledger")
			}
			controlReq := ledgerReq
			controlReq.RequestID = "request_need_veto_changed_scope"
			controlReq.RequestedScope.RepositorySlugs = []string{"full-chaos/dev-health-acr"}
			control := h.turn(controlReq, committingNeedResponse())
			if soleLedgerEvent(t, control).Outcome != ConfirmedNeedLedgerDroppedIdentityChanged || len(memberEntries(control.result, member)) != 0 {
				t.Fatal("changed identity disclosed a remembered member")
			}
		})
	}
}

func TestAppendVetoedRememberedNeeds(t *testing.T) {
	t.Parallel()
	ledger := confirmedNeedLedgerResult{SourceResultID: "result_need_veto_parent", Entries: []confirmedStructureMember{
		{Member: contractsv1.ContextFabricStructureNeedExpectedKind, AppliedValue: string(SubjectRepository)},
		{Member: contractsv1.ContextFabricStructureNeedSubjectAnchor, AppliedValue: "repository:need-anchor"},
		{Member: contractsv1.ContextFabricStructureNeedSubjectHandle, AppliedValue: "532"},
		{Member: contractsv1.ContextFabricStructureNeedSubjectCandidate, AppliedValue: "repository:need-candidate"},
		{Member: contractsv1.ContextFabricStructureNeedWindow, AppliedValue: string(RelativeWindowTrailing90D)},
	}}
	for veto, disposition := range map[structureVetoReason]contractsv1.ContextFabricStructureDisposition{structureVetoConfirmationUnresolved: contractsv1.ContextFabricStructureDispositionVetoedUnresolved, structureVetoConfirmationConflict: contractsv1.ContextFabricStructureDispositionVetoedConflict, structureVetoStaleSupersededOffer: contractsv1.ContextFabricStructureDispositionVetoedStale} {
		got := appendVetoedRememberedNeeds(nil, ledger, InvestigationRequest{}, veto)
		if len(got) != 4 {
			t.Fatalf("%s entries=%#v, want four structure members", veto, got)
		}
		for _, entry := range got {
			if err := entry.Validate(); err != nil {
				t.Fatal(err)
			}
			if entry.Disposition != disposition || entry.Source != contractsv1.ContextFabricStructureSourceCarried || entry.Member == contractsv1.ContextFabricStructureNeedWindow || entry.PriorResultID != ledger.SourceResultID {
				t.Fatalf("%s: %#v", veto, entry)
			}
		}
	}
	for _, veto := range []structureVetoReason{structureVetoNone, "unrecognized"} {
		if got := appendVetoedRememberedNeeds(nil, ledger, InvestigationRequest{}, veto); len(got) != 0 {
			t.Fatalf("non-veto %q emitted %#v", veto, got)
		}
	}
	receipt := []BoundSubjectReceipt{{ResultID: "result_fresh_need", ReceiptID: "receipt_fresh_need"}}
	for _, cell := range []struct {
		name    string
		request InvestigationRequest
		blocked contractsv1.ContextFabricStructureNeedKind
	}{
		{"kind receipt", InvestigationRequest{PriorKindReceipts: receipt}, contractsv1.ContextFabricStructureNeedExpectedKind},
		{"anchor receipt", InvestigationRequest{PriorAnchorReceipts: receipt}, contractsv1.ContextFabricStructureNeedSubjectAnchor},
		{"handle receipt", InvestigationRequest{PriorHandleReceipts: receipt}, contractsv1.ContextFabricStructureNeedSubjectHandle},
		{"candidate receipt", InvestigationRequest{PriorCandidateReceipts: receipt}, contractsv1.ContextFabricStructureNeedSubjectCandidate},
		{"explicit kind", InvestigationRequest{ExpectedKinds: []SubjectKind{SubjectTeam}}, contractsv1.ContextFabricStructureNeedExpectedKind},
		{"explicit handle", InvestigationRequest{SubjectHandles: []contractsv1.ContextFabricRequestedHandle{{Kind: SubjectPullRequest, Value: "777"}}}, contractsv1.ContextFabricStructureNeedSubjectHandle},
	} {
		t.Run(cell.name, func(t *testing.T) {
			got := appendVetoedRememberedNeeds(nil, ledger, cell.request, structureVetoConfirmationConflict)
			if len(got) != 3 {
				t.Fatalf("got %#v, want only three unstated members", got)
			}
			for _, entry := range got {
				if entry.Member == cell.blocked {
					t.Fatalf("supplied member fell back to old value: %#v", entry)
				}
			}
		})
	}
	existing := ConfirmedStructureEntry{Member: contractsv1.ContextFabricStructureNeedSubjectCandidate, AppliedValue: "repository:new", Source: contractsv1.ContextFabricStructureSourceReceipt, PriorResultID: "result_fresh_need", ReceiptID: "receipt_fresh_need", Provenance: contractsv1.ContextFabricStructureClarificationConfirmed, Disposition: contractsv1.ContextFabricStructureDispositionVetoedConflict}
	got := appendVetoedRememberedNeeds([]ConfirmedStructureEntry{existing}, ledger, InvestigationRequest{}, structureVetoConfirmationConflict)
	if len(got) != 4 || !reflect.DeepEqual(got[0], existing) {
		t.Fatalf("existing echo replaced or duplicated: %#v", got)
	}
	for _, entry := range got[1:] {
		if entry.Member == existing.Member {
			t.Fatal("duplicate member")
		}
	}
	if got := appendVetoedRememberedNeeds([]ConfirmedStructureEntry{existing}, confirmedNeedLedgerResult{}, InvestigationRequest{}, structureVetoConfirmationConflict); !reflect.DeepEqual(got, []ConfirmedStructureEntry{existing}) {
		t.Fatalf("empty ledger changed echo: %#v", got)
	}
}

// A confirmed window has the same authority as its fresh receipt when a
// separately selected candidate carries a different window. All offers and
// parent ledgers below come from preceding engine turns.
func TestConfirmedNeedConsumers_ConfirmedWindowPrecedesDifferentCarrier(t *testing.T) {
	h := newNeedTurnHarness(t, nil)
	offerTurn, long := windowTurnOne(t, h, "request_resume_window_offer")
	var short contractsv1.ContextFabricWindowOption
	for _, o := range offerTurn.result.WindowClarification.Options {
		if o.RelativeID == RelativeWindowTrailing30D && o.Start != nil {
			short = o
			break
		}
	}
	if short.ReceiptID == "" || long.RelativeID != RelativeWindowTrailing90D {
		t.Fatal("fixture: two distinct generated windows required")
	}
	parentReq := needTurnRequest("request_resume_window_parent", false)
	parentReq.PriorWindowReceipts = []BoundSubjectReceipt{{ResultID: offerTurn.result.ResultID, ReceiptID: long.ReceiptID}}
	parent := h.turn(parentReq, committingNeedResponse())
	carrierReq := needTurnRequest("request_resume_window_carrier", false)
	carrierReq.PriorWindowReceipts = []BoundSubjectReceipt{{ResultID: offerTurn.result.ResultID, ReceiptID: short.ReceiptID}}
	carrier := h.turn(carrierReq, candidateOfferingNeedResponse())
	if parent.saved == nil || len(parent.saved.ConfirmedNeeds) != 1 || carrier.result.StructureNeeds == nil || len(carrier.result.StructureNeeds.CandidateOptions) == 0 {
		t.Fatal("fixture: persisted ledger and candidate offer required")
	}
	candidate := carrier.result.StructureNeeds.CandidateOptions[0]
	req := continuingNeedTurn(needTurnRequest("request_resume_window_ledger", false), parent.result.ResultID)
	req.PriorCandidateReceipts = []BoundSubjectReceipt{{ResultID: carrier.result.ResultID, ReceiptID: candidate.ReceiptID}}
	ledger := h.turn(req, committingNeedResponse())
	freshReq := req
	freshReq.RequestID = "request_resume_window_fresh"
	freshReq.PriorWindowReceipts = []BoundSubjectReceipt{{ResultID: offerTurn.result.ResultID, ReceiptID: long.ReceiptID}}
	fresh := h.turn(freshReq, committingNeedResponse())
	controlReq := req
	controlReq.RequestID = "request_resume_window_control"
	controlReq.ParentResultID = ""
	controlReq.Conversation = nil
	control := h.turn(controlReq, committingNeedResponse())
	for name, o := range map[string]needTurnOutcome{"ledger": ledger, "fresh": fresh, "carrier_only": control} {
		if o.result.EffectiveEvidenceWindow == nil {
			t.Fatalf("fixture: %s no effective window", name)
		}
		t.Logf("%s effective=%s save_key=%s ledger=%v carrier=%v", name, o.result.EffectiveEvidenceWindow.RelativeID, o.saveKey, o.windows, o.windowCarry)
	}
	if !reflect.DeepEqual(ledger.result.EffectiveEvidenceWindow, fresh.result.EffectiveEvidenceWindow) || ledger.saveKey != fresh.saveKey {
		t.Fatal("ledger/fresh differ")
	}
	if control.result.EffectiveEvidenceWindow.RelativeID != short.RelativeID || ledger.result.EffectiveEvidenceWindow.RelativeID != long.RelativeID {
		t.Fatal("fixture did not distinguish sources")
	}

	// A new window confirmation on this turn replaces the remembered 90 days.
	replacementReq := req
	replacementReq.RequestID = "request_need_window_replacement"
	replacementReq.PriorWindowReceipts = []BoundSubjectReceipt{{ResultID: offerTurn.result.ResultID, ReceiptID: short.ReceiptID}}
	replacement := h.turn(replacementReq, committingNeedResponse())
	if !reflect.DeepEqual(replacement.result.EffectiveEvidenceWindow, control.result.EffectiveEvidenceWindow) || replacement.saveKey != control.saveKey {
		t.Fatal("current window confirmation must replace the remembered window")
	}
	if replacement.saved == nil || len(replacement.saved.ConfirmedNeeds) == 0 {
		t.Fatal("replacement must save its new window confirmation")
	}
	windowSaved := false
	for _, entry := range replacement.saved.ConfirmedNeeds {
		if entry.Member == contractsv1.ContextFabricStructureNeedWindow {
			windowSaved = true
			if entry.AppliedValue != string(short.RelativeID) || !sameWindowBounds(entry.WindowStart, short.Start) || !sameWindowBounds(entry.WindowEnd, short.End) {
				t.Fatalf("replacement did not save the new frozen window: %+v", entry)
			}
		}
	}
	if !windowSaved {
		t.Fatal("replacement ledger omitted the new window")
	}
}
