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
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/hintsource"
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
	engine, graph, store, _ := buildCommittedAnchorEngineWithTelemetry(t)
	return engine, graph, store
}

// buildCommittedAnchorEngineWithTelemetry is buildCommittedAnchorEngine's own
// twin for a test that must also read the deferred confirmed-need-ledger
// Info line's own recorded event.
func buildCommittedAnchorEngineWithTelemetry(t *testing.T) (*Engine, *needTurnGraph, *staticResultStore, *recordingTelemetry) {
	t.Helper()
	graph := &needTurnGraph{}
	store := &staticResultStore{results: map[string]InvestigationResult{}, states: map[string]*PersistedSemanticState{}}
	telemetry := &recordingTelemetry{}
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
		Results:   store,
		Telemetry: telemetry,
	}, EngineOptions{
		ServiceVersion: "chaos5788-anchor-carry-test",
		Now:            func() time.Time { return time.Unix(500, 0).UTC() },
		NewResultID:    resultIDSequence(),
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	return engine, graph, store, telemetry
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

	// The BINDING half: the confirmation turn's own request to ResolveSubjects
	// carries the carried anchor as a SubjectHint sourced from
	// hintsource.EngineCommittedAnchorCarry -- the channel that reaches
	// resolution's own caller-hint exact-commit exit and lets a same-kind
	// statistical decoy never outrank a proven carry (graphrank's own
	// TestEngineCommittedAnchorCarryHintShortCircuitsWithProvenBasis proves
	// what that channel does with the hint once it arrives; this proves the
	// engine actually sends it).
	wantHint := SubjectHint{Kind: committedAnchorRepo.Kind, ID: committedAnchorRepo.CanonicalID, Label: committedAnchorRepo.CanonicalID, Source: string(hintsource.EngineCommittedAnchorCarry)}
	hintSent := false
	for _, hint := range call.request.RequestedScope.SubjectHints {
		if hint == wantHint {
			hintSent = true
			break
		}
	}
	if !hintSent {
		t.Fatalf("ResolveSubjects request hints = %#v, want %#v among them", call.request.RequestedScope.SubjectHints, wantHint)
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
		{"bound anchor beside an unrelated unbound subject", frame, SubjectResolution{Committed: []SubjectRef{other, anchor}, Candidates: []SubjectCandidate{scopeAnchorMatch(anchor)}}, CommitBasisSet{SubjectMapKey(anchor): CommitBasisAuthoritativeIdentity}, true},
		// The exact shape a mislabeled capture would produce: a matching
		// candidate TERM alone never proves identity (a statistical/exact-label
		// commit can echo the anchor's own wording just as readily as a proven
		// one), so a term match must never substitute for CommitBasis.
		{"statistical basis with a matching candidate term", frame, SubjectResolution{Committed: []SubjectRef{anchor}, Candidates: []SubjectCandidate{scopeAnchorMatch(anchor)}}, CommitBasisSet{SubjectMapKey(anchor): CommitBasisStatistical}, false},
		{"no basis recorded at all, with a matching candidate term", frame, SubjectResolution{Committed: []SubjectRef{anchor}, Candidates: []SubjectCandidate{scopeAnchorMatch(anchor)}}, nil, false},
	} {
		row := row
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			got, ok, _ := engineCommittedAnchorForCapture(row.frame, "", row.resolution, row.bases)
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

// TestCarriedAnchorAgreementForCoversItsInputDomain enumerates
// carriedAnchorAgreementFor's own domain: whether a subject_anchor entry
// applied this turn at all, crossed with what this turn's own resolution
// committed (nothing, the same subject, a different subject of the same
// kind, or only a different kind entirely).
func TestCarriedAnchorAgreementForCoversItsInputDomain(t *testing.T) {
	t.Parallel()
	entry := confirmedStructureMember{Member: contractsv1.ContextFabricStructureNeedSubjectAnchor, AppliedKind: committedAnchorRepo.Kind, AppliedValue: committedAnchorRepo.CanonicalID, Basis: ConfirmedNeedBasisEngineCommitted}
	applied := func(has bool) map[contractsv1.ContextFabricStructureNeedKind]confirmedStructureMember {
		if !has {
			return nil
		}
		return map[contractsv1.ContextFabricStructureNeedKind]confirmedStructureMember{contractsv1.ContextFabricStructureNeedSubjectAnchor: entry}
	}
	teamOther := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:unrelated-kind", Label: "unrelated kind"}
	for _, row := range []struct {
		name        string
		hasEntry    bool
		resolution  SubjectResolution
		bases       CommitBasisSet
		wantAgree   ConfirmedAnchorAgreement
		wantDropped bool
		wantEntryOK bool
	}{
		{"nothing applied this turn", false, SubjectResolution{Committed: []SubjectRef{committedAnchorRepoOther}}, nil, ConfirmedAnchorAgreementNotApplicable, false, false},
		// R3: an entry with nothing of its own kind committed this turn is
		// DROPPED (vetoed_unresolved), never left to ride an untouched
		// ledger into a later turn as if this turn's own resolution had
		// stood behind it.
		{"applied, nothing committed this turn", true, SubjectResolution{}, nil, ConfirmedAnchorAgreementAbsent, true, true},
		{"applied, only a different kind committed", true, SubjectResolution{Committed: []SubjectRef{teamOther}}, nil, ConfirmedAnchorAgreementAbsent, true, true},
		{"applied, resolution committed the SAME subject", true, SubjectResolution{Committed: []SubjectRef{committedAnchorRepo}}, nil, ConfirmedAnchorAgreementAgree, false, true},
		{"applied, resolution committed a DIFFERENT subject of the same kind on an identity-proven basis", true, SubjectResolution{Committed: []SubjectRef{committedAnchorRepoOther}}, CommitBasisSet{SubjectMapKey(committedAnchorRepoOther): CommitBasisCallerCanonicalID}, ConfirmedAnchorAgreementDisagree, true, true},
		// R4, the exact bug this domain table now pins: resolution committed
		// BOTH the carried id AND a distinct proven commit of the same kind
		// -- a caller hint and the engine carry co-committing. A same-id
		// match found before the distinct proven commit in the slice must
		// never short-circuit to Agree: the distinct proven commit beside it
		// is what decides, whatever the iteration order.
		{"applied, resolution committed the SAME subject AND a distinct proven commit of the same kind", true, SubjectResolution{Committed: []SubjectRef{committedAnchorRepo, committedAnchorRepoOther}}, CommitBasisSet{SubjectMapKey(committedAnchorRepoOther): CommitBasisCallerCanonicalID}, ConfirmedAnchorAgreementDisagree, true, true},
		// The exact gap a mislabeled veto would reopen: a same-kind,
		// different-id STATISTICAL commit is never a conflict -- anchorBound
		// itself refuses to bind such a commit, so it must not evict a
		// genuinely proven carry either. Nothing IDENTITY-PROVEN of the
		// entry's own kind committed, so this still drops as absent.
		{"applied, resolution committed a DIFFERENT subject of the same kind on a statistical basis", true, SubjectResolution{Committed: []SubjectRef{committedAnchorRepoOther}}, CommitBasisSet{SubjectMapKey(committedAnchorRepoOther): CommitBasisStatistical}, ConfirmedAnchorAgreementAbsent, true, true},
		{"applied, resolution committed a DIFFERENT subject of the same kind with no basis recorded at all", true, SubjectResolution{Committed: []SubjectRef{committedAnchorRepoOther}}, nil, ConfirmedAnchorAgreementAbsent, true, true},
		{"applied, resolution committed both the same subject and an unrelated one", true, SubjectResolution{Committed: []SubjectRef{teamOther, committedAnchorRepo}}, nil, ConfirmedAnchorAgreementAgree, false, true},
	} {
		row := row
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			gotAgreement, gotEntry, gotDropped := carriedAnchorAgreementFor(applied(row.hasEntry), row.resolution, row.bases)
			if gotAgreement != row.wantAgree || gotDropped != row.wantDropped {
				t.Fatalf("carriedAnchorAgreementFor() = (%q, dropped=%t), want (%q, dropped=%t)", gotAgreement, gotDropped, row.wantAgree, row.wantDropped)
			}
			if row.wantEntryOK && gotEntry != entry {
				t.Fatalf("entry = %#v, want %#v", gotEntry, entry)
			}
			if !row.wantEntryOK && gotEntry != (confirmedStructureMember{}) {
				t.Fatalf("entry = %#v, want the zero value", gotEntry)
			}
		})
	}
}

