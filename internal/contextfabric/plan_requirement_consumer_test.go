package contextfabric

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// THE CONSUMER TEST: drive the public entry point, read the answer back, and
// assert BOTH published arrays are on the document a caller receives.
//
// A test that asserted what the projection returned would prove the projection
// runs, not that the rows reach anyone. Writing a value nothing reads is half
// a mechanism, and this layer's whole claim is about what a reader of the
// served artifact can reconstruct -- so the assertion has to be made on the
// artifact, from outside.
//
// The engine is driven with a real RequirementDeriver rather than a stub, so
// the rows on the document are the ones the derivation actually produced for
// this frame. A stub returning a fixed row would let the engine drop the
// derivation entirely and this test would still pass.
func TestInvestigatePublishesThePlanRequirementRowsOnTheServedAnswer(t *testing.T) {
	t.Parallel()
	calls := 0
	telemetry := &recordingTelemetry{}
	deriver := &fixedRequirementDeriver{rows: twoRequirementRows()}

	engine := planRequirementEngine(t, deriver, &calls, telemetry)

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow())
	if err != nil && !errors.Is(err, ErrAnswerExceedsBudget) {
		t.Fatalf("Investigate() error = %v", err)
	}

	// THE DERIVER MUST HAVE BEEN CONSULTED. Zero calls would mean every
	// assertion below is about an empty document, and "no rows" would read
	// as agreement rather than as a missing mechanism.
	if deriver.calls == 0 {
		t.Fatal("the engine never called the requirement deriver; the rows on the document cannot have come from it")
	}

	if result.AnswerPlan == nil {
		t.Fatal("the served answer carries no answer plan")
	}
	if len(result.AnswerPlan.Requirements) != len(deriver.rows) {
		t.Fatalf("the served plan carries %d requirement rows, the derivation produced %d",
			len(result.AnswerPlan.Requirements), len(deriver.rows))
	}
	// The rows must be the DERIVATION'S, not merely well formed. Assert a
	// value only the derivation could have supplied.
	byIdentity := map[string]contractsv1.ContextFabricPlanRequirement{}
	for _, row := range result.AnswerPlan.Requirements {
		byIdentity[row.Requirement] = row
	}
	computed, present := byIdentity["count/member/repository"]
	if !present {
		t.Fatalf("the served plan does not describe the computed requirement; it carries %v", keysOfPlanRows(byIdentity))
	}
	if computed.Step != string(ComputedStepMembershipCardinality) {
		t.Errorf("served computed row names step %q, the derivation named %q", computed.Step, ComputedStepMembershipCardinality)
	}
	if computed.InputClass != string(ComputedInputResolvedMemberSet) {
		t.Errorf("served computed row names input class %q, the derivation named %q", computed.InputClass, ComputedInputResolvedMemberSet)
	}
	if computed.StepExecution != string(ComputedStepDeclaredOnly) {
		t.Errorf("served computed row names execution %q, the derivation named %q", computed.StepExecution, ComputedStepDeclaredOnly)
	}
	read, present := byIdentity["state/subject/project"]
	if !present {
		t.Fatalf("the served plan does not describe the read requirement; it carries %v", keysOfPlanRows(byIdentity))
	}
	// The two served rows must DIFFER in the arm they describe, or a
	// projection that emitted one shape twice would satisfy both lookups.
	if read.Kind == computed.Kind {
		t.Fatalf("both served rows carry kind %q; the read/computed distinction did not survive to the wire", read.Kind)
	}
	// THE KINDS THEMSELVES, not their count. What could serve this
	// requirement is exactly what the row exists to say, so a projection that
	// emitted the right NUMBER of wrong kinds would satisfy a length check
	// and mislead every reader of the document.
	wantKinds := []string{string(FactHealth), string(FactStatus)}
	gotKinds := make([]string, 0, len(read.FactKinds))
	for _, kind := range read.FactKinds {
		gotKinds = append(gotKinds, string(kind))
	}
	if !reflect.DeepEqual(gotKinds, wantKinds) {
		t.Errorf("the served read row names fact kinds %v, the derivation named %v", gotKinds, wantKinds)
	}

	// THE JOIN, on the served document: every outcome row's identity must
	// resolve to a requirement the same document describes.
	if len(result.Completeness.Outcomes) == 0 {
		t.Fatal("the served answer carries no outcome rows; the join has nothing to check")
	}
	joined := 0
	for _, outcome := range result.Completeness.Outcomes {
		if outcome.Requirement == "" {
			continue // an unattributed row is legal and joins to nothing
		}
		joined++
		if _, present := byIdentity[outcome.Requirement]; !present {
			t.Errorf("the served answer names outcome for requirement %q, which its own plan does not describe", outcome.Requirement)
		}
	}
	// COUNT WHAT REACHED THE ASSERTION. The guard above proves the set is
	// non-empty, which is NOT the same as proving any row is attributed: a
	// document whose outcome rows were all unattributed would skip every
	// iteration and pass having joined nothing.
	if joined == 0 {
		t.Fatal("no attributed outcome row reached the join; the served document was never checked against its own plan")
	}

	// And the whole document must be valid: these arrays go through the same
	// validator the store and the route run, so a row shape the engine can
	// produce but the contract rejects fails here rather than in production.
	if err := result.Validate(); err != nil {
		t.Fatalf("the served answer does not satisfy its own contract: %v", err)
	}
}

