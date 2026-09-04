package contextfabric

import (
	"context"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// chainIdentityFixture builds the shape this whole ticket exists for: a turn
// that HAS a conversation but has NOTHING to redeem.
//
// The measured chain is the reason. An offer-driven client re-presents only
// what the latest response still offers. Turn 2 answers the kind question, so
// turn 3 is offered nothing, so turn 3 sends no receipt of any kind -- and a
// request with no receipt names no prior result, so every same-conversation
// carry misses with miss_no_reference and the server asks a question it
// already has the answer to. That miss was measured on three separate
// request_ids on the §5b row.
//
// A receipt cannot express "I am continuing this conversation", because a
// receipt is an ACCEPTANCE of a specific offer and there was nothing to
// accept. parent_result_id is the weaker statement the client can always
// make.
func chainIdentityFixture() (InvestigationRequest, *staticResultStore) {
	priorResult := validInvestigationResult()
	priorResult.ResultID = "result_prior_chain_0001"
	priorResult.ConfirmedStructure = []contractsv1.ContextFabricConfirmedStructureEntry{
		confirmedKindEntry(contractsv1.ContextFabricSubjectTeam, "result_prior_chain_0000", "kindr_confirm0001"),
	}

	request := validInvestigationRequest()
	// The ENTIRE linkage. No receipts of any namespace -- deliberately, since
	// a fixture that also carried a receipt could not tell whether the field
	// or the receipt did the work.
	request.ParentResultID = priorResult.ResultID

	return request, &staticResultStore{results: map[string]InvestigationResult{priorResult.ResultID: priorResult}}
}

// TestChainIdentity_ATurnLinkedOnlyByParentResultIDCarriesTheConfirmedKind is
// the red-first test for the field's whole purpose.
//
// RED at the parent for the RIGHT reason: the field exists on the request
// (so this compiles) but nothing reads it, so carryReferencedResultIDs
// returns an empty frontier and the kind carry reports miss_no_reference --
// which is exactly the telemetry value measured on the live §5b chain.
func TestChainIdentity_ATurnLinkedOnlyByParentResultIDCarriesTheConfirmedKind(t *testing.T) {
	t.Parallel()

	request, store := chainIdentityFixture()
	telemetry := &recordingTelemetry{}
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	graph := &kindRecordingGraphReader{graphReaderStub: graphReaderStub{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
	}}
	freshResult := validInvestigationResult()

	engine := mustReuseTestEngine(t, EngineDependencies{
		Graph: graph,
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return freshResult, nil
		}),
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}}, nil
		}),
		Results:   store,
		Telemetry: telemetry,
	})

	result, err := engine.Investigate(context.Background(), reusePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}

	hit := false
	var outcomes []KindCarryOutcome
	for _, c := range telemetry.kindCarries {
		outcomes = append(outcomes, c.outcome)
		if c.outcome == KindCarryHit {
			hit = true
		}
	}
	if !hit {
		t.Errorf("kind carry outcomes = %v, want a hit: a turn naming its predecessor by parent_result_id must reach that predecessor's confirmed kind -- miss_no_reference here is the measured defect, not a fixture problem", outcomes)
	}

	carried := false
	for _, entry := range result.ConfirmedStructure {
		if entry.Member == contractsv1.ContextFabricStructureNeedExpectedKind &&
			entry.Source == contractsv1.ContextFabricStructureSourceCarried {
			carried = true
			if entry.PriorResultID == "" {
				t.Error("carried expected_kind entry has an empty prior_result_id: the origin must be disclosed, not merely applied")
			}
		}
	}
	if !carried {
		t.Errorf("ConfirmedStructure = %+v, want a source=carried expected_kind entry", result.ConfirmedStructure)
	}

	sawTeam := false
	for _, seen := range graph.seen {
		if seen != nil && seen.Kind == contractsv1.ContextFabricSubjectTeam {
			sawTeam = true
		}
	}
	if !sawTeam {
		t.Errorf("ResolveSubjects saw ConfirmedExpectedKind %+v, want a non-nil team: the carried kind must reach the pool filter, which is what makes the carry change an answer rather than only a disclosure", graph.seen)
	}
}

