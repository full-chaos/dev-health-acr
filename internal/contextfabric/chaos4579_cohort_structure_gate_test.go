package contextfabric

import (
	"context"
	"reflect"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
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
// RE-POINTED for CHAOS-4634 (S4): every direct-unit test below now calls
// GateOffersByFamily with an explicit QuestionFamilyOutcome instead of
// GateSubjectAxisOffers with a Shape -- the gate this file pins is
// family-keyed now, not Shape-keyed (chaos4579_cohort_structure_gate.go's
// own CHAOS-4634 header explains why). Per CHAOS-4634's own instruction,
// nothing here was deleted; every scenario keeps its original defect-of-
// record intent, re-expressed against the family the same interpretation
// resolves to in production (verified against chaos4632_question_family_
// precedence.go's own table). The TestCHAOS4579_SubjectlessTerminal_*
// end-to-end tests need NO change at all: they already exercise the real
// RuntimeQuestionInterpreter (via buildTerminalGateEngine), which now
// resolves and threads the family itself from the SAME Shape these tests
// already set, through the identical precedence rows (4/5) the direct
// unit tests below use explicitly. The TestCHAOS4579_ClassDefaultGate_*
// end-to-end tests use the countingInterpreter fake instead, which is
// given an explicit `family` field below matching what production would
// resolve for the same interpretation.

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

// TestGateOffersByFamily_DiscoveredCohortRankingDropsAnchorHandleAndKindRows
// is TestGateSubjectAxisOffers_DiscoveredCohortDropsAnchorAndHandleRows,
// re-pointed -- WITH ONE DELIBERATE CHANGE. The old §1.3 gate left the kind
// axis untouched on purpose ("a cohort question still legitimately narrows
// WHICH kind of thing the cohort is drawn from") -- that was
// kindOfferMaterial's own "still-open class gap" (chaos3900_structure_
// offers.go:117), which lane-4579's handoff §7 named and deliberately did
// NOT close. CHAOS-4634 closes it: discovered_cohort_ranking's own
// ApplicableAxes (chaos4632_question_family_registry.go) declares window
// ONLY -- design §6.4's own words, "kindOfferMaterial ... not called at
// all" -- so the kind axis is now gated exactly like anchor and handle.
// This is a real, intended widening of the fix's reach (CHAOS-4634's own
// ticket text lists this class gap as one of the things it subsumes), not
// a regression in this test.
func TestGateOffersByFamily_DiscoveredCohortRankingDropsAnchorHandleAndKindRows(t *testing.T) {
	t.Parallel()
	material := chaos4579CohortMaterial()
	material.AnchorOptions = []AnchorOption{{Kind: SubjectTeam, CanonicalID: "team:fullchaos", Label: "Fullchaos"}}
	material.HandleOptions = []HandleOption{{Kind: SubjectTeam, PatternID: "team_slug", Value: "fullchaos"}}

	gated, outcome := GateOffersByFamily(material, QuestionFamilyOutcome{Family: QuestionFamilyDiscoveredCohortRanking})

	if len(gated.Missing) != 0 {
		t.Fatalf("Missing = %#v, want empty: discovered_cohort_ranking's only applicable axis is window, which chaos4579CohortMaterial never disclosed", gated.Missing)
	}
	if len(gated.AnchorOptions) != 0 || len(gated.HandleOptions) != 0 || len(gated.KindOptions) != 0 {
		t.Fatalf("AnchorOptions/HandleOptions/KindOptions = %#v/%#v/%#v, want all three dropped: an option surviving its own Missing row is a receipt redeemable against a need that was never disclosed", gated.AnchorOptions, gated.HandleOptions, gated.KindOptions)
	}
	if outcome != CohortStructureGateApplied {
		t.Fatalf("outcome = %q, want %q", outcome, CohortStructureGateApplied)
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

	// ShapeSingleSubject resolves to subject_investigation (precedence row
	// 5), whose ApplicableAxes covers all five wire axes -- the family
	// re-point of "this shape has a subject axis".
	gated, outcome := GateOffersByFamily(material, QuestionFamilyOutcome{Family: QuestionFamilySubjectInvestigation})

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
//
// Family re-point: a single-term Shape=explicit_cohort sample (fewer than
// the two DISTINCT subject terms precedence row 3 requires) falls all the
// way through the table to unclassified (chaos4632_question_family_
// precedence.go's own row 7 comment names this exact case), whose
// ApplicableAxes is every axis -- so unclassified is the faithful
// re-point, not a hand-picked substitute.
func TestGateSubjectAxisOffers_ExplicitCohortKeepsTheSubjectAxis(t *testing.T) {
	t.Parallel()
	material := chaos4579CohortMaterial()

	gated, outcome := GateOffersByFamily(material, QuestionFamilyOutcome{Family: QuestionFamilyUnclassified})

	if !reflect.DeepEqual(gated, material) {
		t.Fatalf("gated = %#v, want unchanged for explicit_cohort", gated)
	}
	if outcome != CohortStructureGateSubjectBearing {
		t.Fatalf("outcome = %q, want %q", outcome, CohortStructureGateSubjectBearing)
	}
}

// TestGateOffersByFamily_OpenShapeNowRoutesToDiscoveredCohortRanking
// REPLACES TestGateSubjectAxisOffers_OpenShapeKeepsTheSubjectAxis's old
// assertion, with the change flagged rather than silently dropped.
//
// The old §1.3 gate (Shape-keyed) treated ShapeOpen conservatively: "open
// makes no claim about its own subject structure", so it stayed
// unrestricted. The CHAOS-4632 precedence table does NOT agree --
// precedence row 4 explicitly groups `ShapeOpen` with `ShapeDiscoveredCohort`
// ("the FIRST row that reads Shape" fires on either), so a
// minimally-structured open-shape sample (no GroupKind, no ScopeAnchorTerm,
// no comparison terms, fewer than two distinct subject terms) resolves to
// discovered_cohort_ranking, not unclassified. That is a deliberate
// decision in the design (§3's own taxonomy table lists `open` among
// discovered_cohort_ranking's CompatibleShapes), not a defect this test
// should paper over -- so this test now pins the NEW behavior instead of
// the old conservative default.
func TestGateOffersByFamily_OpenShapeNowRoutesToDiscoveredCohortRanking(t *testing.T) {
	t.Parallel()
	material := chaos4579CohortMaterial()

	// Verify the routing claim against the real resolver, not just assert
	// a hand-picked family -- this is the fact the test exists to pin.
	resolved := ResolveQuestionFamily([]FamilySample{{Shape: ShapeOpen, SubjectTerms: []string{"Ask Dev"}}})
	if resolved.Family != QuestionFamilyDiscoveredCohortRanking {
		t.Fatalf("ResolveQuestionFamily(Shape=open) = %q, want %q -- precedence row 4 groups Shape=open with Shape=discovered_cohort", resolved.Family, QuestionFamilyDiscoveredCohortRanking)
	}

	gated, outcome := GateOffersByFamily(material, resolved)

	if len(gated.Missing) != 0 || len(gated.AnchorOptions) != 0 || len(gated.HandleOptions) != 0 || len(gated.KindOptions) != 0 {
		t.Fatalf("gated = %#v, want every non-window axis dropped: discovered_cohort_ranking's only applicable axis is window", gated)
	}
	if outcome != CohortStructureGateApplied {
		t.Fatalf("outcome = %q, want %q", outcome, CohortStructureGateApplied)
	}
}

// TestGateSubjectAxisOffers_MatchedButNothingToRemoveReportsNoOp keeps
// "the gate FIRED" distinguishable from "the gate merely MATCHED". Without
// the split, a regression that stopped producing anchor material for an
// unrelated reason would be indistinguishable, in the artifacts alone,
// from this gate doing its job.
func TestGateSubjectAxisOffers_MatchedButNothingToRemoveReportsNoOp(t *testing.T) {
	t.Parallel()
	// Family re-point: since CHAOS-4634 also gates the kind axis for
	// discovered_cohort_ranking (see the DiscoveredCohortRanking test
	// above), a genuinely-NoOp fixture must already be window-only -- the
	// old fixture's KindOptions would now make this Applied, not NoOp,
	// which is the correct new behavior pinned separately.
	material := StructureOfferMaterial{
		Missing: []StructureNeedKind{contractsv1.ContextFabricStructureNeedWindow},
	}

	gated, outcome := GateOffersByFamily(material, QuestionFamilyOutcome{Family: QuestionFamilyDiscoveredCohortRanking})

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

	gated, _ := GateOffersByFamily(material, QuestionFamilyOutcome{Family: QuestionFamilyDiscoveredCohortRanking})

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

	if _, _ = GateOffersByFamily(material, QuestionFamilyOutcome{Family: QuestionFamilyDiscoveredCohortRanking}); !reflect.DeepEqual(material, before) {
		t.Fatalf("input material mutated to %#v, want %#v", material, before)
	}
}

// TestCHAOS4579_ClassDefaultGate_CohortQuestionAsksOnlyForTheWindow is the
// defect itself, end to end through the path chris's turn 1 took: the
// class-default window gate (gate 2) -> gatedOfferMaterial ->
// composeGatedStructureNeeds.
//
// Renamed from ...AsksOnlyForTheWindowAndKind (CHAOS-4634): the kind axis
// is now ALSO gated for discovered_cohort_ranking (see
// TestGateOffersByFamily_DiscoveredCohortRankingDropsAnchorHandleAndKindRows'
// own comment for why) -- turn 1 now asks for the window ALONE.
func TestCHAOS4579_ClassDefaultGate_CohortQuestionAsksOnlyForTheWindow(t *testing.T) {
	t.Parallel()
	interpreter := &countingInterpreter{
		interpretation: chaos4579CohortInterpretation(),
		family:         QuestionFamilyOutcome{Family: QuestionFamilyDiscoveredCohortRanking},
	}
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
		t.Fatal("StructureNeeds is nil, want the window disclosure")
	}
	wantMissing := []StructureNeedKind{contractsv1.ContextFabricStructureNeedWindow}
	if !reflect.DeepEqual(result.StructureNeeds.Missing, wantMissing) {
		t.Fatalf("StructureNeeds.Missing = %#v, want %#v -- CHAOS-4579/CHAOS-4634: a plural cohort question must never be asked for a single anchor, handle, or kind it has no subject axis for", result.StructureNeeds.Missing, wantMissing)
	}
	if len(result.StructureNeeds.AnchorOptions) != 0 || len(result.StructureNeeds.HandleOptions) != 0 || len(result.StructureNeeds.KindOptions) != 0 {
		t.Fatalf("AnchorOptions/HandleOptions/KindOptions = %#v/%#v/%#v, want all empty", result.StructureNeeds.AnchorOptions, result.StructureNeeds.HandleOptions, result.StructureNeeds.KindOptions)
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
	interpreter := &countingInterpreter{ // ShapeSingleSubject -> subject_investigation
		interpretation: bootstrapInterpretation(),
		family:         QuestionFamilyOutcome{Family: QuestionFamilySubjectInvestigation},
	}
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
	interpreter := &countingInterpreter{
		interpretation: chaos4579CohortInterpretation(),
		family:         QuestionFamilyOutcome{Family: QuestionFamilyDiscoveredCohortRanking},
	}
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

// buildTerminalGateEngine wires the SUBJECTLESS-TERMINAL path (regime B: an
// already-confirmed window, so the class-default window gate never fires),
// with telemetry, at a chosen interpreted shape. This is the SECOND
// production call site -- codex round 1, finding 4: without a test here,
// deleting the gate call in unresolved.go's terminalResult leaves every
// gate-2 test above green while anchor/handle rows and the v2 dispatch come
// straight back on this path. Proven by mutation before this test was
// written: with that call removed, the gate-2 suite passed unchanged.
func buildTerminalGateEngine(t *testing.T, shape InvestigationShape, material StructureOfferMaterial, telemetry EngineTelemetry) *Engine {
	t.Helper()
	interpretation := bootstrapInterpretation()
	interpretation.Shape = shape
	engine, err := NewEngine(EngineDependencies{
		Interpreter: RuntimeQuestionInterpreter{Runtime: fakeModelRuntime{interpreted: interpretation, draft: SynthesisDraft{}, receipt: acceptanceReceipt()}},
		Graph:       &acceptanceGraphReader{resolution: ambiguousResolution("Which one?"), context: emptyGraphContext(), material: material},
		Facts: factReaderFunc(func(_ context.Context, _ storage.Principal, request CanonicalFactRequest) (CanonicalFactBundle, error) {
			t.Fatalf("ReadFacts called with %#v -- a terminal result never reads canonical facts", request)
			return CanonicalFactBundle{}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			t.Fatal("Synthesize called -- a terminal result is composed without a model call")
			return InvestigationResult{}, nil
		}),
		Telemetry: telemetry,
	}, EngineOptions{
		ServiceVersion: "chaos4579-terminal-test",
		Now:            func() time.Time { return time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC) },
		NewResultID:    func() string { return "result_chaos4579_term1" },
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	return engine
}

// Renamed from ...CohortQuestionDropsAnchorAndHandleRows (CHAOS-4634): the
// window in this scenario is ALREADY CONFIRMED
// (validInvestigationRequestWithConfirmedWindow), and the kind axis is now
// ALSO gated for discovered_cohort_ranking (see
// TestGateOffersByFamily_DiscoveredCohortRankingDropsAnchorHandleAndKindRows'
// own comment for why) -- so this scenario now has NOTHING left to
// disclose at all, and StructureNeeds is correctly nil.
func TestCHAOS4579_SubjectlessTerminal_CohortQuestionDropsAnchorHandleAndKindRows(t *testing.T) {
	t.Parallel()
	telemetry := &recordingTelemetry{}
	engine := buildTerminalGateEngine(t, ShapeDiscoveredCohort, chaos4579CohortMaterial(), telemetry)

	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), validInvestigationRequestWithConfirmedWindow())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.StructureNeeds != nil {
		t.Fatalf("StructureNeeds = %#v, want nil: window is already confirmed and every other axis (kind/anchor/handle) is inapplicable to discovered_cohort_ranking, so nothing remains to disclose", result.StructureNeeds)
	}
	wantGates := []cohortStructureGateRecord{{CohortStructureGateApplied, ShapeDiscoveredCohort}}
	if !reflect.DeepEqual(telemetry.cohortStructureGates, wantGates) {
		t.Fatalf("cohortStructureGates = %#v, want %#v", telemetry.cohortStructureGates, wantGates)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("result fails Validate(): %v", err)
	}
}

// The terminal path's own v2 dispatch: schemaVersion is read from
// AnchorOptionsRequireV2 in this same function, so a gate that cleared the
// options but not the flag would mint a v2 result carrying no v2-bearing
// option. Asserted HERE, on the call site that owns that dispatch.
func TestCHAOS4579_SubjectlessTerminal_CohortQuestionIsNotPromotedToV2(t *testing.T) {
	t.Parallel()
	material := chaos4579CohortMaterial()
	// A wire-valid anchor option, deliberately: under the mutation that
	// removes this call site's gate, this option reaches the wire and the
	// result promotes to v2 -- which is exactly what this test must observe,
	// rather than tripping over a malformed fixture first.
	material.AnchorOptions = []AnchorOption{{
		Kind: SubjectTeam, CanonicalID: "team:fullchaos", Label: "Fullchaos",
		MatchedTermHash: "0123456789abcdef01234567",
		OfferSource:     contractsv1.ContextFabricStructureOfferEngine,
	}}
	material.AnchorOptionsRequireV2 = true
	engine := buildTerminalGateEngine(t, ShapeDiscoveredCohort, material, &recordingTelemetry{})

	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), validInvestigationRequestWithConfirmedWindow())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.SchemaVersion != InvestigationResultSchemaV1 {
		t.Fatalf("SchemaVersion = %q, want v1: every anchor option was removed, so nothing on this result needs v2 membership-verify semantics", result.SchemaVersion)
	}
	if result.Versions.ContractVersion != InvestigationResultSchemaV1 {
		t.Fatalf("Versions.ContractVersion = %q, want v1 to match SchemaVersion", result.Versions.ContractVersion)
	}
}

// The control on the SECOND call site: a subject-bearing shape still gets
// both rows with empty options, per the standing ruling.
func TestCHAOS4579_SubjectlessTerminal_SingleSubjectStillAsksForAnchorAndHandle(t *testing.T) {
	t.Parallel()
	telemetry := &recordingTelemetry{}
	engine := buildTerminalGateEngine(t, ShapeSingleSubject, chaos4579CohortMaterial(), telemetry)

	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), validInvestigationRequestWithConfirmedWindow())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.StructureNeeds == nil {
		t.Fatal("StructureNeeds is nil")
	}
	wantMissing := []StructureNeedKind{
		contractsv1.ContextFabricStructureNeedExpectedKind,
		contractsv1.ContextFabricStructureNeedSubjectAnchor,
		contractsv1.ContextFabricStructureNeedSubjectHandle,
	}
	if !reflect.DeepEqual(result.StructureNeeds.Missing, wantMissing) {
		t.Fatalf("StructureNeeds.Missing = %#v, want %#v -- unchanged on the terminal path too", result.StructureNeeds.Missing, wantMissing)
	}
	wantGates := []cohortStructureGateRecord{{CohortStructureGateSubjectBearing, ShapeSingleSubject}}
	if !reflect.DeepEqual(telemetry.cohortStructureGates, wantGates) {
		t.Fatalf("cohortStructureGates = %#v, want %#v", telemetry.cohortStructureGates, wantGates)
	}
}