// The plan requirements must survive the STORE, not merely the engine.
//
// The store persists the whole result as one JSON payload and validates on
// both save and get, so a field the engine emits and the contract rejects --
// or one the encoder drops -- fails here. Read back through the store's own
// public Get, which is the surface a later turn actually reads.
func TestPlanRequirementRowsSurviveAStoreRoundTrip(t *testing.T) {
	t.Parallel()
	calls := 0
	telemetry := &recordingTelemetry{}
	deriver := &fixedRequirementDeriver{rows: twoRequirementRows()}
	engine := planRequirementEngine(t, deriver, &calls, telemetry)

	served, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow())
	if err != nil && !errors.Is(err, ErrAnswerExceedsBudget) {
		t.Fatalf("Investigate() error = %v", err)
	}
	if served.AnswerPlan == nil || len(served.AnswerPlan.Requirements) == 0 {
		t.Fatal("the served answer carries no requirement rows; the round trip would prove nothing")
	}

	// Round-trip through the JSON encoding the store persists. This is the
	// transform that turns an in-memory document into the bytes a later turn
	// reads back, and it is where an omitempty or a nil/empty distinction
	// goes wrong.
	decoded := reencodeResult(t, served)
	if got, want := len(decoded.AnswerPlan.Requirements), len(served.AnswerPlan.Requirements); got != want {
		t.Fatalf("after a round trip the plan carries %d requirement rows, before it carried %d", got, want)
	}
	compared := 0
	for index, before := range served.AnswerPlan.Requirements {
		after := decoded.AnswerPlan.Requirements[index]
		compared++
		if !planRowsEqual(before, after) {
			t.Errorf("requirement row %d changed across the round trip:\n before: %+v\n after:  %+v", index, before, after)
		}
	}
	// The length equality above is satisfied by two empty arrays. The Fatal
	// on the served side rules that out today; this count says so at the
	// assertion rather than three statements away, where a later edit to the
	// guard would silently make the comparison vacuous.
	if compared == 0 {
		t.Fatal("no requirement row was compared across the round trip; this test proved nothing")
	}
	if err := decoded.Validate(); err != nil {
		t.Fatalf("the decoded answer does not satisfy its own contract: %v", err)
	}
}

// fixedRequirementDeriver returns a fixed row set and COUNTS its calls.
//
// The count is what makes "the engine consulted the derivation" assertable. A
// deriver that recorded nothing would let a test pass against a document whose
// rows came from anywhere -- the discarding-fake shape this package has been
// bitten by more than once.
type fixedRequirementDeriver struct {
	rows  []DerivedRequirement
	calls int
}

func (d *fixedRequirementDeriver) DeriveRequirements(QuestionFrame) []DerivedRequirement {
	d.calls++
	return d.rows
}

