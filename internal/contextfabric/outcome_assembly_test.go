package contextfabric

import (
	"context"
	"errors"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The acceptance for outcome-driven assembly, at the request shape that
// refuses today: a SINGLE SUBJECT, no cohort, a single-table time_series
// read, measured over the item ceiling.
//
// The mechanism this pins is narrow and deterministic. Stage 3's only
// narrowing lever is the cohort (narrowSynthesisInput returns Narrow:false
// when graph.Cohort is nil), so a single-subject question reaches the
// terminal with its entire narrowing repertoire empty -- no content reduction
// is ever attempted -- while the resolution candidate list, which IS charged
// against the item budget, sits untouched. Decision D5's own header records
// that candidates are the term re-synthesis cannot reach; this is that
// paragraph happening to a real question.
//
// RED at the fix parent: Investigate returns ErrAnswerExceedsBudget and the
// route serves 413. GREEN here: the answer is served, the reduction is a
// NAMED outcome row, and the served completeness says the answer is partial.

// outcomeAssemblyCandidates builds n resolution candidates for a single
// named subject. They are alternatives the resolver did not commit to, so
// nothing in the composed answer cites them -- which is what makes them the
// one term assembly may reduce without orphaning prose.
func outcomeAssemblyCandidates(n int) []SubjectCandidate {
	candidates := make([]SubjectCandidate, 0, n)
	for index := 0; index < n; index++ {
		suffix := string(rune('a'+index/26)) + string(rune('a'+index%26))
		candidates = append(candidates, SubjectCandidate{
			ReceiptID: "receipt_cand_" + suffix,
			Subject: SubjectRef{
				Kind: SubjectTeam, CanonicalID: "org:linear:TEAM" + suffix, Label: "Team " + suffix,
			},
			State:        ResolutionProposed,
			MatchReasons: []string{"Name matched the requested subject term."},
			Confidence:   0.5,
		})
	}
	return candidates
}

// outcomeAssemblySingleSubjectEngine reproduces the 4754 shape: one committed
// team, NO cohort, `candidates` unresolved alternatives, and `facts`
// single-table time_series claimed facts about that one team.
func outcomeAssemblySingleSubjectEngine(t *testing.T, candidates, facts int, options EngineOptions, calls *int, telemetry *recordingTelemetry) *Engine {
	return outcomeAssemblySingleSubjectEngineWithDrivers(t, candidates, facts, 0, options, calls, telemetry)
}

// outcomeAssemblyDrivers builds n driver judgments about the one committed
// team, in the shape the measurement probe beside this file measured the
// acceptance question at.
//
// Drivers matter to the fixture for one reason: they are a charged term the
// reduction may NOT touch. A driver is cited by the composed judgment, so
// dropping one leaves prose describing content that is no longer present.
// A fixture with zero drivers cannot show that the reduction left them
// alone, because there is nothing there to leave alone.
// outcomeAssemblyEvidenceRef is a real, contract-shaped evidence reference.
// A made-up token like "ev_0001" measures fine and fails validation, which
// is how the measurement probe's fixture got away with one.
const outcomeAssemblyEvidenceRef = "acr:v1:pull-request:PR-7"

// claimIDs is the driver's canonical-observation leg, taken from the claims
// the synthesizer actually emitted rather than typed out beside them -- a
// hand-written id is one rename away from citing a fact that is not there.
func claimIDs(claims []ClaimedFact) []string {
	ids := make([]string, 0, len(claims))
	for _, claim := range claims {
		ids = append(ids, claim.ClaimID)
	}
	return ids
}

func outcomeAssemblyDrivers(n int, subject SubjectRef, claimIDs []string) []DriverJudgment {
	drivers := make([]DriverJudgment, 0, n)
	for index := 0; index < n; index++ {
		drivers = append(drivers, DriverJudgment{
			DriverID: "driver_00" + string(rune('a'+index%26)),
			Standing: DriverPrincipal, Category: string(contractsv1.ContextFabricDriverCategoryStatus),
			Title:            "Cycle time widened in the most recent periods",
			Summary:          "Items closed per period fell while items opened held steady, widening the queue.",
			AffectedSubjects: []SubjectRef{subject},
			EvidenceRefIDs:   []string{outcomeAssemblyEvidenceRef},
			// `status` is one of the categories the contract requires a
			// CLAIMED FACT for -- the canonical-observation leg of the
			// evidence distinction. A driver citing nothing would be
			// rejected, which is the gate this fixture has to satisfy
			// rather than route around.
			ClaimedFactIDs: claimIDs[:1],
			Derivation:     DerivationCanonicalStructured, EpistemicStatus: EpistemicObserved,
			Confidence: 0.71, Current: true,
		})
	}
	return drivers
}

func outcomeAssemblySingleSubjectEngineWithDrivers(t *testing.T, candidates, facts, drivers int, options EngineOptions, calls *int, telemetry *recordingTelemetry) *Engine {
	t.Helper()
	team := SubjectRef{Kind: SubjectTeam, CanonicalID: "org:linear:CHAOS", Label: "CHAOS"}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return InterpretedQuestion{
				Shape: ShapeOpen, RequestedJudgment: "status",
				TimeContext:      TimeContext{Axis: TemporalCurrent},
				FactRequirements: []FactRequirement{{Kind: FactStatus}},
			}, nil
		}),
		Graph: &capturingGraphReader{
			resolution: SubjectResolution{
				Candidates: outcomeAssemblyCandidates(candidates),
				Committed:  []SubjectRef{team},
			},
			context: GraphContext{
				// Cohort is nil. This is the whole point: a single-subject
				// question has nothing for the cohort lever to act on.
				Cohort: nil,
				Paths:  []RelationshipPath{}, DriverCandidates: []DriverJudgment{},
				FactRequirements: []FactRequirement{}, EvidenceRefIDs: []string{},
				Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
			},
		},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{
				Facts: []CanonicalFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				Version: "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{},
			}, nil
		}),
		Synthesizer: synthesizerFunc(func(_ context.Context, _ storage.Principal, _ SynthesisInput) (InvestigationResult, error) {
			*calls++
			claims := make([]ClaimedFact, 0, facts)
			for index := 0; index < facts; index++ {
				claims = append(claims, ClaimedFact{
					ClaimID: "claim_workload_" + string(rune('a'+index%26)),
					Kind:    FactStatus, Subject: team, Field: "status",
					Value: ScalarValue{String: ptrString("green")},
				})
			}
			return InvestigationResult{
				Status: InvestigationComplete, DirectJudgment: "Throughput held roughly flat.",
				CurrentState:       "Within the band observed over the window.",
				StrongestPressures: []string{}, Drivers: outcomeAssemblyDrivers(drivers, team, claimIDs(claims)), RemainingWork: []Finding{},
				ReadinessGaps: []Finding{}, Paths: []RelationshipPath{}, Conflicts: []Finding{},
				Limitations: []string{}, EvidenceRefIDs: []string{outcomeAssemblyEvidenceRef}, ClaimedFacts: claims,
				Coverage:            Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				DeterministicAnswer: "Throughput held roughly flat over the requested window.",
				Warnings:            []string{},
				Versions: VersionSet{
					Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
					InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1",
				},
			}, nil
		}),
		Telemetry: telemetry,
	}, options)
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	return engine
}

