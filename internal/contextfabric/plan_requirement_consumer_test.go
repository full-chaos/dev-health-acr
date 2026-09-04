package contextfabric

import (
	"context"
	"encoding/json"
	"errors"
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
	if len(read.FactKinds) == 0 {
		t.Error("the served read row names no fact kinds; what could serve it is exactly what the row exists to say")
	}

	// THE JOIN, on the served document: every outcome row's identity must
	// resolve to a requirement the same document describes.
	if len(result.Completeness.Outcomes) == 0 {
		t.Fatal("the served answer carries no outcome rows; the join has nothing to check")
	}
	for _, outcome := range result.Completeness.Outcomes {
		if outcome.Requirement == "" {
			continue
		}
		if _, present := byIdentity[outcome.Requirement]; !present {
			t.Errorf("the served answer names outcome for requirement %q, which its own plan does not describe", outcome.Requirement)
		}
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
	for index, before := range served.AnswerPlan.Requirements {
		after := decoded.AnswerPlan.Requirements[index]
		if !planRowsEqual(before, after) {
			t.Errorf("requirement row %d changed across the round trip:\n before: %+v\n after:  %+v", index, before, after)
		}
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