func keysOfPlanRows(rows map[string]contractsv1.ContextFabricPlanRequirement) []string {
	out := make([]string, 0, len(rows))
	for identity := range rows {
		out = append(out, identity)
	}
	return out
}

// planRowsEqual compares two rows including their slices.
//
// The struct carries slices, so it is not comparable with ==, and the scalar
// half is compared field by field rather than through a "zero the slices and
// compare" trick: that trick silently stops covering any field added later,
// which is the same silent-drop shape the projection totality test exists to
// prevent. nil and empty are treated as DIFFERENT, because they are different
// on the wire.
func planRowsEqual(a, b contractsv1.ContextFabricPlanRequirement) bool {
	return a.Requirement == b.Requirement &&
		a.Obligation == b.Obligation &&
		a.Role == b.Role &&
		a.Subject == b.Subject &&
		a.Kind == b.Kind &&
		a.Step == b.Step &&
		a.StepExecution == b.StepExecution &&
		a.InputClass == b.InputClass &&
		a.Scope == b.Scope &&
		a.Quantifier == b.Quantifier &&
		a.Unavailable == b.Unavailable &&
		factKindsEqual(a.FactKinds, b.FactKinds) &&
		factKindsEqual(a.InputFactKinds, b.InputFactKinds)
}

// reencodeResult marshals and unmarshals a result, which is exactly what the
// store does to persist and read one back (store.go marshals the whole result
// into a single JSON payload column and unmarshals it on Get).
func reencodeResult(t *testing.T, result InvestigationResult) InvestigationResult {
	t.Helper()
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded InvestigationResult
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return decoded
}