// TestAnchorLedgerDispositionCoversItsInputDomain pins the wire disposition
// each (agreement, superseded) pair renders -- the SAME mapping the
// carried-structure echo and the ledger's own Info line both read, so
// neither can disagree with the other about what happened to this turn's
// applied subject_anchor entry.
func TestAnchorLedgerDispositionCoversItsInputDomain(t *testing.T) {
	t.Parallel()
	for _, row := range []struct {
		agreement  ConfirmedAnchorAgreement
		superseded bool
		want       contractsv1.ContextFabricStructureDisposition
	}{
		{ConfirmedAnchorAgreementNotApplicable, false, ""},
		{ConfirmedAnchorAgreementAgree, false, contractsv1.ContextFabricStructureDispositionApplied},
		{ConfirmedAnchorAgreementAbsent, false, contractsv1.ContextFabricStructureDispositionVetoedUnresolved},
		{ConfirmedAnchorAgreementDisagree, false, contractsv1.ContextFabricStructureDispositionVetoedConflict},
		// superseded wins even over an agreement value that could never
		// actually co-occur with it in practice (the entry is removed from
		// the ledger before resolution runs, so the post-resolution check
		// always reads not_applicable for a superseded entry) -- pinned
		// anyway, because the function's own contract is "superseded always
		// wins," not "superseded happens to win given today's one caller."
		{ConfirmedAnchorAgreementNotApplicable, true, contractsv1.ContextFabricStructureDispositionSupersededByCaller},
		{ConfirmedAnchorAgreementAgree, true, contractsv1.ContextFabricStructureDispositionSupersededByCaller},
	} {
		if got := anchorLedgerDisposition(row.agreement, row.superseded); got != row.want {
			t.Errorf("anchorLedgerDisposition(%q, %t) = %q, want %q", row.agreement, row.superseded, got, row.want)
		}
	}
}

// TestCallerSuppliedHintOfKind pins the contest check's own domain: an empty
// set, a set with no matching kind, and a set with a matching kind whatever
// its source or id.
func TestCallerSuppliedHintOfKind(t *testing.T) {
	t.Parallel()
	repo := contractsv1.ContextFabricSubjectKind("repository")
	team := contractsv1.ContextFabricSubjectKind("team")
	for _, row := range []struct {
		name  string
		hints []contractsv1.ContextFabricSubjectHint
		kind  contractsv1.ContextFabricSubjectKind
		want  bool
	}{
		{"empty set", nil, repo, false},
		{"no matching kind", []contractsv1.ContextFabricSubjectHint{{Kind: team, ID: "team:x", Source: "caller"}}, repo, false},
		{"matching kind, caller source", []contractsv1.ContextFabricSubjectHint{{Kind: repo, ID: "repository:x", Source: "caller"}}, repo, true},
		{"matching kind, engine-minted source", []contractsv1.ContextFabricSubjectHint{{Kind: repo, ID: "repository:x", Source: string(hintsource.PriorSubjectReceipt)}}, repo, true},
		{"matching kind among several", []contractsv1.ContextFabricSubjectHint{{Kind: team, ID: "team:x", Source: "caller"}, {Kind: repo, ID: "repository:y", Source: "caller"}}, repo, true},
	} {
		if got := callerSuppliedHintOfKind(row.hints, row.kind); got != row.want {
			t.Errorf("%s: callerSuppliedHintOfKind() = %t, want %t", row.name, got, row.want)
		}
	}
}

