package contextfabric

// CHAOS-5788: a subject_anchor member the engine bound to the frame's own
// anchor with no offer ever raised is carried forward exactly as a
// receipt-redeemed one is, distinguished only by its disclosed basis. Every
// scenario drives the REAL Engine.Investigate across two turns, over a real
// children_of_scope frame (unlike confirmed_need_consumers_test.go's own
// harness, which never produces one), through the SAME structure-need ledger
// plumbing every other confirmed-need test exercises.

import (
	"context"
	"reflect"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

var (
	committedAnchorRepo      = SubjectRef{Kind: SubjectRepository, CanonicalID: "repository:committed-anchor", Label: "committed-anchor"}
	committedAnchorRepoOther = SubjectRef{Kind: SubjectRepository, CanonicalID: "repository:committed-anchor-other", Label: "other"}
)

// committedAnchorFrame is the fixed real frame every scenario below
// interprets under: children_of_scope, member kind team, anchor term "a" --
// countingFrame's own shape (membership_cardinality_test.go), reused rather
// than duplicated. ScopeAnchorKind is deliberately left unset on the
// WinningSample: the exact shape a frame-repaired proposal leaves behind
// (frame_repair.go), and the shape decideAnchorPoolKindScope's receipt path
// never rescues (graphrank/chaos5393_anchor_pool.go) -- if this axis did
// nothing, the confirmation turn would have no anchor at all.
func committedAnchorFrame() *QuestionFrame {
	return countingFrame(SubjectTeam)
}

// buildCommittedAnchorEngine wires a real-frame interpreter, a scriptable
// graph (needTurnGraph, confirmed_need_consumers_test.go), and a persistent
// store, so a second Investigate call reads exactly what the first one saved.
func buildCommittedAnchorEngine(t *testing.T) (*Engine, *needTurnGraph, *staticResultStore) {
	t.Helper()
	graph := &needTurnGraph{}
	store := &staticResultStore{results: map[string]InvestigationResult{}, states: map[string]*PersistedSemanticState{}}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: familyInterpreter{
			interpreted: InterpretedQuestion{
				Shape: ShapeOpen, RequestedJudgment: "count", TimeContext: TimeContext{Axis: TemporalCurrent}, FactRequirements: []FactRequirement{},
			},
			outcome: QuestionFamilyOutcome{
				Frame: committedAnchorFrame(), FrameObligations: committedAnchorFrame().Obligations,
				Family: QuestionFamilyScopedCohortStatus, Source: QuestionFamilySourceModel,
				WinningSampleIndex: 0, WinningSample: FamilySample{},
			},
		},
		Graph: graph,
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{Facts: []CanonicalFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}, Version: "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{}}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return validInvestigationResult(), nil
		}),
		Results: store,
	}, EngineOptions{
		ServiceVersion: "chaos5788-anchor-carry-test",
		Now:            func() time.Time { return time.Unix(500, 0).UTC() },
		NewResultID:    resultIDSequence(),
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	return engine, graph, store
}

// resultIDSequence mints result_5788_0001, result_5788_0002, ... in order.
func resultIDSequence() func() string {
	next := 0
	return func() string {
		next++
		ids := []string{"result_5788_0001", "result_5788_0002", "result_5788_0003"}
		if next-1 < len(ids) {
			return ids[next-1]
		}
		return "result_5788_overflow"
	}
}

func committedAnchorTurn(t *testing.T, engine *Engine, graph *needTurnGraph, store *staticResultStore, request InvestigationRequest, response needTurnResponse) (InvestigationResult, needTurnCall) {
	t.Helper()
	graph.response = response
	mark := len(graph.calls)
	store.saved, store.savedSemantic = nil, nil
	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate(%s) error = %v", request.RequestID, err)
	}
	if len(graph.calls) != mark+1 {
		t.Fatalf("fixture defect: turn %s must call ResolveSubjects exactly once (calls=%d)", request.RequestID, len(graph.calls)-mark)
	}
	if store.saved == nil {
		t.Fatalf("fixture defect: turn %s must save exactly once", request.RequestID)
	}
	// staticResultStore.Save only records the LAST save (its own doc
	// comment); a later turn naming this one as parent reads through
	// store.results/states, so the harness copies the save out itself --
	// the SAME step needTurnHarness.turn takes (confirmed_need_consumers_test.go).
	store.results[store.saved.ResultID] = *store.saved
	if store.savedSemantic != nil && store.savedSemantic.State != nil {
		store.states[store.saved.ResultID] = store.savedSemantic.State
	}
	return result, graph.calls[mark]
}