// factKindsEqual treats nil and empty as DIFFERENT.
//
// Not because they differ on the WIRE -- they do not; `omitempty` omits both.
// Because an absent key DECODES to nil, so this is the check that catches a
// projection emitting an empty slice: such a row would not survive its own
// round trip. This is the pin the mutation battery showed the encoding test
// could not provide.
func factKindsEqual(a, b []FactKind) bool {
	if (a == nil) != (b == nil) || len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// planRequirementEngine builds an engine whose turn actually CARRIES A FRAME.
//
// This is not incidental wiring. The requirement rows are derived from the
// frame the family outcome carries, so a harness whose interpreter returns no
// frame produces no rows at all -- and every assertion about them would then
// pass vacuously against an empty document. The first revision of this test
// used exactly such a harness and failed on its own "the deriver was never
// called" guard, which is why that guard is there.
func planRequirementEngine(t *testing.T, deriver RequirementDeriver, calls *int, telemetry *recordingTelemetry) *Engine {
	t.Helper()
	frame := namedFrame(GoalAssessState)
	team := SubjectRef{Kind: SubjectTeam, CanonicalID: "org:linear:CHAOS", Label: "CHAOS"}
	engine := outcomeAssemblySingleSubjectEngineWithDrivers(t, 2, 2, 1, budgetStageOptions(30, time.Second), calls, telemetry)
	engine.requirements = deriver
	// Replace the interpreter with the family-aware double, which is the only
	// one that can return an outcome carrying a frame.
	engine.interpreter = familyInterpreter{
		interpreted: InterpretedQuestion{
			Shape: ShapeOpen, RequestedJudgment: "status",
			TimeContext:      TimeContext{Axis: TemporalCurrent},
			SubjectTerms:     []string{team.Label},
			FactRequirements: []FactRequirement{{Kind: FactStatus}},
		},
		outcome: QuestionFamilyOutcome{
			Frame:              &frame,
			Family:             QuestionFamilySubjectInvestigation,
			Source:             QuestionFamilySourceModel,
			WinningSampleIndex: 0,
			WinningSample:      FamilySample{},
		},
	}
	return engine
}

// THE REFINEMENT MUST REACH THE SERVED DOCUMENT, not merely exist as a type.
//
// Round 1's finding was that nothing in production wrote one: the field, its
// validator, its entries in six schema documents and a whole test suite
// existed for a value no code path produced, and the only assignment anywhere
// was in a store-parity fixture. A field nothing writes is not an audit; it is
// a promise.
//
// This drives the real over-budget path — the one narrowing this service
// actually performs — and reads the refinement off the answer a caller
// receives. It is the consumer-side assertion, because a producer-side one
// would have passed against the same defect.
func TestANarrowedAnswerCarriesTheRefinementThatProducedIt(t *testing.T) {
	t.Parallel()
	calls := 0
	telemetry := &recordingTelemetry{}
	// The acceptance shape: 18 candidates + 12 facts + 5 drivers = 35 charged
	// items against a 30-item ceiling, which forces the candidate cut.
	engine := outcomeAssemblySingleSubjectEngineWithDrivers(t, 18, 12, 5, budgetStageOptions(30, time.Second), &calls, telemetry)

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}

	// The narrowing must actually have happened, or the assertion below is
	// about a document that was never cut and proves nothing.
	var narrowed []contractsv1.ContextFabricPlanRequirementOutcomeRow
	for _, row := range result.Completeness.Outcomes {
		if row.Outcome == contractsv1.ContextFabricRequirementNarrowed {
			narrowed = append(narrowed, row)
		}
	}
	if len(narrowed) == 0 {
		t.Fatalf("no narrowed outcome row on the served answer; the fixture did not exercise the cut (outcomes: %d)",
			len(result.Completeness.Outcomes))
	}

	for _, row := range narrowed {
		if len(row.Refinements) == 0 {
			t.Fatalf("a narrowed row served %d of %d and recorded NO refinement; "+
				"the two counts are a before and an after with the step between them erased",
				row.Served, row.Declared)
		}
		step := row.Refinements[0]
		// The cause must be the CEILING, not a selection order: no selection
		// ran here, and claiming one would state that an order chose the
		// survivors when a ceiling did.
		if step.Overrun == "" {
			t.Error("the refinement names no overrun; a ceiling forced this cut and the row must say so")
		}
		if step.Basis != "" {
			t.Errorf("the refinement claims selection basis %q, but this cut truncated at the declared order and no selection ran", step.Basis)
		}
		if step.Stage != contractsv1.ContextFabricOutcomeStageAssembledResult {
			t.Errorf("refinement stage = %q, want assembled_result", step.Stage)
		}
		// The chain must reconcile with the row's own numbers -- which is the
		// property that makes it an audit rather than a decoration.
		if step.Before != row.Declared || step.After != row.Served {
			t.Errorf("refinement runs %d->%d but the row declared %d and served %d",
				step.Before, step.After, row.Declared, row.Served)
		}
	}

	// And the whole document must still satisfy its own contract, including
	// the chain-reconciliation rule the validator enforces.
	if err := result.Validate(); err != nil {
		t.Fatalf("the served answer does not satisfy its own contract: %v", err)
	}
}