// TestCarriedStructureEntriesForDecisiveVetoesOnlyTheDisagreeingAnchor pins
// the disclosure-side rewrite: not-vetoed is a byte-identical no-op, and a
// veto replaces ONLY the matching subject_anchor entry's disposition,
// leaving every other entry (including a different subject_anchor value, an
// impossible but defensive case) untouched.
func TestCarriedStructureEntriesForDecisiveVetoesOnlyTheDisagreeingAnchor(t *testing.T) {
	t.Parallel()
	anchorEntry := &contractsv1.ContextFabricConfirmedStructureEntry{
		Member: contractsv1.ContextFabricStructureNeedSubjectAnchor, AppliedValue: committedAnchorRepo.CanonicalID,
		Source: contractsv1.ContextFabricStructureSourceCarried, PriorResultID: "result_parent",
		Provenance: contractsv1.ContextFabricStructureEngineCommitted, Disposition: contractsv1.ContextFabricStructureDispositionApplied,
	}
	kindEntry := &contractsv1.ContextFabricConfirmedStructureEntry{
		Member: contractsv1.ContextFabricStructureNeedExpectedKind, AppliedValue: string(SubjectTeam),
		Source: contractsv1.ContextFabricStructureSourceCarried, PriorResultID: "result_parent",
		Provenance: contractsv1.ContextFabricStructureClarificationConfirmed, Disposition: contractsv1.ContextFabricStructureDispositionApplied,
	}
	entries := []*contractsv1.ContextFabricConfirmedStructureEntry{anchorEntry, kindEntry, nil}
	vetoedEntry := confirmedStructureMember{AppliedValue: committedAnchorRepo.CanonicalID}

	t.Run("not dropped is a no-op returning the identical slice", func(t *testing.T) {
		t.Parallel()
		got := carriedStructureEntriesForDecisive(entries, vetoedEntry, false, contractsv1.ContextFabricStructureDispositionVetoedConflict)
		if &got[0] != &entries[0] || got[0] != anchorEntry {
			t.Fatalf("not-dropped must return entries unchanged, not a copy")
		}
	})
	t.Run("dropped replaces only the matching anchor entry's disposition", func(t *testing.T) {
		t.Parallel()
		got := carriedStructureEntriesForDecisive(entries, vetoedEntry, true, contractsv1.ContextFabricStructureDispositionVetoedConflict)
		if got[0] == anchorEntry || got[0].Disposition != contractsv1.ContextFabricStructureDispositionVetoedConflict {
			t.Fatalf("anchor entry = %#v, want a NEW pointer with disposition vetoed_conflict", got[0])
		}
		if got[0].Member != anchorEntry.Member || got[0].AppliedValue != anchorEntry.AppliedValue || got[0].Provenance != anchorEntry.Provenance {
			t.Fatalf("anchor entry = %#v, want every other field unchanged from %#v", got[0], anchorEntry)
		}
		if got[1] != kindEntry {
			t.Fatalf("expected_kind entry = %#v, want the untouched original pointer %#v", got[1], kindEntry)
		}
		if got[2] != nil {
			t.Fatalf("nil entry = %#v, want nil preserved", got[2])
		}
	})
	t.Run("dropped renders the disposition it is given, not a fixed one", func(t *testing.T) {
		t.Parallel()
		got := carriedStructureEntriesForDecisive(entries, vetoedEntry, true, contractsv1.ContextFabricStructureDispositionSupersededByCaller)
		if got[0].Disposition != contractsv1.ContextFabricStructureDispositionSupersededByCaller {
			t.Fatalf("anchor entry disposition = %q, want superseded_by_caller", got[0].Disposition)
		}
	})
}

// TestConfirmedNeedsForCaptureWithoutVetoedAnchorDropsOnlyOnVeto pins the
// outgoing-ledger side: not-vetoed returns entries unchanged, vetoed drops
// exactly the subject_anchor member and nothing else.
func TestConfirmedNeedsForCaptureWithoutVetoedAnchorDropsOnlyOnVeto(t *testing.T) {
	t.Parallel()
	entries := []ConfirmedNeedEntry{
		{Member: contractsv1.ContextFabricStructureNeedExpectedKind, AppliedValue: string(SubjectTeam)},
		{Member: contractsv1.ContextFabricStructureNeedSubjectAnchor, AppliedKind: committedAnchorRepo.Kind, AppliedValue: committedAnchorRepo.CanonicalID, Basis: ConfirmedNeedBasisEngineCommitted},
	}
	if got := confirmedNeedsForCaptureWithoutVetoedAnchor(entries, false); !reflect.DeepEqual(got, entries) {
		t.Fatalf("not-vetoed: got %#v, want entries unchanged", got)
	}
	want := []ConfirmedNeedEntry{{Member: contractsv1.ContextFabricStructureNeedExpectedKind, AppliedValue: string(SubjectTeam)}}
	if got := confirmedNeedsForCaptureWithoutVetoedAnchor(entries, true); !reflect.DeepEqual(got, want) {
		t.Fatalf("vetoed: got %#v, want %#v (subject_anchor dropped, expected_kind kept)", got, want)
	}
}