func TestSingleSubjectOverBudgetIsNarrowedAndDisclosedNotRefused(t *testing.T) {
	t.Parallel()
	calls := 0
	telemetry := &recordingTelemetry{}
	// THE ACCEPTANCE SHAPE, EXACTLY AS FILED: 18 resolution candidates + 12
	// claimed facts + 5 drivers = 35 charged items against a 30-item
	// ceiling, which is the split the measurement probe beside this file
	// measured.
	//
	// It is the same input the FAIL-at-parent run uses, deliberately. An
	// earlier revision proved the parent's refusal on 18c/12f/5d and this
	// pass on 23c/12f/0d -- two different documents, so the pair showed that
	// two things happen, not that ONE thing changed. Drivers at 5 also give
	// the reduction something it must NOT touch: a driver is cited by the
	// composed judgment, and the assertion below that all 5 survive is only
	// meaningful when there are 5 to survive.
	//
	// The arithmetic the reduction performs, stated so a fixture edit cannot
	// silently stop exercising it: fixed = 35 - 18 = 17 charged items the
	// cut cannot reach, so allowance = 30 - 17 = 13 candidates, and the
	// served document is 13 + 12 + 5 = 30 items exactly.
	engine := outcomeAssemblySingleSubjectEngineWithDrivers(t, 18, 12, 5, budgetStageOptions(30, time.Second), &calls, telemetry)

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow())
	if errors.Is(err, ErrAnswerExceedsBudget) {
		t.Fatalf("Investigate() refused with a budget refusal; a fresh investigation must be narrowed and disclosed, never refused, against the effective budget: %v", err)
	}
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}

	measurement, err := contractsv1.MeasureContextFabricResponse(result)
	if err != nil {
		t.Fatalf("MeasureContextFabricResponse() error = %v", err)
	}
	t.Logf("served item split: candidates=%d facts=%d drivers=%d budgeted=%d bytes=%d",
		measurement.Items.Candidates, measurement.Items.ClaimedFacts, measurement.Items.Drivers,
		measurement.Items.Budgeted(), measurement.Bytes)
	if measurement.Items.Budgeted() > 30 {
		t.Fatalf("served %d budgeted items against a 30-item ceiling", measurement.Items.Budgeted())
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("the served result does not validate: %v", err)
	}

	// The reduction is DISCLOSED BY NAME, not by a count a reader has to
	// infer a class from.
	if len(result.Completeness.Outcomes) == 0 {
		t.Fatal("the answer was narrowed and carries no outcome row; a narrowing the caller is not told about is the silent truncation this seam removes")
	}
	var narrowed *contractsv1.ContextFabricPlanRequirementOutcomeRow
	for index := range result.Completeness.Outcomes {
		row := &result.Completeness.Outcomes[index]
		if row.Outcome == contractsv1.ContextFabricRequirementNarrowed {
			narrowed = row
		}
		if !contractsv1.ValidContextFabricPlanRequirementOutcome(row.Outcome) {
			t.Fatalf("outcome %q is not a vocabulary member", row.Outcome)
		}
		if !contractsv1.ValidContextFabricAnswerImpactKind(row.Impact) {
			t.Fatalf("impact %q is not a vocabulary member", row.Impact)
		}
	}
	if narrowed == nil {
		t.Fatal("no outcome row reports a narrowing, but the served answer is smaller than the assembled one")
	}
	if narrowed.Impact != contractsv1.ContextFabricAnswerImpactScope {
		t.Fatalf("Impact = %q, want scope -- fewer subjects reached the caller than the resolver found", narrowed.Impact)
	}
	if narrowed.CauseOverrun != contractsv1.ContextFabricBudgetOverrunItems {
		t.Fatalf("CauseOverrun = %q, want items", narrowed.CauseOverrun)
	}
	if narrowed.Stage != contractsv1.ContextFabricOutcomeStageAssembledResult {
		t.Fatalf("Stage = %q, want assembled_result", narrowed.Stage)
	}
	if narrowed.Declared != 18 || narrowed.Served != 13 {
		t.Fatalf("served/declared = %d/%d, want 13/18 -- the exact allowance the ceiling leaves once the 17 charged items the cut cannot reach are subtracted", narrowed.Served, narrowed.Declared)
	}
	// The terms the reduction may NOT touch, asserted rather than assumed.
	// Dropping a driver or a claimed fact would leave the composed judgment
	// describing content that is no longer present, which is the one defect
	// this seam is forbidden to introduce.
	if measurement.Items.Drivers != 5 {
		t.Fatalf("served %d drivers, want all 5 -- a driver is cited by the composed judgment and the reduction may not reach it", measurement.Items.Drivers)
	}
	if measurement.Items.ClaimedFacts != 12 {
		t.Fatalf("served %d claimed facts, want all 12 -- the reduction cuts candidates and nothing else", measurement.Items.ClaimedFacts)
	}
	if measurement.Items.Candidates != 13 {
		t.Fatalf("served %d candidates, want 13", measurement.Items.Candidates)
	}
	if measurement.Items.Budgeted() != 30 {
		t.Fatalf("served %d budgeted items, want exactly 30 -- the reduction is arithmetic, not a search that stops when it fits", measurement.Items.Budgeted())
	}
	if !narrowed.CauseObserved {
		t.Fatal("CauseObserved is false on a cause this stage itself computed; a defaulted cause must never read as an observed one")
	}

	// Completeness is DERIVED from the outcome set, at the surface that
	// serves the answer -- never copied from a census taken before the
	// document was cut.
	if result.Completeness.State != contractsv1.ContextFabricAnswerCompletenessPartial {
		t.Fatalf("Completeness.State = %q, want partial", result.Completeness.State)
	}
	if calls != 1 {
		t.Fatalf("synthesizer called %d times; narrowing the candidate list must not re-run synthesis", calls)
	}
}

