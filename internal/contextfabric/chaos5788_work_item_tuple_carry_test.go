package contextfabric

// A work-item-tuple continuation is one more continuation kind, not a
// special case: the capture/veto sequence that keeps a carried anchor
// honest runs on every one of them, never only the shapes whose own
// resolution happens to reach children_of_scope through the ordinary path.

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/hintsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// twoTurnInterpretation is one turn's scripted (InterpretedQuestion,
// QuestionFamilyOutcome) pair.
type twoTurnInterpretation struct {
	interpreted InterpretedQuestion
	outcome     QuestionFamilyOutcome
}

// twoTurnInterpreter scripts a distinct interpretation per request id, so a
// scenario can drive an ordinary turn followed by a work-item-tuple turn
// through the SAME engine and store -- Investigate's own confirmed-need
// ledger continuation (request.ParentResultID) requires both turns to share
// one engine, and familyInterpreter (used elsewhere in this package) scripts
// only one fixed interpretation for the whole engine.
type twoTurnInterpreter struct {
	byRequestID map[string]twoTurnInterpretation
}

func (i twoTurnInterpreter) Interpret(_ context.Context, _ storage.Principal, request InvestigationRequest) (InterpretedQuestion, QuestionFamilyOutcome, error) {
	scripted, ok := i.byRequestID[request.RequestID]
	if !ok {
		return InterpretedQuestion{}, QuestionFamilyOutcome{}, fmt.Errorf("twoTurnInterpreter: no scripted interpretation for %s", request.RequestID)
	}
	return scripted.interpreted, scripted.outcome, nil
}

var (
	workItemTupleCarryAnchor      = SubjectRef{Kind: SubjectProject, CanonicalID: "project:carry-anchor", Label: "carry anchor"}
	workItemTupleCarryAnchorOther = SubjectRef{Kind: SubjectProject, CanonicalID: "project:carry-anchor-other", Label: "other"}
)

// buildWorkItemTupleCarryEngine wires one engine spanning both an ordinary
// turn (committedAnchorFrame, family ScopedCohortStatus) and a
// work-item-tuple turn (prospectiveTupleFrame, the SAME family, which
// allows the tuple admission) -- a real fresh work-item-tuple dispatch,
// mirroring completeness_authority_work_item_census_test.go's own working
// fixture rather than a hand-assembled verdict.
func buildWorkItemTupleCarryEngine(t *testing.T, ordinaryRequestID, tupleRequestID string) (*Engine, *needTurnGraph, *staticResultStore, *recordingTelemetry) {
	t.Helper()
	ordinaryFrame := committedAnchorFrame()
	tupleFrame := prospectiveTupleFrame(GoalAssessState, GoalCountOrAggregate)
	validated := ValidateFrame(tupleFrame, nil, "").Frame
	graph := &needTurnGraph{}
	store := &staticResultStore{results: map[string]InvestigationResult{}, states: map[string]*PersistedSemanticState{}}
	telemetry := &recordingTelemetry{}
	gate, err := NewWorkItemMembershipGate(1, 0)
	if err != nil {
		t.Fatalf("NewWorkItemMembershipGate() error = %v", err)
	}
	memberID, omitted, err := identity.Derive(identity.KindWorkItem, []string{"repo-1", "work-1"}, nil)
	if err != nil {
		t.Fatalf("identity.Derive() error = %v", err)
	}
	if omitted {
		t.Fatal("identity.Derive() unexpectedly omitted the canonical work-item fixture")
	}
	interpreter := twoTurnInterpreter{byRequestID: map[string]twoTurnInterpretation{
		ordinaryRequestID: {
			interpreted: InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "count", TimeContext: TimeContext{Axis: TemporalCurrent}, FactRequirements: []FactRequirement{}},
			outcome: QuestionFamilyOutcome{
				Frame: ordinaryFrame, FrameObligations: ordinaryFrame.Obligations,
				Family: QuestionFamilyScopedCohortStatus, Source: QuestionFamilySourceModel,
				WinningSampleIndex: 0, WinningSample: FamilySample{},
			},
		},
		tupleRequestID: {
			interpreted: InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}, FactRequirements: []FactRequirement{{Kind: FactHealth}}},
			outcome: QuestionFamilyOutcome{
				Family: QuestionFamilyScopedCohortStatus, Source: QuestionFamilySourceModel,
				Frame: &validated, FrameObligations: validated.Obligations,
				Gate:          workItemTupleFrameGate(DecideFrameGate(ValidateFrame(validated, nil, ""), true), &validated, workItemTupleFamilyPolicyForTest(QuestionFamilyScopedCohortStatus), TimeContext{Axis: TemporalCurrent}),
				WinningSample: FamilySample{ScopeAnchorKind: SubjectProject, ScopeAnchorTerm: "a"},
			},
		},
	}}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: interpreter,
		Graph:       graph,
		CandidateVerifier: func(context.Context, storage.Principal, RequestedScope, ResolvedGraphBinding, SubjectKind, string) (bool, CandidateVerificationReason) {
			return true, CandidateVerificationValid
		},
		WorkItemMembership: tupleMembershipFunc(func(ctx context.Context, _ storage.Principal, _ WorkItemMembershipRequest) (*WorkItemMembershipLease, WorkItemMembershipResult, error) {
			lease, err := gate.Acquire(ctx)
			return lease, WorkItemMembershipResult{
				Census:  WorkItemMembershipCensus{State: WorkItemMembershipCensusExact, PopulationMeasured: true, AuthorizedPopulation: 1},
				Members: []WorkItemMembershipMember{{CanonicalID: memberID, WorkItemID: "work-1"}},
			}, err
		}),
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{Facts: []CanonicalFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}, Version: "ops-v1"}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return validInvestigationResult(), nil
		}),
		Results: store, Requirements: registryDeriver{}, Telemetry: telemetry,
	}, EngineOptions{
		ServiceVersion: "chaos5788-work-item-tuple-carry-test",
		Now:            func() time.Time { return time.Unix(500, 0).UTC() },
		NewResultID:    resultIDSequence(),
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	return engine, graph, store, telemetry
}