// TestConfirmationTurnNeverDisclosesACarriedAnchorItsOwnResolutionDisowned is
// the end-to-end pin for the disagreement veto: turn one commits an
// engine-committed anchor with no offer ever raised; turn two's own
// resolution independently commits a DIFFERENT repository of the same kind
// on an identity-proven basis -- a genuine conflict, never a mere score
// comparison (TestConfirmationTurnStatisticalRescueNeverVetoesACarriedAnchor
// pins the OTHER shape: a same-kind statistical commit never reaches this
// veto at all). The served document must never disclose the CARRIED
// repository as applied while its own resolution answered about another --
// the disclosure is vetoed, and the stale anchor does not reach a third turn
// either.
func TestConfirmationTurnNeverDisclosesACarriedAnchorItsOwnResolutionDisowned(t *testing.T) {
	engine, graph, store := buildCommittedAnchorEngine(t)

	one := needTurnRequest("request_5788_conflict_one", true)
	oneResponse := needTurnResponse{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{committedAnchorRepo}}, bases: provenCommitBases(committedAnchorRepo)}
	oneResult, _ := committedAnchorTurn(t, engine, graph, store, one, oneResponse)

	two := needTurnRequest("request_5788_conflict_two", true)
	two.ExpectedKinds = []contractsv1.ContextFabricSubjectKind{contractsv1.ContextFabricSubjectTeam}
	two = continuingNeedTurn(two, oneResult.ResultID)
	twoResponse := needTurnResponse{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{committedAnchorRepoOther}},
		bases:      CommitBasisSet{SubjectMapKey(committedAnchorRepoOther): CommitBasisCallerCanonicalID},
	}
	twoResult, _ := committedAnchorTurn(t, engine, graph, store, two, twoResponse)

	got := memberEntries(twoResult, contractsv1.ContextFabricStructureNeedSubjectAnchor)
	if len(got) != 1 {
		t.Fatalf("turn two subject_anchor disclosure = %#v, want exactly one entry", got)
	}
	if got[0].Disposition != contractsv1.ContextFabricStructureDispositionVetoedConflict {
		t.Fatalf("disclosure = %+v, want disposition vetoed_conflict: turn two's own resolution disowned the carried repository", got[0])
	}
	if got[0].AppliedValue == committedAnchorRepoOther.CanonicalID {
		t.Fatalf("disclosure = %+v, must echo the CARRIED (stale) value, not the newly-resolved one, so the veto is legible against what was carried", got[0])
	}

	twoSaved := store.states[twoResult.ResultID]
	if twoSaved == nil {
		t.Fatalf("fixture defect: turn two must persist a semantic state")
	}
	for _, entry := range twoSaved.ConfirmedNeeds {
		if entry.Member == contractsv1.ContextFabricStructureNeedSubjectAnchor {
			t.Fatalf("turn two's outgoing ledger carries subject_anchor = %#v, want none: a proven disagreement must not reach a third turn", entry)
		}
	}
}

// TestVetoedTurnsLedgerLineReadsThePostVetoLedger pins the deferred
// confirmed-need-ledger Info line's own closure: on a turn whose carried
// subject_anchor is vetoed, the line's applied_anchor_kind/
// applied_anchor_value_hash must read the FINAL, post-veto ledger -- never
// the stale (pre-resolution) entry the veto just disowned, which the line's
// own anchor_agreement=disagree would otherwise contradict in the same
// breath.
func TestVetoedTurnsLedgerLineReadsThePostVetoLedger(t *testing.T) {
	engine, graph, store, telemetry := buildCommittedAnchorEngineWithTelemetry(t)

	one := needTurnRequest("request_5788_ledger_veto_one", true)
	oneResponse := needTurnResponse{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{committedAnchorRepo}}, bases: provenCommitBases(committedAnchorRepo)}
	oneResult, _ := committedAnchorTurn(t, engine, graph, store, one, oneResponse)

	two := needTurnRequest("request_5788_ledger_veto_two", true)
	two.ExpectedKinds = []contractsv1.ContextFabricSubjectKind{contractsv1.ContextFabricSubjectTeam}
	two = continuingNeedTurn(two, oneResult.ResultID)
	mark := len(telemetry.confirmedNeedLedgers)
	twoResponse := needTurnResponse{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{committedAnchorRepoOther}},
		bases:      CommitBasisSet{SubjectMapKey(committedAnchorRepoOther): CommitBasisCallerCanonicalID},
	}
	committedAnchorTurn(t, engine, graph, store, two, twoResponse)

	events := telemetry.confirmedNeedLedgers[mark:]
	if len(events) != 1 {
		t.Fatalf("confirmed-need-ledger events for turn two = %d, want exactly 1: %#v", len(events), events)
	}
	event := events[0]
	if event.AnchorAgreement != ConfirmedAnchorAgreementDisagree {
		t.Fatalf("event.AnchorAgreement = %q, want %q", event.AnchorAgreement, ConfirmedAnchorAgreementDisagree)
	}
	if event.AppliedAnchorKind != "" || event.AppliedAnchorValueHash != "" {
		t.Fatalf("event = %+v, want AppliedAnchorKind/AppliedAnchorValueHash both empty -- a vetoed entry must never read as applied on the SAME line that reports it disagreed", event)
	}
	for _, member := range event.AppliedMembers {
		if member == contractsv1.ContextFabricStructureNeedSubjectAnchor {
			t.Fatalf("event.AppliedMembers = %v, must not list subject_anchor for a vetoed turn", event.AppliedMembers)
		}
	}
}