// The wire mirror of the answer-obligation vocabulary must equal the domain's
// own, IN BOTH DIRECTIONS.
//
// One direction alone is the failure this guards against: checking only that
// every domain member reaches the wire lets a mirror entry outlive the member
// it mirrors, and checking only the reverse lets a new obligation ship with
// no wire representation and a validator that rejects it. The fact-scope
// vocabularies above it are held to exactly this.
func TestTheWireObligationVocabularyMirrorsTheDomainInBothDirections(t *testing.T) {
	t.Parallel()
	domain := map[string]bool{}
	for _, obligation := range AnswerObligationVocabulary() {
		domain[string(obligation)] = true
	}
	wire := map[string]bool{}
	for _, obligation := range contractsv1.ContextFabricAnswerObligationVocabulary() {
		wire[obligation] = true
	}
	for member := range domain {
		if !wire[member] {
			t.Errorf("obligation %q exists in the domain and not on the wire; a requirement outcome naming it would be rejected by its own validator", member)
		}
	}
	for member := range wire {
		if !domain[member] {
			t.Errorf("the wire mirror carries %q, which is not an obligation; a mirror entry has outlived the member it mirrors", member)
		}
	}
	if len(domain) != len(wire) {
		t.Fatalf("domain has %d obligations, the wire mirror has %d", len(domain), len(wire))
	}
}

