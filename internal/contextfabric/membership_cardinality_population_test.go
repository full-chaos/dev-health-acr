package contextfabric

// `Declared` becomes a number the answer did not already carry.
//
// WHAT THESE PIN. MembershipCardinality's two numbers exist so that "counted
// 14" and "counted 14 of 36" are distinguishable, and until retrieval learned
// to count past the render clamp they could not be: every input to `Declared`
// was derived from `cohort.Members`, so it could only ever equal `Served`. The
// pair carried no information and a count over a clamped cohort read as a
// census.
//
// The tests below are the arithmetic of that fix, at the unit that owns it.
// The stage-1 cases in membership_cardinality_test.go stay exactly as they
// were and are the other half of the claim: the clamp's own Before/After are
// CEILINGS, and this change still refuses to publish them as members.

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestCardinalityDeclaresTheRetrievalPopulationWhenItExceedsTheServedMembers(t *testing.T) {
	t.Parallel()
	// 14 carried, 36 seen: the measured shape of an organization whose
	// project count exceeded its render allowance.
	cardinality, counted := ComputeMembershipCardinality(countingCohort(SubjectProject, 14), 36, nil)

	if !counted {
		t.Fatal("counted = false, want true -- the fixture resolved no member set, so it proves nothing")
	}
	if cardinality.Served != 14 {
		t.Errorf("served = %d, want 14 -- served is what the answer carries and this change must not move it", cardinality.Served)
	}
	if cardinality.Declared != 36 {
		t.Errorf("declared = %d, want 36 -- the population retrieval observed, not the members the budget allowed", cardinality.Declared)
	}
	if !cardinality.Narrowed() {
		t.Error("Narrowed() = false, want true -- 14 of 36 is a cut, and a reader that cannot see it reads the count as a census")
	}
}

func TestCardinalityTakesTheRenderClampBasisWhenThePopulationIsWhatExceededTheAnswer(t *testing.T) {
	t.Parallel()
	// The mechanism that cut 36 to 14 is the pre-read clamp, recorded as the
	// stage-1 `cardinality` step. Its BASIS is the disclosure a reader needs;
	// its numbers are ceilings and must stay unpublished.
	cardinality, counted := ComputeMembershipCardinality(
		countingCohort(SubjectProject, 14), 36,
		[]contractsv1.ContextFabricPlanNarrowing{{
			Stage:  contractsv1.ContextFabricPlanNarrowingCardinality,
			Basis:  contractsv1.ContextFabricNarrowingBasisCanonicalIDLexical,
			Before: 50, After: 14,
		}})

	if !counted {
		t.Fatal("counted = false, want true")
	}
	if cardinality.Declared != 36 {
		t.Errorf("declared = %d, want 36 -- the clamp's own Before (50) is a ceiling and must never become a member count", cardinality.Declared)
	}
	if cardinality.Basis != contractsv1.ContextFabricNarrowingBasisCanonicalIDLexical {
		t.Errorf("basis = %q, want the clamp's basis -- a cut with no named mechanism is a number the reader cannot act on", cardinality.Basis)
	}
}

func TestCardinalityIgnoresAPopulationThatDoesNotExceedTheServedMembers(t *testing.T) {
	t.Parallel()
	// The reuse path threads 0 because no retrieval ran, and a caller
	// threading a stale value could pass anything below the member count.
	// Either way `Declared` must not fall below `Served`: a count declaring
	// fewer than it served would read through Narrowed() as "nothing was cut".
	for _, population := range []int{0, 3, 11} {
		cardinality, counted := ComputeMembershipCardinality(countingCohort(SubjectTeam, 11), population, nil)
		if !counted {
			t.Fatalf("population %d: counted = false, want true", population)
		}
		if cardinality.Served != 11 || cardinality.Declared != 11 {
			t.Errorf("population %d: served/declared = %d/%d, want 11/11", population, cardinality.Served, cardinality.Declared)
		}
		if cardinality.Narrowed() {
			t.Errorf("population %d: Narrowed() = true, want false -- nothing was cut", population)
		}
	}
}