// TestAbsentCarryIsDroppedNotDisclosedAsApplied is the Absent sibling of
// TestVetoedTurnsLedgerLineReadsThePostVetoLedger: a carry whose kind this
// turn's own resolution never touches at all -- not a conflict, nothing to
// agree or disagree with -- is dropped exactly as a genuine conflict is,
// never left to ride an untouched ledger into a later turn as though this
// turn had stood behind it.
func TestAbsentCarryIsDroppedNotDisclosedAsApplied(t *testing.T) {
	engine, graph, store, telemetry := buildCommittedAnchorEngineWithTelemetry(t)

	one := needTurnRequest("request_5788_absent_one", true)
	oneResponse := needTurnResponse{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{committedAnchorRepo}}, bases: provenCommitBases(committedAnchorRepo)}
	oneResult, _ := committedAnchorTurn(t, engine, graph, store, one, oneResponse)

	two := needTurnRequest("request_5788_absent_two", true)
	two.ExpectedKinds = []contractsv1.ContextFabricSubjectKind{contractsv1.ContextFabricSubjectTeam}
	two = continuingNeedTurn(two, oneResult.ResultID)
	mark := len(telemetry.confirmedNeedLedgers)
	// This turn's own resolution commits only the MEMBER kind (team) --
	// nothing of the carried anchor's own kind (repository) at all, so
	// there is nothing to agree or disagree with. Committing the member
	// kind (rather than nothing) keeps this turn on the ordinary decisive
	// path rather than the zero-subjects terminal, which is the shape a
	// real confirmation turn actually has.
	memberOnly := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:absent-turn-member"}
	twoResponse := needTurnResponse{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{memberOnly}}}
	twoResult, _ := committedAnchorTurn(t, engine, graph, store, two, twoResponse)

	events := telemetry.confirmedNeedLedgers[mark:]
	if len(events) != 1 {
		t.Fatalf("confirmed-need-ledger events for turn two = %d, want exactly 1: %#v", len(events), events)
	}
	event := events[0]
	if event.AnchorAgreement != ConfirmedAnchorAgreementAbsent {
		t.Fatalf("event.AnchorAgreement = %q, want %q", event.AnchorAgreement, ConfirmedAnchorAgreementAbsent)
	}
	if event.AnchorDisposition != contractsv1.ContextFabricStructureDispositionVetoedUnresolved {
		t.Fatalf("event.AnchorDisposition = %q, want vetoed_unresolved", event.AnchorDisposition)
	}
	if event.AppliedAnchorKind != "" || event.AppliedAnchorValueHash != "" {
		t.Fatalf("event = %+v, want AppliedAnchorKind/AppliedAnchorValueHash both empty -- an absent entry must never read as applied", event)
	}
	for _, member := range event.AppliedMembers {
		if member == contractsv1.ContextFabricStructureNeedSubjectAnchor {
			t.Fatalf("event.AppliedMembers = %v, must not list subject_anchor for an absent turn", event.AppliedMembers)
		}
	}
	twoSaved := store.states[twoResult.ResultID]
	if twoSaved == nil {
		t.Fatalf("fixture defect: turn two must persist a semantic state")
	}
	for _, entry := range twoSaved.ConfirmedNeeds {
		if entry.Member == contractsv1.ContextFabricStructureNeedSubjectAnchor {
			t.Fatalf("turn two ledger carries subject_anchor = %#v, want none: nothing of its kind was committed this turn", entry)
		}
	}
}

// TestCallerHintOfTheSameKindSupersedesTheEngineCommittedCarry is R2's own
// end-to-end proof: a caller redeeming a PRIOR OFFER for a subject of the
// carry's own kind (the ordinary way a caller-sourced hint reaches
// resolve.go's caller-hint exact-commit channel, hintsource.PriorSubjectReceipt
// -- SemanticRequestIdentityOf never digests PriorSubjectReceipts, unlike
// RequestedScope.SubjectHints, so this is the shape that reaches the carry
// still applied rather than dropping the whole remembered ledger as a
// changed question) means the engine's own carry is never injected beside
// it -- never a co-commit of two distinct identities on the SAME proven
// basis -- and the carry's own disclosure reads superseded_by_caller, not
// applied.
func TestCallerHintOfTheSameKindSupersedesTheEngineCommittedCarry(t *testing.T) {
	engine, graph, store := buildCommittedAnchorEngine(t)

	one := needTurnRequest("request_5788_contest_one", true)
	oneResponse := needTurnResponse{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{committedAnchorRepo}}, bases: provenCommitBases(committedAnchorRepo)}
	oneResult, _ := committedAnchorTurn(t, engine, graph, store, one, oneResponse)

	callerPicked := SubjectRef{Kind: committedAnchorRepo.Kind, CanonicalID: "repository:caller-picked-hint", Label: "caller picked"}
	// A prior offer the caller now redeems -- a real candidate with a real
	// receipt id, stored the way an earlier turn's own offer would be.
	offerResult := validInvestigationResult()
	offerResult.ResultID = "result_5788_contest_offer"
	offerResult.SubjectResolution = SubjectResolution{
		Candidates: []SubjectCandidate{{ReceiptID: "receipt_5788_contest_pick", Subject: callerPicked, State: ResolutionAmbiguous, MatchReasons: []string{"offered"}, Confidence: 0.5}},
	}
	store.results[offerResult.ResultID] = offerResult

	two := needTurnRequest("request_5788_contest_two", true)
	two.ExpectedKinds = []contractsv1.ContextFabricSubjectKind{contractsv1.ContextFabricSubjectTeam}
	two = continuingNeedTurn(two, oneResult.ResultID)
	two.PriorSubjectReceipts = []BoundSubjectReceipt{{ResultID: offerResult.ResultID, ReceiptID: "receipt_5788_contest_pick"}}
	twoResponse := needTurnResponse{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{callerPicked}},
		bases:      provenCommitBases(callerPicked),
	}
	twoResult, call := committedAnchorTurn(t, engine, graph, store, two, twoResponse)

	// Never co-committed: the engine's own carry hint must not join the
	// caller's redeemed hint in the request ResolveSubjects actually saw.
	if len(call.request.RequestedScope.SubjectHints) != 1 || call.request.RequestedScope.SubjectHints[0].Source != string(hintsource.PriorSubjectReceipt) {
		t.Fatalf("turn two's ResolveSubjects request hints = %#v, want exactly the caller's own redeemed hint, nothing injected beside it", call.request.RequestedScope.SubjectHints)
	}
	// The contested entry is removed from the ledger BEFORE resolution
	// runs, not only excluded from the injected hint: it must not narrow
	// ResolveSubjects' own pool-selection parameter either, or a superseded
	// carry would still bias the search toward the very identity the
	// caller's own hint just contested.
	if call.anchor != nil {
		t.Fatalf("ResolveSubjects anchor selection = %#v, want nil: the superseded carry must not narrow the pool either", call.anchor)
	}

	got := memberEntries(twoResult, contractsv1.ContextFabricStructureNeedSubjectAnchor)
	if len(got) != 1 {
		t.Fatalf("turn two subject_anchor disclosure = %#v, want exactly one entry", got)
	}
	if got[0].Disposition != contractsv1.ContextFabricStructureDispositionSupersededByCaller || got[0].AppliedValue != committedAnchorRepo.CanonicalID {
		t.Fatalf("turn two subject_anchor disclosure = %+v, want disposition superseded_by_caller on the carried (turn one) value %s", got[0], committedAnchorRepo.CanonicalID)
	}

	twoSaved := store.states[twoResult.ResultID]
	if twoSaved == nil {
		t.Fatalf("fixture defect: turn two must persist a semantic state")
	}
	for _, entry := range twoSaved.ConfirmedNeeds {
		if entry.Member == contractsv1.ContextFabricStructureNeedSubjectAnchor {
			t.Fatalf("turn two ledger carries subject_anchor = %#v, want none: the caller's own redeemed hint superseded it", entry)
		}
	}
}