// TestCommittedAnchorSurvivesTheConfirmationTurn is pin (a): the anchor turn
// one committed with an authoritative identity and no offer ever raised is
// captured into turn one's own outgoing ledger, and the confirmation turn --
// which restates only the member kind, never the subject -- still receives
// it as a ConfirmedAnchorSelection. The count decision the confirmation
// turn's own resolution stands on then reads anchor_committed with the
// carried anchor.
func TestCommittedAnchorSurvivesTheConfirmationTurn(t *testing.T) {
	engine, graph, store := buildCommittedAnchorEngine(t)

	one := needTurnRequest("request_5788_committed_one", true)
	oneResponse := needTurnResponse{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{committedAnchorRepo}}, bases: provenCommitBases(committedAnchorRepo)}
	oneResult, _ := committedAnchorTurn(t, engine, graph, store, one, oneResponse)
	oneSaved := store.states[oneResult.ResultID]
	if oneSaved == nil {
		t.Fatalf("fixture defect: turn one must persist a semantic state")
	}
	wantOneEntry := ConfirmedNeedEntry{Member: contractsv1.ContextFabricStructureNeedSubjectAnchor, AppliedKind: committedAnchorRepo.Kind, AppliedValue: committedAnchorRepo.CanonicalID, Basis: ConfirmedNeedBasisEngineCommitted}
	if !reflect.DeepEqual(oneSaved.ConfirmedNeeds, []ConfirmedNeedEntry{wantOneEntry}) {
		t.Fatalf("turn one ledger = %#v, want exactly the engine-committed anchor %#v", oneSaved.ConfirmedNeeds, wantOneEntry)
	}

	two := needTurnRequest("request_5788_committed_two", true)
	two.ExpectedKinds = []contractsv1.ContextFabricSubjectKind{contractsv1.ContextFabricSubjectTeam}
	two = continuingNeedTurn(two, oneResult.ResultID)
	twoResponse := needTurnResponse{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{committedAnchorRepo}}, bases: provenCommitBases(committedAnchorRepo)}
	_, call := committedAnchorTurn(t, engine, graph, store, two, twoResponse)

	wantAnchor := &ConfirmedAnchorSelection{Kind: committedAnchorRepo.Kind, CanonicalID: committedAnchorRepo.CanonicalID}
	if !reflect.DeepEqual(call.anchor, wantAnchor) {
		t.Fatalf("ResolveSubjects anchor on the confirmation turn = %#v, want %#v", call.anchor, wantAnchor)
	}

	scope := DecideCountPopulationScope(committedAnchorFrame(), "", twoResponse.resolution, twoResponse.bases, CohortMemberSourceNotApplicable)
	if scope.Decision != CountPopulationScopeAnchorCommitted || scope.AnchorID != committedAnchorRepo.CanonicalID {
		t.Fatalf("count population scope over the confirmation turn's own resolution = %+v, want anchor_committed on %s", scope, committedAnchorRepo.CanonicalID)
	}
}

// TestUnresolvedAnchorCarriesNothing is pin (b): a turn one whose anchor was
// ambiguous -- two candidates, nothing bound -- carries nothing forward. The
// confirmation turn behaves exactly as it did before this axis existed.
func TestUnresolvedAnchorCarriesNothing(t *testing.T) {
	engine, graph, store := buildCommittedAnchorEngine(t)

	one := needTurnRequest("request_5788_ambiguous_one", true)
	oneResponse := needTurnResponse{
		resolution: SubjectResolution{
			Candidates: []SubjectCandidate{scopeCandidate(committedAnchorRepo, "receipt_5788_a"), scopeCandidate(committedAnchorRepoOther, "receipt_5788_b")},
			Committed:  []SubjectRef{},
		},
	}
	oneResult, _ := committedAnchorTurn(t, engine, graph, store, one, oneResponse)
	if saved := store.states[oneResult.ResultID]; saved != nil {
		for _, entry := range saved.ConfirmedNeeds {
			if entry.Member == contractsv1.ContextFabricStructureNeedSubjectAnchor {
				t.Fatalf("turn one ledger carries subject_anchor = %#v, want none: nothing was bound", entry)
			}
		}
	}

	two := needTurnRequest("request_5788_ambiguous_two", true)
	two.ExpectedKinds = []contractsv1.ContextFabricSubjectKind{contractsv1.ContextFabricSubjectTeam}
	two = continuingNeedTurn(two, oneResult.ResultID)
	_, call := committedAnchorTurn(t, engine, graph, store, two, needTurnResponse{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}}})
	if call.anchor != nil {
		t.Fatalf("ResolveSubjects anchor on the confirmation turn = %#v, want nil: turn one bound nothing", call.anchor)
	}
}

