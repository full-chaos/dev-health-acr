package contextfabric

import (
	"context"
	"reflect"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-4579 / CHAOS-4531 -- §1.3's class-conditional gate.
//
// The defect of record (chris, kiac Ask Dev rig, 2026-08-29 19:59 PDT):
// turn 1 of "What teams are struggling and what are the contributing
// factors?" resolved kind=team correctly and then asked "Which repository,
// project, or team?" and "Which specific item?" with no candidates behind
// either -- missing=[window, subject_anchor, subject_handle] on a question
// with no subject to anchor. Turn 2, carrying only the window receipt,
// produced the correct discovered_cohort answer.
//
// Every test below is RED on origin/main f7f69b71: gateSubjectAxisOffers
// does not exist there, and the engine-level pair fails on the Missing
// assertion for the same reason the live turn 1 did.

// chaos4579CohortMaterial reproduces the material chris's turn 1 actually
// carried: a kind offer the resolution genuinely earned, beside
// subject_anchor and subject_handle rows disclosed with NOTHING to offer --
// the "no anchor offers were provided / no handle offers were provided"
// shell the UI rendered.
func chaos4579CohortMaterial() StructureOfferMaterial {
	return StructureOfferMaterial{
		Missing: []StructureNeedKind{
			contractsv1.ContextFabricStructureNeedExpectedKind,
			contractsv1.ContextFabricStructureNeedSubjectAnchor,
			contractsv1.ContextFabricStructureNeedSubjectHandle,
		},
		KindOptions: []KindOption{
			{Kind: SubjectTeam, Label: "a team", OfferSource: contractsv1.ContextFabricStructureOfferEngine},
			{Kind: SubjectProject, Label: "a project", OfferSource: contractsv1.ContextFabricStructureOfferEngine},
		},
	}
}

func chaos4579CohortInterpretation() InterpretedQuestion {
	interpretation := bootstrapInterpretation()
	interpretation.Shape = ShapeDiscoveredCohort
	return interpretation
}

func TestGateSubjectAxisOffers_DiscoveredCohortDropsAnchorAndHandleRows(t *testing.T) {
	t.Parallel()
	material := chaos4579CohortMaterial()
	material.AnchorOptions = []AnchorOption{{Kind: SubjectTeam, CanonicalID: "team:fullchaos", Label: "Fullchaos"}}
	material.HandleOptions = []HandleOption{{Kind: SubjectTeam, PatternID: "team_slug", Value: "fullchaos"}}

	gated, outcome := gateSubjectAxisOffers(material, ShapeDiscoveredCohort)

	wantMissing := []StructureNeedKind{contractsv1.ContextFabricStructureNeedExpectedKind}
	if !reflect.DeepEqual(gated.Missing, wantMissing) {
		t.Fatalf("Missing = %#v, want %#v: a question with no subject axis has no anchor and no handle to be missing", gated.Missing, wantMissing)
	}
	if len(gated.AnchorOptions) != 0 || len(gated.HandleOptions) != 0 {
		t.Fatalf("AnchorOptions/HandleOptions = %#v/%#v, want both dropped: an option surviving its own Missing row is a receipt redeemable against a need that was never disclosed", gated.AnchorOptions, gated.HandleOptions)
	}
	if outcome != CohortStructureGateApplied {
		t.Fatalf("outcome = %q, want %q", outcome, CohortStructureGateApplied)
	}
	// The kind axis is deliberately untouched: chris's own turn 1 resolved
	// kind=team correctly, and a cohort question still legitimately narrows
	// WHICH kind of thing the cohort is drawn from.
	if len(gated.KindOptions) != 2 {
		t.Fatalf("KindOptions = %#v, want the 2 kind offers untouched", gated.KindOptions)
	}
}

// TestGateSubjectAxisOffers_SubjectBearingShapeKeepsEmptyOfferedRows pins
// the OTHER direction: the standing zero-candidates ruling
// (chaos3900_structure_offers.go:1062-1067 -- "subject_anchor is STILL
// disclosed as Missing, with an EMPTY AnchorOptions list") is unchanged
// for every shape that has a subject axis. Without this test the fix could
// be widened into a general "drop empty offer rows" rule, which is exactly
// what that ruling forbids.
func TestGateSubjectAxisOffers_SubjectBearingShapeKeepsEmptyOfferedRows(t *testing.T) {
	t.Parallel()
	material := chaos4579CohortMaterial() // zero anchor/handle options, rows still disclosed

	gated, outcome := gateSubjectAxisOffers(material, ShapeSingleSubject)

	if !reflect.DeepEqual(gated, material) {
		t.Fatalf("gated = %#v, want the material byte-identical: a single_subject question genuinely IS missing an anchor, and the empty options list is the ruled 'missing-and-helpful, nothing offerable' disclosure", gated)
	}
	if outcome != CohortStructureGateSubjectBearing {
		t.Fatalf("outcome = %q, want %q", outcome, CohortStructureGateSubjectBearing)
	}
}

// TestGateSubjectAxisOffers_ExplicitCohortKeepsTheSubjectAxis pins the
// narrower-than-graphrank boundary: graphrank.DiscoveredCohort
// (discover.go:256) admits explicit_cohort too, because it is answering
// "may this run cohort ranking?". This gate answers a DIFFERENT question --
// "does this question have a subject to anchor?" -- and an explicit cohort
// NAMES its members, so an anchor is exactly what disambiguates which
// named things were meant. CHAOS-4531's ruling scopes the gate to
// discovered_cohort in as many words.
func TestGateSubjectAxisOffers_ExplicitCohortKeepsTheSubjectAxis(t *testing.T) {
	t.Parallel()
	material := chaos4579CohortMaterial()

	gated, outcome := gateSubjectAxisOffers(material, ShapeExplicitCohort)

	if !reflect.DeepEqual(gated, material) {
		t.Fatalf("gated = %#v, want unchanged for explicit_cohort", gated)
	}
	if outcome != CohortStructureGateSubjectBearing {
		t.Fatalf("outcome = %q, want %q", outcome, CohortStructureGateSubjectBearing)
	}
}

// TestGateSubjectAxisOffers_OpenShapeKeepsTheSubjectAxis: `open` makes no
// claim about its own subject structure, and treating it as axis-less
// would silently suppress the anchor offer for every unclassified
// question -- far wider than the ruled scope.
func TestGateSubjectAxisOffers_OpenShapeKeepsTheSubjectAxis(t *testing.T) {
	t.Parallel()
	material := chaos4579CohortMaterial()

	gated, outcome := gateSubjectAxisOffers(material, ShapeOpen)

	if !reflect.DeepEqual(gated, material) {
		t.Fatalf("gated = %#v, want unchanged for the open shape", gated)
	}
	if outcome != CohortStructureGateSubjectBearing {
		t.Fatalf("outcome = %q, want %q", outcome, CohortStructureGateSubjectBearing)
	}
}

// TestGateSubjectAxisOffers_MatchedButNothingToRemoveReportsNoOp keeps
// "the gate FIRED" distinguishable from "the gate merely MATCHED". Without
// the split, a regression that stopped producing anchor material for an
// unrelated reason would be indistinguishable, in the artifacts alone,
// from this gate doing its job.
func TestGateSubjectAxisOffers_MatchedButNothingToRemoveReportsNoOp(t *testing.T) {
	t.Parallel()
	material := StructureOfferMaterial{
		Missing:     []StructureNeedKind{contractsv1.ContextFabricStructureNeedExpectedKind},
		KindOptions: []KindOption{{Kind: SubjectTeam, Label: "a team", OfferSource: contractsv1.ContextFabricStructureOfferEngine}},
	}

	gated, outcome := gateSubjectAxisOffers(material, ShapeDiscoveredCohort)

	if !reflect.DeepEqual(gated.Missing, material.Missing) {
		t.Fatalf("Missing = %#v, want %#v unchanged", gated.Missing, material.Missing)
	}
	if outcome != CohortStructureGateNoOp {
		t.Fatalf("outcome = %q, want %q", outcome, CohortStructureGateNoOp)
	}
}

// TestGateSubjectAxisOffers_DiscoveredCohortClearsTheV2Promotion:
// AnchorOptionsRequireV2 (CHAOS-4042) is the SOLE signal promoting a
// result to the v2 semantic major, and unresolved.go/window.go both
// dispatch schemaVersion directly off it. With every anchor option
// removed, leaving it set would mint a v2 result carrying no v2-bearing
// option at all.
func TestGateSubjectAxisOffers_DiscoveredCohortClearsTheV2Promotion(t *testing.T) {
	t.Parallel()
	material := chaos4579CohortMaterial()
	material.AnchorOptions = []AnchorOption{{Kind: SubjectTeam, CanonicalID: "team:fullchaos", Label: "Fullchaos"}}
	material.AnchorOptionsRequireV2 = true

	gated, _ := gateSubjectAxisOffers(material, ShapeDiscoveredCohort)

	if gated.AnchorOptionsRequireV2 {
		t.Fatal("AnchorOptionsRequireV2 stayed true with zero anchor options: schemaVersion would promote to v2 on a result carrying no v2-bearing option")
	}
}

// TestGateSubjectAxisOffers_NeverMutatesItsInput: both call sites hold the
// pre-gate material for telemetry beside the gated copy.
func TestGateSubjectAxisOffers_NeverMutatesItsInput(t *testing.T) {
	t.Parallel()
	material := chaos4579CohortMaterial()
	material.AnchorOptions = []AnchorOption{{Kind: SubjectTeam, CanonicalID: "team:fullchaos", Label: "Fullchaos"}}
	before := chaos4579CohortMaterial()
	before.AnchorOptions = []AnchorOption{{Kind: SubjectTeam, CanonicalID: "team:fullchaos", Label: "Fullchaos"}}

	if _, _ = gateSubjectAxisOffers(material, ShapeDiscoveredCohort); !reflect.DeepEqual(material, before) {
		t.Fatalf("input material mutated to %#v, want %#v", material, before)
	}
}

// TestCHAOS4579_ClassDefaultGate_CohortQuestionAsksOnlyForTheWindowAndKind
// is the defect itself, end to end through the path chris's turn 1 took:
// the class-default window gate (gate 2) -> gatedOfferMaterial ->
// composeGatedStructureNeeds.
func TestCHAOS4579_ClassDefaultGate_CohortQuestionAsksOnlyForTheWindowAndKind(t *testing.T) {
	t.Parallel()
	interpreter := &countingInterpreter{interpretation: chaos4579CohortInterpretation()}
	graph := chaos4234GatedGraph()
	graph.material = chaos4579CohortMaterial()
	store := &staticResultStore{results: map[string]InvestigationResult{}}
	telemetry := &recordingTelemetry{}
	engine := buildWindowGateEngineWithTelemetry(t, interpreter, graph, store, telemetry)

	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.StructureNeeds == nil {
		t.Fatal("StructureNeeds is nil, want the window + kind disclosure")
	}
	wantMissing := []StructureNeedKind{
		contractsv1.ContextFabricStructureNeedWindow,
		contractsv1.ContextFabricStructureNeedExpectedKind,
	}
	if !reflect.DeepEqual(result.StructureNeeds.Missing, wantMissing) {
		t.Fatalf("StructureNeeds.Missing = %#v, want %#v -- CHAOS-4579: a plural cohort question must never be asked for a single anchor or handle it has no subject for", result.StructureNeeds.Missing, wantMissing)
	}
	if len(result.StructureNeeds.AnchorOptions) != 0 || len(result.StructureNeeds.HandleOptions) != 0 {
		t.Fatalf("AnchorOptions/HandleOptions = %#v/%#v, want both empty", result.StructureNeeds.AnchorOptions, result.StructureNeeds.HandleOptions)
	}
	if result.SchemaVersion != InvestigationResultSchemaV1 {
		t.Fatalf("SchemaVersion = %q, want v1", result.SchemaVersion)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("result fails Validate(): %v", err)
	}
	// Decision-basis telemetry: the gate that changed the disclosure says
	// so, and names the shape it fired for.
	wantGates := []cohortStructureGateRecord{{CohortStructureGateApplied, ShapeDiscoveredCohort}}
	if !reflect.DeepEqual(telemetry.cohortStructureGates, wantGates) {
		t.Fatalf("cohortStructureGates = %#v, want %#v", telemetry.cohortStructureGates, wantGates)
	}
	if !reflect.DeepEqual(telemetry.structureNeedsDisclosed, wantMissing) {
		t.Fatalf("structureNeedsDisclosed = %#v, want %#v -- the disclosed-member telemetry must agree with what was actually disclosed", telemetry.structureNeedsDisclosed, wantMissing)
	}
}

// TestCHAOS4579_ClassDefaultGate_SingleSubjectStillAsksForAnchorAndHandle
// is the control: the SAME material on a subject-bearing shape still
// discloses both rows with empty options, per the standing ruling. Run
// against a build where the gate is widened to every shape, this fails and
// the cohort test above passes -- which is what makes the pair a gate
// rather than a blanket suppression.
func TestCHAOS4579_ClassDefaultGate_SingleSubjectStillAsksForAnchorAndHandle(t *testing.T) {
	t.Parallel()
	interpreter := &countingInterpreter{interpretation: bootstrapInterpretation()} // ShapeSingleSubject
	graph := chaos4234GatedGraph()
	graph.material = chaos4579CohortMaterial()
	store := &staticResultStore{results: map[string]InvestigationResult{}}
	telemetry := &recordingTelemetry{}
	engine := buildWindowGateEngineWithTelemetry(t, interpreter, graph, store, telemetry)

	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.StructureNeeds == nil {
		t.Fatal("StructureNeeds is nil")
	}
	wantMissing := []StructureNeedKind{
		contractsv1.ContextFabricStructureNeedWindow,
		contractsv1.ContextFabricStructureNeedExpectedKind,
		contractsv1.ContextFabricStructureNeedSubjectAnchor,
		contractsv1.ContextFabricStructureNeedSubjectHandle,
	}
	if !reflect.DeepEqual(result.StructureNeeds.Missing, wantMissing) {
		t.Fatalf("StructureNeeds.Missing = %#v, want %#v -- the standing zero-candidates ruling is unchanged for subject-bearing shapes", result.StructureNeeds.Missing, wantMissing)
	}
	wantGates := []cohortStructureGateRecord{{CohortStructureGateSubjectBearing, ShapeSingleSubject}}
	if !reflect.DeepEqual(telemetry.cohortStructureGates, wantGates) {
		t.Fatalf("cohortStructureGates = %#v, want %#v -- the denominator must be observable, not inferred from a missing log line", telemetry.cohortStructureGates, wantGates)
	}
}

// TestCHAOS4579_ClassDefaultGate_CohortWithOnlySubjectAxisMaterialDegradesToWindowOnly:
// when the anchor/handle rows were the ONLY thing the offers-only pass
// produced, the gate empties the material and the gated terminal reduces
// to CHAOS-4118's window-only disclosure. The gated-offer outcome is
// reported as `empty` -- accurate about what will be disclosed -- and the
// cohort gate's own `applied` event beside it says WHY, which is what
// keeps "the pool was empty" distinguishable from "the gate removed it".
func TestCHAOS4579_ClassDefaultGate_CohortWithOnlySubjectAxisMaterialDegradesToWindowOnly(t *testing.T) {
	t.Parallel()
	interpreter := &countingInterpreter{interpretation: chaos4579CohortInterpretation()}
	graph := chaos4234GatedGraph()
	graph.material = StructureOfferMaterial{
		Missing: []StructureNeedKind{
			contractsv1.ContextFabricStructureNeedSubjectAnchor,
			contractsv1.ContextFabricStructureNeedSubjectHandle,
		},
	}
	store := &staticResultStore{results: map[string]InvestigationResult{}}
	telemetry := &recordingTelemetry{}
	engine := buildWindowGateEngineWithTelemetry(t, interpreter, graph, store, telemetry)

	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.StructureNeeds == nil {
		t.Fatal("StructureNeeds is nil, want the window-only disclosure")
	}
	wantMissing := []StructureNeedKind{contractsv1.ContextFabricStructureNeedWindow}
	if !reflect.DeepEqual(result.StructureNeeds.Missing, wantMissing) {
		t.Fatalf("StructureNeeds.Missing = %#v, want %#v: turn 1 asks ONE question, the window", result.StructureNeeds.Missing, wantMissing)
	}
	wantGates := []cohortStructureGateRecord{{CohortStructureGateApplied, ShapeDiscoveredCohort}}
	if !reflect.DeepEqual(telemetry.cohortStructureGates, wantGates) {
		t.Fatalf("cohortStructureGates = %#v, want %#v", telemetry.cohortStructureGates, wantGates)
	}
	if want := []GatedOfferResolutionOutcome{GatedOfferResolutionEmpty}; !reflect.DeepEqual(telemetry.gatedOfferResolutions, want) {
		t.Fatalf("gatedOfferResolutions = %#v, want %#v", telemetry.gatedOfferResolutions, want)
	}
}