// TestZeroSubjectsTerminalReadsThePostVetoLedgerToo pins the class this
// file's own carry sequence applies at EVERY exit, not only the decisive
// save: an ordinary continuation whose own resolution commits nothing at
// all (the zero-subjects terminal, reached AFTER resolution ran) still
// discloses the carried entry's true post-decision state and drops it from
// the outgoing ledger, never the pre-veto snapshot.
func TestZeroSubjectsTerminalReadsThePostVetoLedgerToo(t *testing.T) {
	engine, graph, store := buildCommittedAnchorEngine(t)

	one := needTurnRequest("request_5788_zerosubjects_one", true)
	oneResponse := needTurnResponse{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{committedAnchorRepo}}, bases: provenCommitBases(committedAnchorRepo)}
	oneResult, _ := committedAnchorTurn(t, engine, graph, store, one, oneResponse)

	two := needTurnRequest("request_5788_zerosubjects_two", true)
	two.ExpectedKinds = []contractsv1.ContextFabricSubjectKind{contractsv1.ContextFabricSubjectTeam}
	two = continuingNeedTurn(two, oneResult.ResultID)
	// Nothing committed, nothing candidate, an empty cohort (needTurnGraph's
	// own DiscoverContext) -- the zero-subjects terminal's own trigger.
	twoResponse := needTurnResponse{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}}}
	twoResult, _ := committedAnchorTurn(t, engine, graph, store, two, twoResponse)

	got := memberEntries(twoResult, contractsv1.ContextFabricStructureNeedSubjectAnchor)
	if len(got) != 1 {
		t.Fatalf("turn two subject_anchor disclosure = %#v, want exactly one entry", got)
	}
	if got[0].Disposition != contractsv1.ContextFabricStructureDispositionVetoedUnresolved {
		t.Fatalf("turn two subject_anchor disclosure = %+v, want disposition vetoed_unresolved -- the zero-subjects terminal must read the same post-decision state the decisive save does", got[0])
	}

	twoSaved := store.states[twoResult.ResultID]
	if twoSaved == nil {
		t.Fatalf("fixture defect: turn two must persist a semantic state")
	}
	for _, entry := range twoSaved.ConfirmedNeeds {
		if entry.Member == contractsv1.ContextFabricStructureNeedSubjectAnchor {
			t.Fatalf("turn two ledger carries subject_anchor = %#v, want none: the zero-subjects terminal must drop it too", entry)
		}
	}
}