// TestCommittedAnchorDisclosesItsBasis is pin (d): the confirmation turn's
// own carried-structure disclosure names the carried anchor with the
// engine_committed provenance, never the clarification_confirmed token a
// caller-picked offer would carry.
func TestCommittedAnchorDisclosesItsBasis(t *testing.T) {
	engine, graph, store := buildCommittedAnchorEngine(t)

	one := needTurnRequest("request_5788_disclose_one", true)
	oneResponse := needTurnResponse{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{committedAnchorRepo}}, bases: provenCommitBases(committedAnchorRepo)}
	oneResult, _ := committedAnchorTurn(t, engine, graph, store, one, oneResponse)

	two := needTurnRequest("request_5788_disclose_two", true)
	two.ExpectedKinds = []contractsv1.ContextFabricSubjectKind{contractsv1.ContextFabricSubjectTeam}
	two = continuingNeedTurn(two, oneResult.ResultID)
	twoResult, _ := committedAnchorTurn(t, engine, graph, store, two, needTurnResponse{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{committedAnchorRepo}}, bases: provenCommitBases(committedAnchorRepo)})

	got := memberEntries(twoResult, contractsv1.ContextFabricStructureNeedSubjectAnchor)
	if len(got) != 1 {
		t.Fatalf("turn two subject_anchor disclosure = %#v, want exactly one entry", got)
	}
	if got[0].Provenance != contractsv1.ContextFabricStructureEngineCommitted || got[0].Source != contractsv1.ContextFabricStructureSourceCarried || got[0].AppliedValue != committedAnchorRepo.CanonicalID {
		t.Fatalf("turn two subject_anchor disclosure = %+v, want provenance engine_committed, source carried, value %s", got[0], committedAnchorRepo.CanonicalID)
	}
}

// TestEngineCommittedAnchorForCaptureCoversItsInputDomain enumerates the
// capture gate's own input domain: it must fire on exactly the resolutions
// anchorBound already admits (DecideCountPopulationScope, count_population_scope_test.go's
// own domain table), and never on any other shape.
func TestEngineCommittedAnchorForCaptureCoversItsInputDomain(t *testing.T) {
	t.Parallel()
	frame := committedAnchorFrame()
	member := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:M", Label: "m"}
	anchor := scopeAnchorRepository()
	other := SubjectRef{Kind: SubjectRepository, CanonicalID: "repository:OTHER", Label: "other"}
	for _, row := range []struct {
		name       string
		frame      *QuestionFrame
		resolution SubjectResolution
		bases      CommitBasisSet
		wantOK     bool
	}{
		{"no frame", nil, SubjectResolution{Committed: []SubjectRef{anchor}, Candidates: []SubjectCandidate{scopeAnchorMatch(anchor)}}, nil, false},
		{"nothing committed", frame, SubjectResolution{}, nil, false},
		{"member kind committed, not the anchor", frame, SubjectResolution{Committed: []SubjectRef{member}}, nil, false},
		{"unrelated committed subject, no term match", frame, SubjectResolution{Committed: []SubjectRef{other}}, nil, false},
		{"ambiguous: two candidates, nothing committed", frame, SubjectResolution{Candidates: []SubjectCandidate{scopeCandidate(anchor, "r1"), scopeCandidate(other, "r2")}}, nil, false},
		{"statistical basis alone, no term match", frame, SubjectResolution{Committed: []SubjectRef{anchor}}, CommitBasisSet{SubjectMapKey(anchor): CommitBasisStatistical}, false},
		{"authoritative identity basis alone, no term match", frame, SubjectResolution{Committed: []SubjectRef{anchor}}, CommitBasisSet{SubjectMapKey(anchor): CommitBasisAuthoritativeIdentity}, false},
		{"caller canonical id basis", frame, SubjectResolution{Committed: []SubjectRef{anchor}}, CommitBasisSet{SubjectMapKey(anchor): CommitBasisCallerCanonicalID}, true},
		{"authoritative identity basis with a matching candidate term", frame, SubjectResolution{Committed: []SubjectRef{anchor}, Candidates: []SubjectCandidate{scopeAnchorMatch(anchor)}}, CommitBasisSet{SubjectMapKey(anchor): CommitBasisAuthoritativeIdentity}, true},
		{"bound anchor beside an unrelated unbound subject", frame, SubjectResolution{Committed: []SubjectRef{other, anchor}, Candidates: []SubjectCandidate{scopeAnchorMatch(anchor)}}, nil, true},
	} {
		row := row
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			got, ok := engineCommittedAnchorForCapture(row.frame, "", row.resolution, row.bases)
			if ok != row.wantOK {
				t.Fatalf("ok = %t, want %t (member %#v)", ok, row.wantOK, got)
			}
			if !ok {
				return
			}
			if got.Member != contractsv1.ContextFabricStructureNeedSubjectAnchor || got.AppliedKind != anchor.Kind || got.AppliedValue != anchor.CanonicalID || got.Basis != ConfirmedNeedBasisEngineCommitted {
				t.Fatalf("member = %#v, want subject_anchor/%s/%s/engine_committed", got, anchor.Kind, anchor.CanonicalID)
			}
		})
	}
}