func TestCardinalityLeavesTheBasisEmptyWhenThePopulationCutNothing(t *testing.T) {
	t.Parallel()
	// population EQUAL to served, with a clamp step present. The clamp ran
	// (it always records a step) but it cut nothing, so there is no loss to
	// attribute and the basis must stay empty.
	//
	// THIS IS THE CELL THAT SEPARATES `>` FROM `>=`. Every other fixture
	// here passes a population either above or below the served count, and
	// both operators agree on those. Weakened to `>=`, this cell publishes a
	// cut mechanism for a cohort that lost nothing -- a disclosure a reader
	// would act on and that never happened.
	cardinality, counted := ComputeMembershipCardinality(
		countingCohort(SubjectProject, 14), 14,
		[]contractsv1.ContextFabricPlanNarrowing{{
			Stage:  contractsv1.ContextFabricPlanNarrowingCardinality,
			Basis:  contractsv1.ContextFabricNarrowingBasisCanonicalIDLexical,
			Before: 50, After: 14,
		}})

	if !counted {
		t.Fatal("counted = false, want true")
	}
	if cardinality.Declared != 14 || cardinality.Served != 14 {
		t.Errorf("served/declared = %d/%d, want 14/14 -- nothing was cut", cardinality.Served, cardinality.Declared)
	}
	if cardinality.Basis != "" {
		t.Errorf("basis = %q, want empty -- naming a mechanism for a cut that did not happen is a false disclosure", cardinality.Basis)
	}
	if cardinality.Narrowed() {
		t.Error("Narrowed() = true, want false")
	}
}

func TestCardinalityLetsALaterMemberNarrowingOutrankThePopulation(t *testing.T) {
	t.Parallel()
	// Two losses on one turn: the clamp cut the population to what could be
	// rendered, and a stage-3 candidate narrowing then cut further. The
	// LARGEST count observed wins `Declared`, and the basis a reader is shown
	// is the one belonging to that count.
	cardinality, counted := ComputeMembershipCardinality(
		countingCohort(SubjectProject, 4), 9,
		[]contractsv1.ContextFabricPlanNarrowing{{
			Stage:  contractsv1.ContextFabricPlanNarrowingSynthesisInput,
			Basis:  contractsv1.ContextFabricNarrowingBasisCanonicalIDLexical,
			Before: 20, After: 4,
		}})

	if !counted {
		t.Fatal("counted = false, want true")
	}
	if cardinality.Declared != 20 {
		t.Errorf("declared = %d, want 20 -- a member narrowing observed more than the population arm did, and the largest observed count is the claim", cardinality.Declared)
	}
}

// THE CALLER-BOUND CASE: the population cuts the cohort and NO recorded step did.
//
// The engine records a `cardinality` narrowing step only when the response
// budget is what clamped MaxCohortMembers. A caller whose OWN MaxCohortMembers
// is the binding constraint therefore reaches assembly with a capped cohort, a
// fully counted pool, and a plan that never recorded a narrowing -- so the
// cardinality has no basis to carry.
//
// That combination made the outcome row `narrowed` with no cause, which the
// outcome validator refuses, turning a previously-served answer into a 422.
// It is invisible to every fixture that lets the budget do the clamping, which
// is why it survived the suite, the battery and CI: the corpus always passes a
// cap far above the allowance.
//
// Driven end to end through the real Engine, because the defect is in the
// SERVED document and a unit call on the row builder would not have produced
// one.
func TestACallerBoundCohortStillNamesWhyTheCountWasCut(t *testing.T) {
	t.Parallel()
	telemetry := &recordingTelemetry{}
	frame := countingFrame(SubjectTeam)
	// Cohort 10 = the caller's own cap, below the budget allowance of 14, so
	// no clamp is recorded. Population 36 = what retrieval saw.
	engine := newCountingEngineWithPopulation(t, countingCohort(SubjectTeam, 10), 36, frame, telemetry)
	result := runCountingRequest(t, engine, 10)

	assembled := countOutcomeRows(result, contractsv1.ContextFabricOutcomeStageAssembledResult)
	if len(assembled) != 1 {
		t.Fatalf("assembled count rows = %d, want exactly 1", len(assembled))
	}
	row := assembled[0]
	if row.Outcome != contractsv1.ContextFabricRequirementNarrowed {
		t.Fatalf("outcome = %q, want %q -- 10 of 36 is a cut", row.Outcome, contractsv1.ContextFabricRequirementNarrowed)
	}
	if row.CauseNarrowing != "" {
		t.Fatalf("cause_narrowing = %q, want empty -- this fixture records no narrowing step, and if it does the case under test is not being exercised", row.CauseNarrowing)
	}
	if row.CauseCoverage != contractsv1.ContextFabricCoverageDetailPopulationTruncated {
		t.Errorf("cause_coverage = %q, want %q -- a narrowed row with no cause is refused by the validator", row.CauseCoverage, contractsv1.ContextFabricCoverageDetailPopulationTruncated)
	}
	if !row.CauseObserved {
		t.Error("cause_observed = false, want true -- retrieval counted the members it could not carry; nothing here defaulted")
	}
	if row.Served != 10 || row.Declared != 36 {
		t.Errorf("served/declared = %d/%d, want 10/36", row.Served, row.Declared)
	}
}