// TestFrameGateRefusalDiscloseNotEvaluatedNeverApplied pins the OTHER half
// of the same class: a turn that ends BEFORE its own resolution ever runs
// at all (a frame-gate refusal) has no post-decision verdict to disclose,
// so the carried entry must never read as `applied` -- it reads
// `not_evaluated`, and the outgoing ledger carries it forward UNCHANGED
// (there was nothing to evaluate, let alone drop).
func TestFrameGateRefusalDiscloseNotEvaluatedNeverApplied(t *testing.T) {
	engine, graph, store, telemetry := buildCommittedAnchorEngineWithTelemetry(t)

	one := needTurnRequest("request_5788_framegate_one", true)
	oneResponse := needTurnResponse{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{committedAnchorRepo}}, bases: provenCommitBases(committedAnchorRepo)}
	oneResult, _ := committedAnchorTurn(t, engine, graph, store, one, oneResponse)

	two := needTurnRequest("request_5788_framegate_two", true)
	two.ExpectedKinds = []contractsv1.ContextFabricSubjectKind{contractsv1.ContextFabricSubjectTeam}
	two = continuingNeedTurn(two, oneResult.ResultID)

	// The interpreter's own family outcome carries a refusing gate this
	// time -- Investigate returns from the frame-gate-refusal terminal
	// BEFORE ResolveSubjects is ever called, so graph.calls does not grow
	// (committedAnchorTurn's own call-count assertion does not apply here).
	engine.interpreter = twoTurnInterpreter{byRequestID: map[string]twoTurnInterpretation{
		one.RequestID: {
			interpreted: InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "count", TimeContext: TimeContext{Axis: TemporalCurrent}, FactRequirements: []FactRequirement{}},
			outcome: QuestionFamilyOutcome{
				Frame: committedAnchorFrame(), FrameObligations: committedAnchorFrame().Obligations,
				Family: QuestionFamilyScopedCohortStatus, Source: QuestionFamilySourceModel,
			},
		},
		two.RequestID: {
			interpreted: InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "count", TimeContext: TimeContext{Axis: TemporalCurrent}, FactRequirements: []FactRequirement{}},
			outcome: QuestionFamilyOutcome{
				Frame: committedAnchorFrame(), FrameObligations: committedAnchorFrame().Obligations,
				Family: QuestionFamilyScopedCohortStatus, Source: QuestionFamilySourceModel,
				Gate: FrameGate{Outcome: FrameGateRefusedBasis, RefuseBasis: CohortMemberKindUnservable, DeclaredMemberKind: SubjectTeam},
			},
		},
	}}
	callsMark := len(graph.calls)
	twoResult, err := engine.Investigate(context.Background(), acceptancePrincipal(), two)
	if err != nil {
		t.Fatalf("Investigate(%s) error = %v", two.RequestID, err)
	}
	if len(graph.calls) != callsMark {
		t.Fatalf("ResolveSubjects calls = %d, want %d: a frame-gate refusal must never reach resolution", len(graph.calls), callsMark)
	}

	got := memberEntries(twoResult, contractsv1.ContextFabricStructureNeedSubjectAnchor)
	if len(got) != 1 {
		t.Fatalf("turn two subject_anchor disclosure = %#v, want exactly one entry", got)
	}
	if got[0].Disposition != contractsv1.ContextFabricStructureDispositionNotEvaluated || got[0].AppliedValue != committedAnchorRepo.CanonicalID {
		t.Fatalf("turn two subject_anchor disclosure = %+v, want disposition not_evaluated on the carried value %s -- this turn's own resolution never ran, so it can never read as applied", got[0], committedAnchorRepo.CanonicalID)
	}

	if len(telemetry.confirmedNeedLedgers) == 0 {
		t.Fatalf("fixture defect: turn two must record a confirmed-need-ledger line")
	}
	event := telemetry.confirmedNeedLedgers[len(telemetry.confirmedNeedLedgers)-1]
	if event.AppliedAnchorKind != committedAnchorRepo.Kind || confirmedNeedValueHash(committedAnchorRepo.CanonicalID) != event.AppliedAnchorValueHash {
		t.Fatalf("event = %+v, want the carried anchor still applied_* on the ledger line -- nothing has evaluated it away yet", event)
	}
	if event.CaptureDecision != "" || event.CaptureSkipReason != CaptureSkipReasonFrameGateRefused {
		t.Fatalf("event capture_decision/capture_skip_reason = %q/%q, want empty/%q -- a frame-gate refusal never reaches subject resolution, so it must say so rather than leave the reason silently blank",
			event.CaptureDecision, event.CaptureSkipReason, CaptureSkipReasonFrameGateRefused)
	}
}

// TestGraphNotProjectedTerminalDisclosesNotEvaluated is the third exit kind
// this class covers: ResolveSubjects itself reports ErrGraphNotProjected,
// so this turn's own resolution never meaningfully ran either -- the same
// not_evaluated treatment the frame-gate refusal gets, not the post-veto
// one the zero-subjects terminal gets (that one's own resolution DID run,
// just to nothing).
func TestGraphNotProjectedTerminalDisclosesNotEvaluated(t *testing.T) {
	engine, graph, store, telemetry := buildCommittedAnchorEngineWithTelemetry(t)

	one := needTurnRequest("request_5788_notprojected_one", true)
	oneResponse := needTurnResponse{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{committedAnchorRepo}}, bases: provenCommitBases(committedAnchorRepo)}
	oneResult, _ := committedAnchorTurn(t, engine, graph, store, one, oneResponse)

	two := needTurnRequest("request_5788_notprojected_two", true)
	two.ExpectedKinds = []contractsv1.ContextFabricSubjectKind{contractsv1.ContextFabricSubjectTeam}
	two = continuingNeedTurn(two, oneResult.ResultID)
	twoResponse := needTurnResponse{err: fmt.Errorf("query context graph: %w", ErrGraphNotProjected)}
	twoResult, _ := committedAnchorTurn(t, engine, graph, store, two, twoResponse)

	got := memberEntries(twoResult, contractsv1.ContextFabricStructureNeedSubjectAnchor)
	if len(got) != 1 {
		t.Fatalf("turn two subject_anchor disclosure = %#v, want exactly one entry", got)
	}
	if got[0].Disposition != contractsv1.ContextFabricStructureDispositionNotEvaluated || got[0].AppliedValue != committedAnchorRepo.CanonicalID {
		t.Fatalf("turn two subject_anchor disclosure = %+v, want disposition not_evaluated on the carried value %s", got[0], committedAnchorRepo.CanonicalID)
	}

	twoSaved := store.states[twoResult.ResultID]
	if twoSaved == nil {
		t.Fatalf("fixture defect: turn two must persist a semantic state")
	}
	found := false
	for _, entry := range twoSaved.ConfirmedNeeds {
		if entry.Member == contractsv1.ContextFabricStructureNeedSubjectAnchor {
			found = true
			if entry.AppliedValue != committedAnchorRepo.CanonicalID || entry.Basis != ConfirmedNeedBasisEngineCommitted {
				t.Fatalf("turn two ledger subject_anchor = %#v, want the carried value unchanged", entry)
			}
		}
	}
	if !found {
		t.Fatalf("turn two ledger carries no subject_anchor entry, want the carried value passed through unchanged: this turn's own resolution never ran to disprove it")
	}

	if len(telemetry.confirmedNeedLedgers) == 0 {
		t.Fatalf("fixture defect: turn two must record a confirmed-need-ledger line")
	}
	event := telemetry.confirmedNeedLedgers[len(telemetry.confirmedNeedLedgers)-1]
	if event.CaptureDecision != "" || event.CaptureSkipReason != CaptureSkipReasonGraphNotProjected {
		t.Fatalf("event capture_decision/capture_skip_reason = %q/%q, want empty/%q -- a graph-not-projected degrade never reaches the capture check, so it must say so rather than leave the reason silently blank",
			event.CaptureDecision, event.CaptureSkipReason, CaptureSkipReasonGraphNotProjected)
	}
}