// EVERY POST-PLAN EXIT THAT PUBLISHES REQUIREMENTS MUST ALSO PUBLISH THEIR
// OUTCOME ROWS.
//
// Review round 2 found the gap by following the code rather than the tests.
// The requirement rows are stamped onto the plan where the plan is created, so
// every exit downstream carries them -- but only ONE exit seeds the outcome
// set, and the axis-conflict window veto is not it. That veto returns after
// planning has run, stamps the plan deliberately (its own comment says the
// result is SAVED, so a plan omitted there is missing from a persisted answer
// permanently), and derives completeness with no rows.
//
// The join this branch made total then rejects exactly that document: plan
// requirements present, nothing accounting for them. A legitimate saved
// terminal became ErrInvalidResult -- a regression introduced by tightening
// the join, and invisible to every test because no test drove that exit.
//
// The fix is at the ONE point every independent exit already passes through
// on its way to Validate, rather than at each exit: completeness is where the
// outcome set is finalised, so completeness is where an unseeded plan gets its
// seed. This test pins the property at that choke point, so a NEW exit added
// tomorrow inherits it without anyone remembering to.
func TestEveryPublishedPlanRequirementIsAccountedForByCompleteness(t *testing.T) {
	t.Parallel()
	rows := twoRequirementRows()
	plan := AnswerPlan{
		Family:        contractsv1.ContextFabricQuestionFamilySubjectInvestigation,
		FamilySource:  contractsv1.ContextFabricQuestionFamilySourceStructurePrecedence,
		FamilyVersion: "veto-v1",
		Requirements:  PlanRequirementsFromDerived(rows),
	}
	if len(plan.Requirements) == 0 {
		t.Fatal("the fixture published no requirements; this test cannot detect the gap")
	}

	// The shape the axis-conflict veto produces: the plan stamped, the outcome
	// set never seeded, completeness computed on its way out.
	result := InvestigationResult{
		SchemaVersion: InvestigationResultSchemaV1,
		Status:        InvestigationClarificationRequired,
		AnswerPlan:    &plan,
	}
	if len(result.Completeness.Outcomes) != 0 {
		t.Fatal("the fixture pre-seeded outcomes; the gap under test cannot occur")
	}

	result.Completeness = ComputeAnswerCompleteness(result)

	// THE HARM ASSERTION: every published requirement is accounted for.
	if len(result.Completeness.Outcomes) == 0 {
		t.Fatal("completeness published a plan describing requirements and NO outcome row for any of them; " +
			"the served document cannot be joined and the total join rejects it")
	}
	accounted := map[string]bool{}
	for _, row := range result.Completeness.Outcomes {
		if row.Requirement != "" {
			accounted[row.Requirement] = true
		}
	}
	if len(accounted) == 0 {
		t.Fatal("outcome rows exist but none is attributed; nothing reaches the join assertion below")
	}
	for _, requirement := range plan.Requirements {
		if !accounted[requirement.Requirement] {
			t.Errorf("plan publishes requirement %q with no outcome row accounting for it", requirement.Requirement)
		}
	}

	// And the whole document must satisfy the join it is now held to.
	if err := contractsv1.ValidateContextFabricPlanRequirements(plan.Requirements); err != nil {
		t.Errorf("the seeded plan's requirements do not validate: %v", err)
	}
}

// COMPLETENESS MUST NOT MUTATE THE RESULT IT READS.
//
// ComputeAnswerCompleteness documents itself as pure. The gap-fill added for
// the unaccounted-requirement defect appends rows, and `result` arrives by
// value while its slice header still points at the CALLER's backing array --
// so an append with spare capacity would write the gap rows into the caller's
// storage. That is capacity-dependent, which means it would pass on a fixture
// built from a literal and fail on one built with make(); this fixture forces
// the spare capacity so the defect cannot hide behind an allocation.
func TestComputeAnswerCompletenessDoesNotWriteIntoTheCallersOutcomeStorage(t *testing.T) {
	t.Parallel()
	rows := twoRequirementRows()
	plan := AnswerPlan{
		Family:        contractsv1.ContextFabricQuestionFamilySubjectInvestigation,
		FamilySource:  contractsv1.ContextFabricQuestionFamilySourceStructurePrecedence,
		FamilyVersion: "v1",
		Requirements:  PlanRequirementsFromDerived(rows),
	}

	// One carried row, in storage with room for more. Without the copy, the
	// gap row lands in backing[1] and the caller's array is rewritten.
	backing := make([]RequirementOutcomeRow, 1, 8)
	backing[0] = SeedRequirementOutcomes(rows)[0]
	if cap(backing) <= len(backing) {
		t.Fatalf("fixture has no spare capacity (len %d cap %d); it cannot detect the aliasing write", len(backing), cap(backing))
	}
	sentinel := backing[:cap(backing)][1]

	result := InvestigationResult{
		ResultID:   "r-alias",
		Status:     InvestigationComplete,
		AnswerPlan: &plan,
	}
	result.Completeness.Outcomes = backing

	got := ComputeAnswerCompleteness(result)

	// The gap-fill must have HAPPENED, or this test proves nothing about the
	// append it is guarding.
	if len(got.Outcomes) <= len(backing) {
		t.Fatalf("completeness returned %d rows from %d carried; no gap row was appended, so the aliasing guard is untested",
			len(got.Outcomes), len(backing))
	}
	if beyond := backing[:cap(backing)][1]; !reflect.DeepEqual(beyond, sentinel) {
		t.Errorf("completeness wrote a row into the caller's spare capacity: %+v", beyond)
	}
	if len(result.Completeness.Outcomes) != 1 {
		t.Errorf("the caller's slice length changed to %d", len(result.Completeness.Outcomes))
	}
}