// THE CAUSE HAS TO REACH THE OPERATOR, NOT JUST THE DOCUMENT.
//
// The caller-bound path records its cause as a COVERAGE cause, because no
// narrowing step ran to supply a basis. The event projected only basis and
// overrun, so on that path the Info line said `outcome=narrowed served=10
// declared=36` and named no mechanism -- a cut an operator could see and not
// explain.
//
// DRIVEN THROUGH THE REAL SLOG SINK, deliberately. The document-level guard
// beside this one uses recording telemetry and passes either way; that is
// exactly why the omission survived it, and why asserting on the emitted line
// is the only thing that pins the projection.
func TestTheCardinalityCauseReachesTheEmittedLine(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	sink := SlogEngineTelemetry{logger: slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))}

	// The caller-bound shape: a cut with a coverage cause and NO narrowing
	// basis. Built through the production projection, not hand-assembled.
	rows := []RequirementOutcomeRow{{
		Stage:         contractsv1.ContextFabricOutcomeStageAssembledResult,
		Requirement:   "count/member/team",
		Obligation:    string(ObligationCount),
		Outcome:       contractsv1.ContextFabricRequirementNarrowed,
		Impact:        contractsv1.ContextFabricAnswerImpactScope,
		Served:        10,
		Declared:      36,
		CauseCoverage: contractsv1.ContextFabricCoverageDetailPopulationTruncated,
		CauseObserved: true,
	}}
	event, ok := membershipCardinalityEventFrom(
		InvestigationResult{Completeness: AnswerCompleteness{Outcomes: rows}}, QuestionFamilyScopedCohortStatus)
	if !ok {
		t.Fatal("no cardinality event projected from an assembled count row")
	}
	if event.Basis != "" {
		t.Fatalf("basis = %q, want empty -- if a basis is present this fixture is not the caller-bound path", event.Basis)
	}

	sink.RecordMembershipCardinality(context.Background(), storage.Principal{OrgID: "org_1"}, event)

	line := buf.String()
	if !strings.Contains(line, `"cause_coverage":"population_truncated"`) {
		t.Errorf("emitted line carries no cause_coverage: %s", line)
	}
	if !strings.Contains(line, `"outcome":"narrowed"`) {
		t.Errorf("emitted line is not the narrowed outcome under test: %s", line)
	}
}

// mustCardinality is the test-side adapter for the call sites that used to
// hand appendMembershipCardinality its raw inputs. Production computes the
// cardinality once, before synthesis, and passes the VALUE; a test that still
// wants to express "the cardinality of this cohort" says so here rather than
// each site re-deriving it differently.
func mustCardinality(cohort *Cohort, population int, narrowing []contractsv1.ContextFabricPlanNarrowing) MembershipCardinality {
	cardinality, _ := ComputeMembershipCardinality(cohort, population, narrowing)
	return cardinality
}

// ONE NUMBER, AND IT REACHES THE SERVED DOCUMENT.
//
// The step now runs before synthesis instead of after it. That move is only
// safe if the number still describes the member set the reader receives, and
// if there is still exactly one of it -- a value computed early and a value
// computed late could differ, and a document carrying two count rows could not
// be read at all. This drives the real Engine end to end and reads the served
// result, so it fails if the pre-synthesis value is stale, dropped, or joined
// by a second.
func TestTheServedDocumentCarriesExactlyOneCountAndItIsThePreSynthesisOne(t *testing.T) {
	t.Parallel()
	telemetry := &recordingTelemetry{}
	frame := countingFrame(SubjectTeam)
	// 14 members carried, 36 seen: the served count must say both, from one
	// computation that happened before the model was asked anything.
	engine := newCountingEngineWithPopulation(t, countingCohort(SubjectTeam, 14), 36, frame, telemetry)
	result := runCountingRequest(t, engine, 14)

	assembled := countOutcomeRows(result, contractsv1.ContextFabricOutcomeStageAssembledResult)
	if len(assembled) != 1 {
		t.Fatalf("assembled count rows = %d, want exactly 1 -- two rows means two computations, and a reader cannot tell which number the answer stands behind", len(assembled))
	}
	row := assembled[0]
	if row.Served != 14 {
		t.Errorf("served = %d, want 14 -- the count must describe the member set the document carries", row.Served)
	}
	if row.Declared != 36 {
		t.Errorf("declared = %d, want 36 -- the population observed before the render clamp, carried through synthesis to the served document", row.Declared)
	}
	if len(result.Cohort.Members) != 14 {
		t.Errorf("served members = %d, want 14 -- the count and the member list must describe the same document", len(result.Cohort.Members))
	}
}

