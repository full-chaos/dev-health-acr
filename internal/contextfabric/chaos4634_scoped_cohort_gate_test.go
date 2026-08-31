package contextfabric

import (
	"context"
	"reflect"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-4634 (S4) -- the acceptance case CHAOS-4579/4531 never covered:
// Q-B, "What are the statuses of the fullchaos team's projects?"
// (CHAOS-4622 §2/§3). scoped_cohort_status is a family GateSubjectAxisOffers
// never had a row for at all -- it only ever knew "subject axis present or
// absent", so it left subject_candidate completely untouched regardless of
// class. That is precisely the CHAOS-4622 §2 defect: Q-B garbled offers
// that asked the caller to pick a single CI-run candidate on a question
// whose named term ("fullchaos team") is a SCOPE, not the answer's subject.
//
// scoped_cohort_status's own ApplicableAxes (chaos4632_question_family_
// registry.go) is [scope_anchor, expected_kind, window] -- deliberately
// EXCLUDING subject_handle/subject_candidate. scope_anchor maps onto the
// wire's existing subject_anchor axis (chaos4579_cohort_structure_gate.go's
// own package doc comment explains why), so the team's own ambiguity
// ("Fullchaos" vs "fullchaos", design §7's own two-real-entities case) is
// disclosed through AnchorOptions -- never a candidate pick.

// chaos4622GarbledMaterial reproduces the shape of Q-B's own garbled
// captures (design §7, CHAOS-4622 triage table): a CI-run-dominated
// candidate pool, kind offers unrelated to "project", alongside the ONE
// legitimate anchor ambiguity the org's real data actually has (two
// distinct "fullchaos"/"Fullchaos" entities).
func chaos4622GarbledMaterial() StructureOfferMaterial {
	return StructureOfferMaterial{
		Missing: []StructureNeedKind{
			contractsv1.ContextFabricStructureNeedExpectedKind,
			contractsv1.ContextFabricStructureNeedSubjectCandidate,
			contractsv1.ContextFabricStructureNeedSubjectAnchor,
			contractsv1.ContextFabricStructureNeedSubjectHandle,
		},
		KindOptions: []KindOption{
			{Kind: SubjectTeam, Label: "a team", OfferSource: contractsv1.ContextFabricStructureOfferEngine},
			{Kind: contractsv1.ContextFabricSubjectCIRun, Label: "a CI pipeline run", OfferSource: contractsv1.ContextFabricStructureOfferEngine},
		},
		CandidateOptions: []CandidateOption{
			{Kind: contractsv1.ContextFabricSubjectCIRun, CanonicalID: "ci_pipeline_run:1234", Label: "CI run #1234", OfferSource: contractsv1.ContextFabricStructureOfferEngine},
			{Kind: contractsv1.ContextFabricSubjectCIRun, CanonicalID: "ci_pipeline_run:1235", Label: "CI run #1235", OfferSource: contractsv1.ContextFabricStructureOfferEngine},
		},
		AnchorOptions: []AnchorOption{
			{Kind: SubjectTeam, CanonicalID: "team:fullchaos-lower", Label: "fullchaos", MatchedTermHash: "aaaaaaaaaaaaaaaaaaaaaaaa", OfferSource: contractsv1.ContextFabricStructureOfferEngine},
			{Kind: SubjectTeam, CanonicalID: "team:fullchaos-upper", Label: "Fullchaos", MatchedTermHash: "bbbbbbbbbbbbbbbbbbbbbbbb", OfferSource: contractsv1.ContextFabricStructureOfferEngine},
		},
		HandleOptions: []HandleOption{
			{Kind: contractsv1.ContextFabricSubjectCIRun, PatternID: "ci_run_number", Value: "1234", OfferSource: contractsv1.ContextFabricStructureOfferEngine},
		},
	}
}

// TestGateOffersByFamily_ScopedCohortStatusNeverOffersASingleSubjectPick is
// the S4 unit-level pin: candidate and handle are dropped (the CHAOS-4622
// §2 defect), kind and anchor survive (the legitimate "which kind, which
// fullchaos" narrowing), window is untouched throughout.
func TestGateOffersByFamily_ScopedCohortStatusNeverOffersASingleSubjectPick(t *testing.T) {
	t.Parallel()
	material := chaos4622GarbledMaterial()

	gated, outcome := GateOffersByFamily(material, QuestionFamilyOutcome{Family: QuestionFamilyScopedCohortStatus})

	wantMissing := []StructureNeedKind{
		contractsv1.ContextFabricStructureNeedExpectedKind,
		contractsv1.ContextFabricStructureNeedSubjectAnchor,
	}
	if !reflect.DeepEqual(gated.Missing, wantMissing) {
		t.Fatalf("Missing = %#v, want %#v -- scoped_cohort_status's ApplicableAxes is [scope_anchor, expected_kind, window] only", gated.Missing, wantMissing)
	}
	if len(gated.CandidateOptions) != 0 {
		t.Fatalf("CandidateOptions = %#v, want empty: CHAOS-4622 §2 -- a scoped cohort question must NEVER be asked to pick a single CI-run candidate", gated.CandidateOptions)
	}
	if len(gated.HandleOptions) != 0 {
		t.Fatalf("HandleOptions = %#v, want empty: subject_handle is not applicable to scoped_cohort_status", gated.HandleOptions)
	}
	if len(gated.KindOptions) != 2 {
		t.Fatalf("KindOptions = %#v, want the 2 kind offers untouched: expected_kind IS applicable (which kind of thing the scoped cohort is drawn from)", gated.KindOptions)
	}
	if len(gated.AnchorOptions) != 2 {
		t.Fatalf("AnchorOptions = %#v, want the 2 anchor offers untouched: this is scope_anchor's own wire vehicle -- the org's real ambiguity (\"fullchaos\" vs \"Fullchaos\") is exactly what should be asked", gated.AnchorOptions)
	}
	if outcome != CohortStructureGateApplied {
		t.Fatalf("outcome = %q, want %q", outcome, CohortStructureGateApplied)
	}
}

// TestCHAOS4622_ScopedCohortGate_QuestionBNeverOffersACIRunCandidate is the
// end-to-end pin through the class-default window gate path, mirroring
// TestCHAOS4579_ClassDefaultGate_CohortQuestionAsksOnlyForTheWindow's own
// shape for the scoped-cohort family instead of discovered-cohort.
func TestCHAOS4622_ScopedCohortGate_QuestionBNeverOffersACIRunCandidate(t *testing.T) {
	t.Parallel()
	interpreter := &countingInterpreter{
		interpretation: bootstrapInterpretation(), // Shape is irrelevant here -- family drives the gate
		family:         QuestionFamilyOutcome{Family: QuestionFamilyScopedCohortStatus},
	}
	graph := chaos4234GatedGraph()
	graph.material = chaos4622GarbledMaterial()
	store := &staticResultStore{results: map[string]InvestigationResult{}}
	telemetry := &recordingTelemetry{}
	engine := buildWindowGateEngineWithTelemetry(t, interpreter, graph, store, telemetry)

	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.StructureNeeds == nil {
		t.Fatal("StructureNeeds is nil, want the window + kind + anchor disclosure")
	}
	wantMissing := []StructureNeedKind{
		contractsv1.ContextFabricStructureNeedWindow,
		contractsv1.ContextFabricStructureNeedExpectedKind,
		contractsv1.ContextFabricStructureNeedSubjectAnchor,
	}
	if !reflect.DeepEqual(result.StructureNeeds.Missing, wantMissing) {
		t.Fatalf("StructureNeeds.Missing = %#v, want %#v -- CHAOS-4622 §2: Q-B must ask window, kind, and the scope anchor, never a candidate", result.StructureNeeds.Missing, wantMissing)
	}
	if len(result.StructureNeeds.CandidateOptions) != 0 {
		t.Fatalf("CandidateOptions = %#v, want empty -- never a CI-run candidate pick", result.StructureNeeds.CandidateOptions)
	}
	if len(result.StructureNeeds.HandleOptions) != 0 {
		t.Fatalf("HandleOptions = %#v, want empty", result.StructureNeeds.HandleOptions)
	}
	if len(result.StructureNeeds.AnchorOptions) != 2 {
		t.Fatalf("AnchorOptions = %#v, want the 2 real scope-anchor candidates (\"fullchaos\"/\"Fullchaos\") preserved", result.StructureNeeds.AnchorOptions)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("result fails Validate(): %v", err)
	}
	wantGates := []cohortStructureGateRecord{{CohortStructureGateApplied, ShapeSingleSubject}}
	if !reflect.DeepEqual(telemetry.cohortStructureGates, wantGates) {
		t.Fatalf("cohortStructureGates = %#v, want %#v", telemetry.cohortStructureGates, wantGates)
	}
}