// TestConfirmedNeedsForCaptureWithCommittedAnchorRespectsAlreadyStated pins
// the precedence rule: a caller's own receipt or explicit field for
// subject_anchor THIS turn always wins over a subject the engine merely
// resolved to -- the engine-committed member is never even considered when
// alreadyStated is true.
func TestConfirmedNeedsForCaptureWithCommittedAnchorRespectsAlreadyStated(t *testing.T) {
	t.Parallel()
	request := needTurnRequest("request_5788_precedence", true)
	member := confirmedStructureMember{Member: contractsv1.ContextFabricStructureNeedSubjectAnchor, AppliedKind: committedAnchorRepo.Kind, AppliedValue: committedAnchorRepo.CanonicalID, Basis: ConfirmedNeedBasisEngineCommitted}
	base := []ConfirmedNeedEntry{{Member: contractsv1.ContextFabricStructureNeedExpectedKind, AppliedValue: string(SubjectTeam)}}

	t.Run("ok=false is a no-op", func(t *testing.T) {
		t.Parallel()
		got := confirmedNeedsForCaptureWithCommittedAnchor(nil, nil, request, base, confirmedStructureMember{}, false)
		if !reflect.DeepEqual(got, base) {
			t.Fatalf("got %#v, want base %#v unchanged", got, base)
		}
	})
	t.Run("a receipt-confirmed subject_anchor this turn wins over the engine-committed one", func(t *testing.T) {
		t.Parallel()
		receiptConfirmed := confirmedStructureMember{Member: contractsv1.ContextFabricStructureNeedSubjectAnchor, AppliedKind: SubjectTeam, AppliedValue: "team:caller_picked", ReceiptID: "ancr_x"}
		// base already carries this turn's receipt confirmation -- the SAME
		// value Investigate's own pre-resolution ledger computation
		// (engine.go, confirmedNeedsForCapture) would have produced from the
		// identical confirmedThisTurn. The function under test must return it
		// UNCHANGED: alreadyStated is the signal that base already has the
		// right content, never a cue to recompute anything.
		baseWithReceipt := mergeConfirmedNeedsLedger(nil, []confirmedStructureMember{receiptConfirmed}, request)
		got := confirmedNeedsForCaptureWithCommittedAnchor(nil, []confirmedStructureMember{receiptConfirmed}, request, baseWithReceipt, member, true)
		if !reflect.DeepEqual(got, baseWithReceipt) {
			t.Fatalf("got %#v, want %#v unchanged: the caller's own receipt, never the engine-committed member", got, baseWithReceipt)
		}
	})
	t.Run("ok=true with nothing else stated folds the engine-committed anchor in", func(t *testing.T) {
		t.Parallel()
		got := confirmedNeedsForCaptureWithCommittedAnchor(nil, nil, request, base, member, true)
		want := []ConfirmedNeedEntry{{Member: contractsv1.ContextFabricStructureNeedSubjectAnchor, AppliedKind: committedAnchorRepo.Kind, AppliedValue: committedAnchorRepo.CanonicalID, Basis: ConfirmedNeedBasisEngineCommitted}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %#v, want %#v", got, want)
		}
	})
}

// TestValidConfirmedNeedBasisAndProvenanceMapping enumerates the closed
// vocabulary and its wire rendering.
func TestValidConfirmedNeedBasisAndProvenanceMapping(t *testing.T) {
	t.Parallel()
	for _, row := range []struct {
		basis          ConfirmedNeedBasis
		wantValid      bool
		wantProvenance contractsv1.ContextFabricStructureProvenance
	}{
		{ConfirmedNeedBasisConfirmed, true, contractsv1.ContextFabricStructureClarificationConfirmed},
		{ConfirmedNeedBasisEngineCommitted, true, contractsv1.ContextFabricStructureEngineCommitted},
		{ConfirmedNeedBasis("not_a_basis"), false, contractsv1.ContextFabricStructureClarificationConfirmed},
	} {
		if got := ValidConfirmedNeedBasis(row.basis); got != row.wantValid {
			t.Errorf("ValidConfirmedNeedBasis(%q) = %t, want %t", row.basis, got, row.wantValid)
		}
		if got := provenanceForConfirmedNeedBasis(row.basis); got != row.wantProvenance {
			t.Errorf("provenanceForConfirmedNeedBasis(%q) = %q, want %q", row.basis, got, row.wantProvenance)
		}
	}
}