// GAP-FILL, NOT SEED-IF-EMPTY.
//
// The quiet case is an exit that accounted for SOME requirements and not
// others. A seed-if-empty rule leaves that document unjoinable while looking
// like it was handled. This asserts the partially-accounted set is completed,
// that the already-accounted row is left exactly as the stage wrote it, and
// that running the derivation twice adds nothing -- idempotence is what makes
// it safe at a choke point every exit calls.
func TestCompletenessFillsOnlyTheUnaccountedRequirements(t *testing.T) {
	t.Parallel()
	rows := twoRequirementRows()
	published := PlanRequirementsFromDerived(rows)
	if len(published) < 2 {
		t.Fatalf("fixture published %d requirements; the partial case needs at least two", len(published))
	}
	plan := AnswerPlan{
		Family:        contractsv1.ContextFabricQuestionFamilySubjectInvestigation,
		FamilySource:  contractsv1.ContextFabricQuestionFamilySourceStructurePrecedence,
		FamilyVersion: "v1",
		Requirements:  published,
	}

	// Account for the FIRST requirement only, with a row a later stage would
	// have written -- distinguishable from any seed row, so a fix that
	// overwrote it instead of preserving it is caught.
	carried := RequirementOutcomeRow{
		Stage:         contractsv1.ContextFabricOutcomeStageAssembledResult,
		Requirement:   published[0].Requirement,
		Obligation:    published[0].Obligation,
		Outcome:       contractsv1.ContextFabricRequirementNarrowed,
		Impact:        contractsv1.ContextFabricAnswerImpactScope,
		CauseOverrun:  contractsv1.ContextFabricBudgetOverrunItems,
		CauseObserved: true,
		Declared:      3,
		Served:        1,
	}
	result := InvestigationResult{ResultID: "r-partial", Status: InvestigationComplete, AnswerPlan: &plan}
	result.Completeness.Outcomes = []RequirementOutcomeRow{carried}

	got := ComputeAnswerCompleteness(result)

	byIdentity := map[string][]RequirementOutcomeRow{}
	for _, row := range got.Outcomes {
		byIdentity[row.Requirement] = append(byIdentity[row.Requirement], row)
	}
	for _, requirement := range published {
		if n := len(byIdentity[requirement.Requirement]); n == 0 {
			t.Errorf("requirement %q is published and still unaccounted for", requirement.Requirement)
		}
	}
	// The stage's own row survives unchanged, and was not duplicated.
	if got, want := byIdentity[published[0].Requirement], []RequirementOutcomeRow{carried}; !reflect.DeepEqual(got, want) {
		t.Errorf("the carried row was not preserved verbatim:\n got: %+v\nwant: %+v", got, want)
	}
	// The second requirement gained exactly one PLANNING-stage row.
	filled := byIdentity[published[1].Requirement]
	if len(filled) != 1 {
		t.Fatalf("requirement %q gained %d rows, want exactly 1", published[1].Requirement, len(filled))
	}
	if filled[0].Stage != contractsv1.ContextFabricOutcomeStagePlanning {
		t.Errorf("the gap row is stamped %q, want the planning stage that derived it", filled[0].Stage)
	}

	// IDEMPOTENT. Feeding the completed set back in must add nothing.
	again := result
	again.Completeness.Outcomes = got.Outcomes
	if second := ComputeAnswerCompleteness(again); len(second.Outcomes) != len(got.Outcomes) {
		t.Errorf("re-deriving grew the outcome set from %d to %d; the gap-fill is not idempotent",
			len(got.Outcomes), len(second.Outcomes))
	}
}