// Every member of the outcome vocabulary is either PRODUCED by this package
// or DECLARED unreachable by name.
//
// A vocabulary member no producer can reach is not a member -- it is a
// promise. Naming the unreachable ones explicitly is what makes the next one
// fail here instead of joining a list nobody maintains.
func TestEveryOutcomeTokenIsProducedOrDeclaredUnreachable(t *testing.T) {
	t.Parallel()
	// not_attempted is UNREACHABLE in this slice, and that is a fact about
	// the code rather than an omission. It names a requirement a declared
	// cap prevented the engine from ever attempting, and the step that
	// refines requirement rows after subject resolution -- the only place
	// the pre-read cardinality clamp could be attached to a requirement --
	// is owned by the requirement-derivation seam and has not landed. The
	// token ships now because the vocabulary is closed and a later addition
	// to a closed enum is the expensive kind of change; it is asserted
	// unreachable so that the day it becomes reachable, this line fails and
	// a person decides.
	unreachable := map[contractsv1.ContextFabricPlanRequirementOutcome]string{
		contractsv1.ContextFabricRequirementNotAttempted:  "needs the post-resolution requirement refinement step, which is another seam's to build",
		contractsv1.ContextFabricRequirementNotApplicable: "needs a requirement the question did not ask for, which only the refinement step can distinguish from one it did",
	}

	produced := map[contractsv1.ContextFabricPlanRequirementOutcome]bool{}
	// satisfied and unavailable are what seeding produces, from a served
	// and an unserved requirement row respectively.
	for _, served := range []bool{true, false} {
		requirement := DerivedRequirement{
			RequirementCoordinate: RequirementCoordinate{
				Obligation: ObligationState, Role: SubjectRoleSubject, Subject: SubjectTeam,
			},
		}
		if !served {
			requirement.Unavailable = RequirementReasonNoDeclaringProducer
		}
		for _, row := range seedRequirementOutcomes(&QuestionFrame{}, stubRequirementDeriver{rows: []DerivedRequirement{requirement}}) {
			produced[row.Outcome] = true
		}
	}
	// narrowed is what the assembly-stage reduction produces.
	produced[candidateNarrowingOutcomeRow(
		candidateNarrowing{Served: 1, Declared: 2, Narrowed: true},
		contractsv1.ContextFabricBudgetOverrunItems, "", "").Outcome] = true

	for _, token := range contractsv1.ContextFabricPlanRequirementOutcomeVocabulary() {
		reason, declared := unreachable[token]
		switch {
		case produced[token] && declared:
			t.Errorf("outcome %q is declared unreachable (%q) but this package produces it; remove the declaration", token, reason)
		case !produced[token] && !declared:
			t.Errorf("outcome %q is neither produced by this package nor declared unreachable; a vocabulary member no producer can reach is a promise, not a member", token)
		}
	}
}

// stubRequirementDeriver returns a fixed row set, so the seeding path can be
// exercised without a live registry.
type stubRequirementDeriver struct{ rows []DerivedRequirement }

func (s stubRequirementDeriver) DeriveRequirements(QuestionFrame) []DerivedRequirement { return s.rows }

// Seeding must be HONEST about its own absence: no frame, or no deriver, and
// the answer says its outcomes were not derived rather than that nothing was
// lost.
func TestAnAnswerWithNoDerivedRequirementsSaysSoRatherThanClaimingCompleteness(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name    string
		frame   *QuestionFrame
		deriver RequirementDeriver
	}{
		{"no frame", nil, stubRequirementDeriver{rows: []DerivedRequirement{{}}}},
		{"no deriver", &QuestionFrame{}, nil},
		{"a deriver that derives nothing", &QuestionFrame{}, stubRequirementDeriver{}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			rows := seedRequirementOutcomes(testCase.frame, testCase.deriver)
			if len(rows) != 0 {
				t.Fatalf("seeded %d rows, want none", len(rows))
			}
			if state := contractsv1.DeriveContextFabricAnswerCompletenessState(rows); state != contractsv1.ContextFabricAnswerCompletenessNotDerived {
				t.Fatalf("state = %q, want not_derived -- an answer whose outcomes were never derived must not claim that nothing was lost", state)
			}
		})
	}
}
