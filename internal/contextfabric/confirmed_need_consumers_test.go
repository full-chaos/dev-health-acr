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
	return g.response.resolution, g.response.material, g.response.bases, nil, nil
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
	saved        *PersistedSemanticState
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
		Results:   store,
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
	h.store.saved, h.store.savedSemantic = nil, nil
	result, err := h.engine.Investigate(context.Background(), acceptancePrincipal(), request)
	if err != nil {
		h.t.Fatalf("Investigate(%s) error = %v", request.RequestID, err)
	}
	if h.store.saved == nil {
		h.t.Fatalf("fixture defect: turn %s saved nothing", request.RequestID)
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
		saved:        saved,
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
		if len(three.windowCarry) != 1 || three.windowCarry[0].outcome == WindowCarryHit {
			t.Fatalf("window carries = %#v, want exactly one miss (the population this consumer exists for)", three.windowCarry)
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
	})
}

// TestConfirmedNeedConsumers_WindowCarrierWins pins the precedence: when the
// parent persisted its confirmed window, the same-conversation carrier
// supplies it and the ledger stands down -- one window value, one entry.
func TestConfirmedNeedConsumers_WindowCarrierWins(t *testing.T) {
	t.Parallel()
	h := newNeedTurnHarness(t, nil)
	one, option := windowTurnOne(t, h, "request_need_precedence_one")
	twoRequest := needTurnRequest("request_need_precedence_two", false)
	twoRequest.PriorWindowReceipts = []BoundSubjectReceipt{{ResultID: one.result.ResultID, ReceiptID: option.ReceiptID}}
	two := h.turn(twoRequest, committingNeedResponse())
	if two.result.EffectiveEvidenceWindow == nil || two.saved == nil || len(two.saved.ConfirmedNeeds) != 1 {
		t.Fatalf("fixture defect: turn two must persist its confirmed window and ledger; window=%#v saved=%#v", two.result.EffectiveEvidenceWindow, two.saved)
	}
	three := h.turn(continuingNeedTurn(needTurnRequest("request_need_precedence_three", false), two.result.ResultID), committingNeedResponse())
	if len(three.windowCarry) != 1 || three.windowCarry[0].outcome != WindowCarryHit {
		t.Fatalf("window carries = %#v, want exactly one hit", three.windowCarry)
	}
	if !reflect.DeepEqual(three.windows, []confirmedNeedLedgerWindowRecord{{ConfirmedNeedLedgerWindowCarrierPrecedence, two.result.ResultID, string(option.RelativeID)}}) {
		t.Fatalf("ledger window events = %#v, want exactly one carrier_precedence", three.windows)
	}
	entries := memberEntries(three.result, contractsv1.ContextFabricStructureNeedWindow)
	if len(entries) != 1 || entries[0].Source != contractsv1.ContextFabricStructureSourceCarried {
		t.Fatalf("window disclosure = %#v, want exactly one carried entry (the carrier's)", entries)
	}
	if window := three.result.EffectiveEvidenceWindow; window == nil || window.RelativeID != option.RelativeID || !sameWindowBounds(window.Start, option.Start) {
		t.Fatalf("effective window = %#v, want the carried confirmed window", window)
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
	got := axisConflictConfirmedNeeds(remembered)
	want := []ConfirmedNeedEntry{{Member: contractsv1.ContextFabricStructureNeedSubjectCandidate, AppliedKind: SubjectRepository, AppliedValue: "repository:need-r2"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("axisConflictConfirmedNeeds() = %#v, want %#v", got, want)
	}
}

func TestDecideLedgerWindow(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	relative := confirmedStructureMember{Member: contractsv1.ContextFabricStructureNeedWindow, AppliedValue: string(RelativeWindowTrailing90D), WindowStart: &start, WindowEnd: &end}
	absolute := confirmedStructureMember{Member: contractsv1.ContextFabricStructureNeedWindow, AppliedValue: windowAbsoluteAppliedValuePrefix + "1:2", WindowStart: &start, WindowEnd: &end}
	inferred := &contractsv1.ContextFabricEffectiveEvidenceWindow{RelativeID: RelativeWindowTrailing30D, Provenance: WindowInferredDefault}
	stated := &contractsv1.ContextFabricEffectiveEvidenceWindow{RelativeID: RelativeWindowTrailing30D, Provenance: WindowQuestionStated}
	ledger := func(entries ...confirmedStructureMember) confirmedNeedLedgerResult {
		return confirmedNeedLedgerResult{Outcome: ConfirmedNeedLedgerHit, Entries: entries, SourceResultID: "result_need_parent"}
	}
	miss := windowCarryResult{Outcome: WindowCarryMissNoConfirmedWindow}
	hit := windowCarryResult{Outcome: WindowCarryHit, Window: stated}

	if got := decideLedgerWindow(ledger(), inferred, miss); got.Present {
		t.Fatalf("no window entry: got %#v, want not present", got)
	}
	if got := decideLedgerWindow(ledger(confirmedStructureMember{Member: contractsv1.ContextFabricStructureNeedWindow}), inferred, miss); got.Present {
		t.Fatalf("empty-valued entry: got %#v, want not present", got)
	}
	if got := decideLedgerWindow(ledger(relative), inferred, hit); got.Decision != ConfirmedNeedLedgerWindowCarrierPrecedence || got.Applied() || composeLedgerWindowEntry(got) != nil {
		t.Fatalf("carrier hit: got %#v, want carrier_precedence, nothing applied or disclosed", got)
	}
	if got := decideLedgerWindow(ledger(relative), stated, miss); got.Decision != ConfirmedNeedLedgerWindowNotApplicable || got.Applied() {
		t.Fatalf("stated window: got %#v, want not_applicable", got)
	}
	if got := decideLedgerWindow(ledger(relative), nil, miss); got.Decision != ConfirmedNeedLedgerWindowNotApplicable || got.Applied() {
		t.Fatalf("no window axis: got %#v, want not_applicable", got)
	}
	applied := decideLedgerWindow(ledger(relative), inferred, miss)
	if !applied.Applied() || applied.Window.RelativeID != RelativeWindowTrailing90D || applied.Window.Provenance != WindowClarificationConfirmed ||
		!applied.Window.Start.Equal(start) || !applied.Window.End.Equal(end) || applied.Window.Start == relative.WindowStart {
		t.Fatalf("applied: got %#v, want the remembered window rebuilt with copied bounds", applied)
	}
	if entry := composeLedgerWindowEntry(applied); entry == nil || *entry != carriedNeedEntry(contractsv1.ContextFabricStructureNeedWindow, string(RelativeWindowTrailing90D), "result_need_parent") {
		t.Fatalf("applied disclosure = %#v", entry)
	}
	abs := decideLedgerWindow(ledger(absolute), inferred, miss)
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
		{Member: contractsv1.ContextFabricStructureNeedSubjectAnchor, AppliedKind: SubjectProject, AppliedValue: "p", MatchedTermHash: "h"},
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
			entry.MatchedTermHash != original.MatchedTermHash || entry.PatternID != original.PatternID ||
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
	engine.recordConfirmedNeedLedger(context.Background(), acceptancePrincipal(), ledger, applied)
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
		"dropped_members": "subject_anchor:reverify_not_confirmed",
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
		len(confirmedNeedMemberDropReasons()) != 2 || len(confirmedNeedLedgerWindowDecisions()) != 3 {
		t.Fatal("vocabulary membership is not closed")
	}
	if got := observableConfirmedNeedDrops(nil); got != "none" {
		t.Fatalf("observableConfirmedNeedDrops(nil) = %q, want none", got)
	}
	if got := confirmedNeedValueHash(""); got != "" {
		t.Fatalf("confirmedNeedValueHash(\"\") = %q, want empty", got)
	}
}