// TestWorkItemTupleContinuationDropsADisagreeingCarryFromItsOwnLedger:
// turn one commits a project anchor via authoritative identity, no offer
// ever raised -- captured as engine_committed, exactly as this file's own
// ordinary-continuation fixtures capture one. Turn two is a work-item-tuple
// continuation (children_of_scope over work_item, anchored on project)
// naming turn one as its parent, whose OWN resolution commits a DIFFERENT
// project on an identity-proven basis: a genuine disagreement. Turn two's
// own outgoing ledger must not carry turn one's stale anchor forward for a
// turn three to inherit, and turn two's own request to ResolveSubjects must
// still have carried the hint (the injection half of the same sequence).
func TestWorkItemTupleContinuationDropsADisagreeingCarryFromItsOwnLedger(t *testing.T) {
	engine, graph, store, _ := buildWorkItemTupleCarryEngine(t, "request_5788_tuple_carry_one", "request_5788_tuple_carry_two")

	one := needTurnRequest("request_5788_tuple_carry_one", true)
	oneResponse := needTurnResponse{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{workItemTupleCarryAnchor}}, bases: provenCommitBases(workItemTupleCarryAnchor)}
	oneResult, _ := committedAnchorTurn(t, engine, graph, store, one, oneResponse)
	oneSaved := store.states[oneResult.ResultID]
	if oneSaved == nil {
		t.Fatalf("fixture defect: turn one must persist a semantic state")
	}
	wantOneEntry := ConfirmedNeedEntry{Member: contractsv1.ContextFabricStructureNeedSubjectAnchor, AppliedKind: workItemTupleCarryAnchor.Kind, AppliedValue: workItemTupleCarryAnchor.CanonicalID, Basis: ConfirmedNeedBasisEngineCommitted}
	if len(oneSaved.ConfirmedNeeds) != 1 || oneSaved.ConfirmedNeeds[0] != wantOneEntry {
		t.Fatalf("turn one ledger = %#v, want exactly the engine-committed anchor %#v", oneSaved.ConfirmedNeeds, wantOneEntry)
	}

	two := needTurnRequest("request_5788_tuple_carry_two", true)
	two = continuingNeedTurn(two, oneResult.ResultID)
	twoResponse := needTurnResponse{
		resolution: SubjectResolution{
			Candidates: []SubjectCandidate{{ReceiptID: "receipt_tuple_carry_other", Subject: workItemTupleCarryAnchorOther, State: ResolutionCommitted, MatchedTerms: []string{"a"}, MatchReasons: []string{"matched"}, Confidence: 1}},
			Committed:  []SubjectRef{workItemTupleCarryAnchorOther},
		},
		bases: provenCommitBases(workItemTupleCarryAnchorOther),
	}
	twoResult, call := committedAnchorTurn(t, engine, graph, store, two, twoResponse)

	// The injection half: the carried anchor still reaches ResolveSubjects
	// as a hint, whether or not this turn's own tuple resolution can
	// consume it.
	wantHint := SubjectHint{Kind: workItemTupleCarryAnchor.Kind, ID: workItemTupleCarryAnchor.CanonicalID, Label: workItemTupleCarryAnchor.CanonicalID, Source: string(hintsource.EngineCommittedAnchorCarry)}
	hintSent := false
	for _, hint := range call.request.RequestedScope.SubjectHints {
		if hint == wantHint {
			hintSent = true
			break
		}
	}
	if !hintSent {
		t.Fatalf("tuple turn's ResolveSubjects request hints = %#v, want %#v among them", call.request.RequestedScope.SubjectHints, wantHint)
	}

	// The drop half, the regression this file exists to pin: turn two's own
	// outgoing ledger must never carry turn one's stale anchor forward.
	twoSaved := store.states[twoResult.ResultID]
	if twoSaved == nil {
		t.Fatalf("fixture defect: turn two must persist a semantic state")
	}
	for _, entry := range twoSaved.ConfirmedNeeds {
		if entry.Member == contractsv1.ContextFabricStructureNeedSubjectAnchor {
			t.Fatalf("turn two ledger carries subject_anchor = %#v, want none: this turn's own resolution disagreed with the carry", entry)
		}
	}
}