// TestChainIdentity_ParentResultIDNeverBindsThePriorSubjects is the mandatory
// guard from the design review, and it is the one that would be easiest to
// violate by accident: seeding the carry walk and seeding subject hints are
// one line apart.
//
// Naming a prior result says "this is the turn I follow". It must NOT say
// "and bind whatever subjects that turn committed". A receipt says the
// second thing; this field must never be allowed to, or a caller could
// silently inherit a subject they never chose.
func TestChainIdentity_ParentResultIDNeverBindsThePriorSubjects(t *testing.T) {
	t.Parallel()

	request, store := chainIdentityFixture()
	// Give the parent a committed subject worth stealing.
	prior := store.results[request.ParentResultID]
	prior.SubjectResolution = SubjectResolution{
		Committed: []SubjectRef{{Kind: SubjectTeam, CanonicalID: "team_platform", Label: "Platform"}},
	}
	store.results[request.ParentResultID] = prior

	var seenHints [][]SubjectHint
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	graph := &hintRecordingGraphReader{
		graphReaderStub: graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}}},
		record:          func(h []SubjectHint) { seenHints = append(seenHints, h) },
	}
	freshResult := validInvestigationResult()

	engine := mustReuseTestEngine(t, EngineDependencies{
		Graph: graph,
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return freshResult, nil
		}),
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}}, nil
		}),
		Results: store,
	})

	if _, err := engine.Investigate(context.Background(), reusePrincipal(), request); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}

	if len(seenHints) == 0 {
		t.Fatal("ResolveSubjects was never called, so this test proved nothing about subject hints")
	}
	for _, hints := range seenHints {
		for _, hint := range hints {
			if hint.ID == "team_platform" || hint.Label == "Platform" {
				t.Fatalf("ResolveSubjects received SubjectHint %+v derived from parent_result_id: naming a prior result must seed the CARRIES ONLY, never bind that result's committed subjects into this turn", hint)
			}
		}
	}
}

// hintRecordingGraphReader records the SubjectHints each ResolveSubjects call
// received, so the guard above asserts on the engine's actual input rather
// than on a stub's scripted reply.
type hintRecordingGraphReader struct {
	graphReaderStub
	record func([]SubjectHint)
}

func (g *hintRecordingGraphReader) ResolveSubjects(ctx context.Context, principal storage.Principal, request InvestigationRequest, interpreted InterpretedQuestion, binding ResolvedGraphBinding, confirmedKind *ConfirmedExpectedKind, confirmedAnchor *ConfirmedAnchorSelection, frame *QuestionFrame, scopeAnchorKind SubjectKind) (SubjectResolution, StructureOfferMaterial, CommitBasisSet, CommitDecisionDigestSet, error) {
	g.record(request.RequestedScope.SubjectHints)
	return g.graphReaderStub.ResolveSubjects(ctx, principal, request, interpreted, binding, confirmedKind, confirmedAnchor, frame, scopeAnchorKind)
}

// TestChainIdentity_ParentResultIDJoinsTheReuseBypass pins a guarantee this
// slice gets for FREE, which is exactly why it needs a test: nothing here
// was written to produce it, so nothing would notice if it stopped holding.
//
// The reuse bypass keys on carryReferencedResultIDs -- the carries' own seed
// population -- rather than on a list of fields. Teaching that function to
// read parent_result_id therefore joined the new field to the reuse bypass
// with no second edit. That is the intended payoff of population-keying, but
// an unasserted emergent property is one refactor away from silently
// disappearing, and the failure would be invisible: a turn that names its
// predecessor would be served a stored answer produced before that
// predecessor existed, with no error anywhere.
func TestChainIdentity_ParentResultIDJoinsTheReuseBypass(t *testing.T) {
	t.Parallel()

	request, store := chainIdentityFixture()
	_, candidate := reusableCandidate()
	reuseGateCalls := 0
	telemetry := &recordingTelemetry{}
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	freshResult := validInvestigationResult()

	engine := mustReuseTestEngine(t, EngineDependencies{
		Graph: graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}}},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return freshResult, nil
		}),
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}}, nil
		}),
		Results:   store,
		Telemetry: telemetry,
		ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
			reuseGateCalls++
			return candidate, true, nil
		}),
	})

	result, err := engine.Investigate(context.Background(), reusePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if reuseGateCalls != 0 {
		t.Errorf("ReuseGate was called %d times, want 0: a request naming a prior result may inherit a confirmed axis from it, and every carry runs after the reuse lookup, so it must never consult the cache", reuseGateCalls)
	}
	if result.Reused {
		t.Error("result.Reused = true: a turn naming its predecessor was served an answer produced before that predecessor's axes were confirmed")
	}
	found := false
	for _, reason := range telemetry.answerReuseBypasses {
		if reason == AnswerReuseBypassPriorResultReference {
			found = true
		}
	}
	if !found {
		t.Errorf("bypass reasons = %v, want %q: the bypass must be attributable to the prior-result reference, not merely to have happened", telemetry.answerReuseBypasses, AnswerReuseBypassPriorResultReference)
	}
}