// THE CLAIM AND THE ROW ARE THE SAME NUMBER, end to end.
//
// This is what grounds a claim that has no canonical fact behind it. Every
// other minted claim is re-derived against the fact bundle by
// validateMintedClaimsGrounded; this one asserts something the server computed,
// so what stands in for that check is the served document agreeing with itself:
// the claim's value, the outcome row's served count, and the member list must
// all say the same thing, or the answer is asserting a number it cannot show.
func TestTheCardinalityClaimAgreesWithTheRowAndTheMembers(t *testing.T) {
	t.Parallel()
	telemetry := &recordingTelemetry{}
	frame := countingFrame(SubjectTeam)
	engine := newCountingEngineWithPopulation(t, countingCohort(SubjectTeam, 14), 36, frame, telemetry)
	result := runCountingRequest(t, engine, 14)

	var claim *ClaimedFact
	for i := range result.ClaimedFacts {
		if result.ClaimedFacts[i].Kind == contractsv1.ContextFabricFactCardinality {
			claim = &result.ClaimedFacts[i]
			break
		}
	}
	if claim == nil {
		t.Fatal("no cardinality claim on the served document -- the computed count reached the reader as prose only")
	}
	if claim.Value.Integer == nil {
		t.Fatal("cardinality claim carries no integer value")
	}

	assembled := countOutcomeRows(result, contractsv1.ContextFabricOutcomeStageAssembledResult)
	if len(assembled) != 1 {
		t.Fatalf("assembled count rows = %d, want exactly 1", len(assembled))
	}
	if got, want := *claim.Value.Integer, int64(assembled[0].Served); got != want {
		t.Errorf("claim value = %d, outcome row served = %d -- the document asserts two different counts", got, want)
	}
	if got, want := *claim.Value.Integer, int64(len(result.Cohort.Members)); got != want {
		t.Errorf("claim value = %d, member list = %d -- the count does not describe the members served beside it", got, want)
	}
	if claim.Subject.Kind != SubjectOrganization {
		t.Errorf("claim subject kind = %q, want organization -- a population count is not true of any single member", claim.Subject.Kind)
	}
	// The prose states it too, from the same value.
	if !strings.Contains(result.DeterministicAnswer, "Counted 14 teams of 36 found.") {
		t.Errorf("answer prose does not state the count: %q", result.DeterministicAnswer)
	}
}

// cardinalityFor is the test-side stand-in for what production threads.
//
// Production computes the cardinality once per pass, before synthesis, and
// hands the VALUE to every consumer. A test that passes a zero value is not
// exercising the same code path -- an unresolved cardinality makes the read
// row state an absence, which is a different assertion from the one most of
// these tests are making. Derived from the result's own cohort so the value a
// test threads describes the document that test is about.
func cardinalityFor(result InvestigationResult, plan AnswerPlan) MembershipCardinality {
	cardinality, _ := ComputeMembershipCardinality(result.Cohort, 0, plan.Narrowing)
	return cardinality
}

// THE CLAIM CAP, AT ITS EDGE.
//
// A 251st claimed fact fails the contract bound and invalidates the WHOLE
// answer -- so at the cap the count claim is dropped and the answer is served
// without it, rather than not served at all. The count itself is unaffected:
// it is still on the outcome row, and the Info line still reports it, with
// `claimed` false so the loss is visible.
//
// Pinned at 249/250/251 because the whole decision is one comparison and the
// only way it can be wrong is by one.
func TestTheCardinalityClaimIsDroppedExactlyAtTheContractCap(t *testing.T) {
	t.Parallel()
	const cap = contractsv1.ContextFabricClaimedFactsMaxCount
	for _, tc := range []struct {
		existing int
		admitted bool
		why      string
	}{
		{cap - 1, true, "one slot left: the count takes it"},
		{cap, false, "at the cap: a 251st claim invalidates the answer"},
		{cap + 1, false, "already over: nothing to do but refuse"},
	} {
		if got := cardinalityClaimAdmitted(tc.existing); got != tc.admitted {
			t.Errorf("cardinalityClaimAdmitted(%d) = %v, want %v -- %s", tc.existing, got, tc.admitted, tc.why)
		}
	}
	// Non-vacuous control: the bound under test is the contract's, not a
	// number restated here.
	if cap != 250 {
		t.Fatalf("ContextFabricClaimedFactsMaxCount = %d; this test's 249/250/251 cells are about the real bound", cap)
	}
}