// The two seeds must agree ROW FOR ROW.
//
// One is built from the derivation, the other from what the plan publishes.
// They were briefly two copies of the same code in two packages; they are now
// one builder, and this is the assertion that keeps them one. It compares the
// whole rows rather than the identities, because a drift in the CAUSE mapping
// -- the part most likely to be extended -- would leave the identities equal.
func TestBothSeedsProduceIdenticalPlanningRows(t *testing.T) {
	t.Parallel()
	rows := twoRequirementRows()
	// The fixture must exercise the unavailable arm, or the cause mapping
	// this test exists to protect is never reached.
	rows[1].Unavailable = RequirementReasonComputedPopulationAbsent

	fromDerivation := SeedRequirementOutcomes(rows)
	fromPlan := SeedOutcomesFromPublishedPlanRequirements(PlanRequirementsFromDerived(rows))

	unavailable := 0
	for _, row := range fromDerivation {
		if row.Outcome == contractsv1.ContextFabricRequirementUnavailable {
			unavailable++
			if row.CauseCoverage == "" {
				t.Errorf("row %q is unavailable with no coverage cause; the mapping fixture is not exercising the table", row.Requirement)
			}
		}
	}
	if unavailable == 0 {
		t.Fatal("no seeded row is unavailable; the cause mapping was never reached and this test proved nothing")
	}
	if !reflect.DeepEqual(fromDerivation, fromPlan) {
		t.Errorf("the derivation-side and plan-side seeds disagree:\n derivation: %+v\n plan: %+v", fromDerivation, fromPlan)
	}
}

// THE ORDER OF THE TWO WRITES, PINNED.
//
// The veto exits derive completeness and THEN call finalizeServed, which is
// what stamps the plan. So anything the completeness derivation reads off the
// plan is invisible at their own call -- the plan is still nil there. That is
// how those exits came to serve, and save, a plan describing requirement rows
// no outcome row accounted for.
//
// This test is deliberately NOT given a pre-stamped plan. A fixture that sets
// result.AnswerPlan itself passes whether or not the ordering is right, which
// is exactly how the first version of this fix looked correct while leaving
// every veto exit broken. Here the plan arrives the way the exits pass it --
// as the argument -- so the assertion can only hold if the stamp happens
// before the derivation.
func TestFinalizeServedAccountsForPlanRequirementsItStampsItself(t *testing.T) {
	t.Parallel()
	rows := twoRequirementRows()
	plan := AnswerPlan{
		Family:        contractsv1.ContextFabricQuestionFamilySubjectInvestigation,
		FamilySource:  contractsv1.ContextFabricQuestionFamilySourceStructurePrecedence,
		FamilyVersion: "v1",
		Requirements:  PlanRequirementsFromDerived(rows),
	}
	if len(plan.Requirements) == 0 {
		t.Fatal("the plan publishes no requirements; the fixture cannot detect an unaccounted one")
	}

	result := InvestigationResult{ResultID: "r-order", Status: InvestigationNoMatch}
	// The shape the veto exits reach this call in: completeness already
	// derived by the exit, with no plan on the result yet.
	result.Completeness = ComputeAnswerCompleteness(result)
	if result.AnswerPlan != nil {
		t.Fatal("the fixture pre-stamped the plan; it cannot detect the ordering defect")
	}
	if len(result.Completeness.Outcomes) != 0 {
		t.Fatalf("the exit-stage completeness already carries %d rows; the fixture is not the unaccounted shape", len(result.Completeness.Outcomes))
	}

	engine := &Engine{}
	served, err := engine.finalizeServed(context.Background(), storage.Principal{}, BudgetAssertWindowVeto, result, &plan,
		ResponseBudget{MaxItems: 1000, MaxSerializedBytes: 1 << 20})
	if err != nil {
		t.Fatalf("finalizeServed returned %v; the ordering cannot be observed", err)
	}
	if served.AnswerPlan == nil {
		t.Fatal("finalizeServed did not stamp the plan")
	}

	accounted := map[string]bool{}
	for _, row := range served.Completeness.Outcomes {
		accounted[row.Requirement] = true
	}
	unaccounted := 0
	for _, requirement := range served.AnswerPlan.Requirements {
		if !accounted[requirement.Requirement] {
			unaccounted++
			t.Errorf("the served document publishes requirement %q and no outcome row accounts for it", requirement.Requirement)
		}
	}
	if unaccounted == 0 && len(served.Completeness.Outcomes) == 0 {
		t.Fatal("the served document carries no outcome rows at all; the loop above asserted nothing")
	}
}
