package contextfabric

import (
	"context"
	"reflect"
	"slices"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-12640 (lead ruling, FINAL): the class-default window gate counts a
// question beyond the window only when the offers-only pass found something
// to PICK (offerMaterialRedeemable). The standing zero-candidates disclosure
// stays: a named subject with zero candidates still names subject_anchor /
// subject_handle as missing, and is never answered on another subject. Rows
// are the shapes of the CHAOS-4451 before arm (ext65 cases 62, 46, 53).
func TestTheWindowGateCountsOnlyPickableAsks(t *testing.T) {
	t.Parallel()
	pick := handleStateTestPicks[0]
	emptyAsks := StructureOfferMaterial{Missing: []StructureNeedKind{contractsv1.ContextFabricStructureNeedSubjectAnchor, contractsv1.ContextFabricStructureNeedSubjectHandle}}
	realChoice := StructureOfferMaterial{
		Missing: []StructureNeedKind{contractsv1.ContextFabricStructureNeedExpectedKind, contractsv1.ContextFabricStructureNeedSubjectCandidate, contractsv1.ContextFabricStructureNeedSubjectAnchor, contractsv1.ContextFabricStructureNeedSubjectHandle},
		KindOptions: []KindOption{
			{Kind: SubjectRepository, Label: "a repository", OfferSource: contractsv1.ContextFabricStructureOfferEngine},
			{Kind: SubjectProject, Label: "a project", OfferSource: contractsv1.ContextFabricStructureOfferEngine},
		},
	}
	named := func(terms ...string) *QuestionFrame {
		frame := DeriveFrameObligations(QuestionFrame{Goals: []InvestigationGoal{GoalAssessState}, SubjectExpression: SubjectExpression{Kind: SubjectExpressionNamed, Named: &NamedSubjectExpression{Terms: terms}}, Temporal: TemporalIntentCurrent, Version: QuestionFrameVersion}, nil)
		return &frame
	}
	for _, tc := range []struct {
		name     string
		question string
		terms    []string
		material StructureOfferMaterial
		want     recordedInferredWindowGate
		gated    bool
	}{
		{"case 62: a bare name with zero candidates", "Is `quartz-referrals` behind on recovery time?", []string{"quartz-referrals"}, emptyAsks, recordedInferredWindowGate{InferredWindowGateAnswered, InferredWindowGateBasisWindowOnlyUnpickableAsks}, false},
		{"case 46: a real kind and candidate choice", "What is going on in the container repo?", []string{"container"}, realChoice, recordedInferredWindowGate{InferredWindowGateGated, InferredWindowGateBasisBeyondWindow}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			telemetry := &recordingTelemetry{}
			interpretation := singleSubjectInterpretation(pick.class, pick.confidence)
			interpretation.SubjectTerms = tc.terms
			interpreter := &countingInterpreter{interpretation: interpretation, family: QuestionFamilyOutcome{Family: QuestionFamilySubjectInvestigation, Frame: named(tc.terms...)}}
			request := validInvestigationRequest()
			request.Question = tc.question
			engine, graph := handleStateEngine(t, interpreter, telemetry)
			graph.material = tc.material
			result, _ := engine.Investigate(context.Background(), acceptancePrincipal(), request)
			if !reflect.DeepEqual(telemetry.inferredWindowGates, []recordedInferredWindowGate{tc.want}) {
				t.Fatalf("gate lines = %+v, want exactly %+v", telemetry.inferredWindowGates, tc.want)
			}
			if gated := result.Status == InvestigationClarificationRequired; gated != tc.gated {
				t.Fatalf("status = %q, want gated=%v", result.Status, tc.gated)
			}
			if tc.gated {
				return
			}
			// The hard limit: zero candidates end not found, on no other
			// subject, and keep the zero-candidates disclosure.
			if result.Status != InvestigationNoMatch || len(result.SubjectResolution.Committed) != 0 {
				t.Fatalf("status = %q committed = %v, want no_match with nothing committed", result.Status, result.SubjectResolution.Committed)
			}
			if result.StructureNeeds == nil || !slices.Contains(result.StructureNeeds.Missing, contractsv1.ContextFabricStructureNeedSubjectAnchor) || !slices.Contains(result.StructureNeeds.Missing, contractsv1.ContextFabricStructureNeedSubjectHandle) {
				t.Fatalf("structure_needs = %+v, want the zero-candidates disclosure kept", result.StructureNeeds)
			}
		})
	}
}