// TestResolutionErrorDisclosesTheReasonCaptureNeverRan is the fourth exit
// kind this class covers: ResolveSubjects returns an error OTHER than
// ErrGraphNotProjected (StageSubjectResolution) -- unlike the
// graph-not-projected degrade, this exit surfaces as an Investigate error,
// never a served terminal, but the deferred ledger line still fires and
// must still say why capture never ran rather than leaving the reason
// silently blank.
func TestResolutionErrorDisclosesTheReasonCaptureNeverRan(t *testing.T) {
	engine, graph, store, telemetry := buildCommittedAnchorEngineWithTelemetry(t)

	one := needTurnRequest("request_5788_resolutionerror_one", true)
	oneResponse := needTurnResponse{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{committedAnchorRepo}}, bases: provenCommitBases(committedAnchorRepo)}
	oneResult, _ := committedAnchorTurn(t, engine, graph, store, one, oneResponse)

	two := needTurnRequest("request_5788_resolutionerror_two", true)
	two.ExpectedKinds = []contractsv1.ContextFabricSubjectKind{contractsv1.ContextFabricSubjectTeam}
	two = continuingNeedTurn(two, oneResult.ResultID)
	graph.response = needTurnResponse{err: fmt.Errorf("authorization scope lookup failed")}
	if _, err := engine.Investigate(context.Background(), acceptancePrincipal(), two); err == nil {
		t.Fatal("Investigate() error = nil, want the resolve-subjects error this fixture is built to trigger")
	}

	if len(telemetry.confirmedNeedLedgers) == 0 {
		t.Fatalf("fixture defect: turn two must record a confirmed-need-ledger line even on an error exit")
	}
	event := telemetry.confirmedNeedLedgers[len(telemetry.confirmedNeedLedgers)-1]
	if event.CaptureDecision != "" || event.CaptureSkipReason != CaptureSkipReasonResolutionError {
		t.Fatalf("event capture_decision/capture_skip_reason = %q/%q, want empty/%q -- a hard subject-resolution error never reaches the capture check, so it must say so rather than leave the reason silently blank",
			event.CaptureDecision, event.CaptureSkipReason, CaptureSkipReasonResolutionError)
	}
}

// TestLedgerForExitCoversItsInputDomain is the unit-level pin under the
// integration tests above: resolutionRan=false is always an identity
// pass-through for the ledger and always not_evaluated for a present
// subject_anchor disclosure entry, regardless of what dropped/disposition
// the caller happens to pass (they are meaningless when resolution never
// ran, and must never leak through); resolutionRan=true delegates to the
// SAME two functions the decisive save and the supersession veto already
// stand on.
func TestLedgerForExitCoversItsInputDomain(t *testing.T) {
	t.Parallel()
	base := []ConfirmedNeedEntry{{Member: contractsv1.ContextFabricStructureNeedSubjectAnchor, AppliedKind: committedAnchorRepo.Kind, AppliedValue: committedAnchorRepo.CanonicalID, Basis: ConfirmedNeedBasisEngineCommitted}}
	anchorEntry := &contractsv1.ContextFabricConfirmedStructureEntry{
		Member: contractsv1.ContextFabricStructureNeedSubjectAnchor, AppliedValue: committedAnchorRepo.CanonicalID,
		Source: contractsv1.ContextFabricStructureSourceCarried, PriorResultID: "result_parent",
		Provenance: contractsv1.ContextFabricStructureEngineCommitted, Disposition: contractsv1.ContextFabricStructureDispositionApplied,
	}
	entries := []*contractsv1.ContextFabricConfirmedStructureEntry{anchorEntry}
	dropped := confirmedStructureMember{AppliedValue: committedAnchorRepo.CanonicalID}

	t.Run("resolution never ran: ledger unchanged, disclosure not_evaluated, whatever dropped/disposition say", func(t *testing.T) {
		t.Parallel()
		gotLedger, gotEntries := ledgerForExit(base, entries, false, dropped, true, contractsv1.ContextFabricStructureDispositionVetoedConflict)
		if !reflect.DeepEqual(gotLedger, base) {
			t.Fatalf("ledger = %#v, want base unchanged", gotLedger)
		}
		if gotEntries[0].Disposition != contractsv1.ContextFabricStructureDispositionNotEvaluated {
			t.Fatalf("disclosure = %+v, want not_evaluated", gotEntries[0])
		}
	})
	t.Run("resolution ran, not dropped: both pass through unchanged", func(t *testing.T) {
		t.Parallel()
		gotLedger, gotEntries := ledgerForExit(base, entries, true, confirmedStructureMember{}, false, "")
		if !reflect.DeepEqual(gotLedger, base) {
			t.Fatalf("ledger = %#v, want base unchanged", gotLedger)
		}
		if gotEntries[0] != anchorEntry {
			t.Fatalf("disclosure = %#v, want the untouched original pointer", gotEntries[0])
		}
	})
	t.Run("resolution ran, dropped: ledger drops subject_anchor, disclosure reads the given disposition", func(t *testing.T) {
		t.Parallel()
		gotLedger, gotEntries := ledgerForExit(base, entries, true, dropped, true, contractsv1.ContextFabricStructureDispositionVetoedConflict)
		for _, entry := range gotLedger {
			if entry.Member == contractsv1.ContextFabricStructureNeedSubjectAnchor {
				t.Fatalf("ledger = %#v, want subject_anchor dropped", gotLedger)
			}
		}
		if gotEntries[0].Disposition != contractsv1.ContextFabricStructureDispositionVetoedConflict {
			t.Fatalf("disclosure = %+v, want vetoed_conflict", gotEntries[0])
		}
	})
}