// ancestryRecordingStore records the parentResultID every Save was handed,
// keyed by the result id being saved. Asserting on the Save ARGUMENT rather
// than on a round-tripped Get keeps this a test about the ENGINE (does it
// stamp ancestry on this path?) rather than about a store's persistence.
type ancestryRecordingStore struct {
	*staticResultStore
	saved map[string]string
}

func newAncestryRecordingStore(seed *staticResultStore) *ancestryRecordingStore {
	return &ancestryRecordingStore{staticResultStore: seed, saved: map[string]string{}}
}

func (s *ancestryRecordingStore) Save(ctx context.Context, principal storage.Principal, result InvestigationResult, snap SourceWatermarkSnapshot, epoch RebuildEpoch, axisKey string, retrieval ReuseRetrievalIdentity, prompts ReusePromptVersions, authorities ReuseVersionAuthorities, graphEpoch int64, parentResultID string) error {
	s.saved[result.ResultID] = parentResultID
	return s.staticResultStore.Save(ctx, principal, result, snap, epoch, axisKey, retrieval, prompts, authorities, graphEpoch, parentResultID)
}

// TestChainIdentity_AncestryIsRecordedEvenWhenTheTurnIsVetoed is the design
// review's durability requirement, and it is the part of this ticket most
// likely to be got wrong by building only what the happy path needs.
//
// A request-only parent pointer is not durable chain identity. Ancestry is
// only walkable if EVERY turn recorded its parent -- and several engine paths
// return BEFORE any carry runs: the four pre-carry veto/terminal returns and
// the answer-reuse hit. Those are exactly the paths a reader forgets, because
// no carry happened on them and nothing about a carry is on screen.
//
// The failure they cause is not local. A chain whose MIDDLE turn recorded no
// parent has a hole in it, and every turn after the hole is cut off from
// everything before it -- so a clarification loop that hits one veto in the
// middle loses its whole history, which is precisely the situation where the
// history was worth keeping.
//
// COVERAGE LIMIT, stated rather than left to be discovered: this exercises
// the DECISIVE path and ONE veto path (the structure veto, reached by a kind
// receipt that names a prior result the store does not hold). The other three
// Save-bearing returns pass the same helper at the same position and are NOT
// independently pinned here.
func TestChainIdentity_AncestryIsRecordedEvenWhenTheTurnIsVetoed(t *testing.T) {
	t.Parallel()

	t.Run("decisive path", func(t *testing.T) {
		t.Parallel()
		request, seed := chainIdentityFixture()
		store := newAncestryRecordingStore(seed)
		engine := ancestryTestEngine(t, store)

		result, err := engine.Investigate(context.Background(), reusePrincipal(), request)
		if err != nil {
			t.Fatalf("Investigate() error = %v", err)
		}
		if got := store.saved[result.ResultID]; got != request.ParentResultID {
			t.Fatalf("Save recorded parent %q for %q, want %q", got, result.ResultID, request.ParentResultID)
		}
	})

	t.Run("pre-carry veto path", func(t *testing.T) {
		t.Parallel()
		request, seed := chainIdentityFixture()
		// A kind receipt naming a prior result the store does not hold vetoes
		// the whole request before any carry runs -- the shape that would
		// leave a hole in the chain.
		request.PriorKindReceipts = []BoundSubjectReceipt{{ResultID: "result_absent_00001", ReceiptID: "kindr_absent00001"}}
		store := newAncestryRecordingStore(seed)
		engine := ancestryTestEngine(t, store)

		result, err := engine.Investigate(context.Background(), reusePrincipal(), request)
		if err != nil {
			t.Fatalf("Investigate() error = %v", err)
		}
		// Guard: if this stopped being a veto the assertion below would be
		// testing the decisive path twice and would not know it.
		if result.Status != InvestigationNoMatch {
			t.Fatalf("result.Status = %q, want %q -- the fixture must actually reach the veto path for this test to prove anything", result.Status, InvestigationNoMatch)
		}
		got, saved := store.saved[result.ResultID]
		if !saved {
			t.Fatalf("the vetoed turn %q was never saved, so no ancestry could be recorded for it", result.ResultID)
		}
		if got != request.ParentResultID {
			t.Fatalf("vetoed turn recorded parent %q, want %q: a turn that was vetoed on an unrelated axis still has a real predecessor, and a later turn must be able to walk back THROUGH it", got, request.ParentResultID)
		}
	})
}

func ancestryTestEngine(t *testing.T, store InvestigationResultStore) *Engine {
	t.Helper()
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	freshResult := validInvestigationResult()
	return mustReuseTestEngine(t, EngineDependencies{
		Graph: graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}}},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return freshResult, nil
		}),
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}}, nil
		}),
		Results: store,
	})
}
