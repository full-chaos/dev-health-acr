package contextfabric

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-6561: a cohort answer the plan narrowed must SAY so, in plain terms,
// on the served document -- from how many members retrieval found, to how many
// the answer carries, and which recorded steps cut them -- without changing
// any number the answer serves.
//
// The prod acceptance shape (09-f2b): a grouped question (team -> members),
// 11 members found, the requested member limit 20 clamped to 10 by the item
// budget before retrieval, then the assembled answer over its item budget and
// re-synthesized over 5. The answer carried `served 5 / declared 11` with
// cause `fact_narrowed`, cause_observed false, and no statement of either
// step.
//
// Driven through the REAL engine: a validated grouped frame, the requirement
// deriver, discovery that honours MaxCohortMembers and counts the pool, and a
// synthesizer whose answer grows with the members it is handed -- so the
// clamp, the overrun and the retry are production's own.

const chaos6561MemberRequirement = "state/member/project"

// chaos6561PopulationGraph behaves like discovery: it honours the request's
// MaxCohortMembers and reports how many members the pool held.
type chaos6561PopulationGraph struct {
	*capturingGraphReader
	population int
}

func (g chaos6561PopulationGraph) DiscoverContext(ctx context.Context, principal storage.Principal, request GraphDiscoveryRequest) (GraphContext, error) {
	graph, err := g.capturingGraphReader.DiscoverContext(ctx, principal, request)
	if err != nil {
		return graph, err
	}
	cohort := budgetStageCohort(g.population)
	if limit := request.Request.Options.MaxCohortMembers; limit > 0 && limit < len(cohort.Members) {
		cohort.Members = cohort.Members[:limit]
		cohort.Complete = false
		cohort.Truncated = true
	}
	graph.Cohort = cohort
	graph.CohortPopulation = g.population
	return graph, nil
}

// chaos6561Investigate runs one investigation over a population of
// `population` projects grouped by team, with `claims` claimed facts per member
// the synthesizer is handed, under `maxItems`, requesting a member limit of 20
// (the MCP default).
func chaos6561Investigate(t *testing.T, population, claims, maxItems int, telemetry *recordingTelemetry) (InvestigationResult, int) {
	t.Helper()
	result, calls, err := chaos6561InvestigateWithin(t, population, claims, maxItems, 0, telemetry)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	return result, calls
}

// chaos6561InvestigateWithin is chaos6561Investigate with a serialized-byte
// ceiling (0 = none) and the engine's error returned rather than fatal, so a
// caller can assert on a refusal.
func chaos6561InvestigateWithin(t *testing.T, population, claims, maxItems int, maxBytes int64, telemetry *recordingTelemetry) (InvestigationResult, int, error) {
	t.Helper()
	frame, _ := boundaryGroupedFrame(t, SubjectTeam, SubjectProject)
	// Two independent kinds serve `state`, so the kind-level evaluation is
	// lossless and the row reaches the POPULATION arm -- the arm that
	// published the prod row.
	stateReader := func(kind FactKind) FactCapability {
		return FactCapability{
			Kind:                  kind,
			SupportedSubjectKinds: []SubjectKind{SubjectProject, SubjectTeam},
			Obligations:           map[SubjectKind][]AnswerObligation{SubjectProject: {ObligationState}, SubjectTeam: {ObligationState}},
		}
	}
	deriver := registryDeriver{capabilities: []FactCapability{stateReader(FactHealth), stateReader(FactFlow)}}
	pool := budgetStageCohort(population)
	calls := 0
	if telemetry == nil {
		telemetry = &recordingTelemetry{}
	}
	options := budgetStageOptions(maxItems, time.Second)
	options.MaxSerializedBytes = maxBytes
	engine, err := NewEngine(EngineDependencies{
		Interpreter: familyInterpreter{
			interpreted: InterpretedQuestion{
				Shape: ShapeDiscoveredCohort, RequestedJudgment: "status",
				TimeContext:      TimeContext{Axis: TemporalCurrent},
				FactRequirements: []FactRequirement{{Kind: FactStatus}},
			},
			outcome: QuestionFamilyOutcome{Frame: &frame, Family: QuestionFamilyUnclassified, Source: QuestionFamilySourceModel},
		},
		Graph: chaos6561PopulationGraph{
			capturingGraphReader: &capturingGraphReader{
				resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
				context: GraphContext{
					Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{},
					FactRequirements: []FactRequirement{}, EvidenceRefIDs: []string{},
					Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				},
			},
			population: population,
		},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			bundle := CanonicalFactBundle{
				Facts: []CanonicalFact{},
				Coverage: Coverage{Sources: []SourceObservation{
					{Source: canonicalFactSourcePrefix + string(FactHealth), State: SourceAvailable},
					{Source: canonicalFactSourcePrefix + string(FactFlow), State: SourceAvailable},
				}, DegradedReasons: []string{}},
				Version: "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{},
			}
			for _, member := range pool.Members {
				bundle.Facts = append(bundle.Facts,
					CanonicalFact{Kind: FactHealth, Subject: member.Subject, SourceState: SourceAvailable},
					CanonicalFact{Kind: FactFlow, Subject: member.Subject, SourceState: SourceAvailable})
			}
			return bundle, nil
		}),
		Synthesizer: synthesizerFunc(func(_ context.Context, _ storage.Principal, input SynthesisInput) (InvestigationResult, error) {
			calls++
			facts := []ClaimedFact{}
			if input.Graph.Cohort != nil {
				for _, member := range input.Graph.Cohort.Members {
					for claim := 0; claim < claims; claim++ {
						facts = append(facts, ClaimedFact{
							ClaimID: "claim_" + member.Subject.CanonicalID + "_" + string(rune('0'+claim)),
							Kind:    FactStatus, Subject: member.Subject, Field: "status",
							Value: ScalarValue{String: ptrString("green")},
						})
					}
				}
			}
			return InvestigationResult{
				Status: InvestigationComplete, DirectJudgment: "Fine.", CurrentState: "Nominal.",
				StrongestPressures: []string{}, Drivers: []DriverJudgment{}, RemainingWork: []Finding{},
				ReadinessGaps: []Finding{}, Paths: []RelationshipPath{}, Conflicts: []Finding{},
				Limitations: []string{}, EvidenceRefIDs: []string{}, ClaimedFacts: facts,
				Coverage:            input.Facts.Coverage,
				DeterministicAnswer: "Fine, based on available context.", Warnings: []string{},
				Versions: VersionSet{
					Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
					InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1",
				},
			}, nil
		}),
		Requirements: deriver,
		Telemetry:    telemetry,
	}, options)
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	request := validInvestigationRequestWithConfirmedWindow()
	request.Options.MaxCohortMembers = 20
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_6561"}, request)
	return result, calls, err
}

// chaos6561HeadroomItems returns the item budget at which this fixture's plan
// clamps the member limit to exactly `members` (MaxMembers = MaxItems -
// headroom), probed through the engine rather than assumed.
func chaos6561HeadroomItems(t *testing.T, members int) int {
	t.Helper()
	probed, _ := chaos6561Investigate(t, 3, 0, 1000, nil)
	if probed.AnswerPlan == nil {
		t.Fatal("probe served no plan")
	}
	return probed.AnswerPlan.Budget.SynthesisHeadroom + members
}

func chaos6561MemberSteps(plan *AnswerPlan) []PlanNarrowing {
	if plan == nil {
		return nil
	}
	var steps []PlanNarrowing
	for _, step := range plan.Narrowing {
		if !step.Groups {
			steps = append(steps, step)
		}
	}
	return steps
}

// chaos6561NarrowingLimitations finds the sentence by its plain-text opening,
// not by the new recogniser, so this file also runs on the baseline tree.
func chaos6561NarrowingLimitations(result InvestigationResult) []string {
	var found []string
	for _, limitation := range result.Limitations {
		if strings.HasPrefix(limitation, "This answer lists ") && strings.Contains(limitation, "narrowed to fit the response budget") {
			found = append(found, limitation)
		}
	}
	return found
}

func chaos6561AssembledRow(t *testing.T, result InvestigationResult, requirement string) RequirementOutcomeRow {
	t.Helper()
	for _, row := range result.Completeness.Outcomes {
		if row.Requirement == requirement && row.Stage == contractsv1.ContextFabricOutcomeStageAssembledResult {
			return row
		}
	}
	t.Fatalf("no assembled_result row for %q: %+v", requirement, result.Completeness.Outcomes)
	return RequirementOutcomeRow{}
}

// TestCHAOS6561TwoStepNarrowingIsDisclosedFromFoundToServed is the prod
// shape: 11 found; limit 20 -> 10 before retrieval; 10 -> 5 after the
// assembled answer exceeded its item budget.
func TestCHAOS6561TwoStepNarrowingIsDisclosedFromFoundToServed(t *testing.T) {
	t.Parallel()
	maxItems := chaos6561HeadroomItems(t, 10)
	// Claims per member so 10 members overrun and 5 fit:
	// 10*(1+c) > maxItems >= 5*(1+c).
	claims := -1
	for c := 0; c < 50; c++ {
		if 10*(1+c) > maxItems && 5*(1+c) <= maxItems {
			claims = c
			break
		}
	}
	if claims < 0 {
		t.Fatalf("fixture defect: no claims-per-member gives 10 over / 5 fit at maxItems=%d", maxItems)
	}
	result, calls := chaos6561Investigate(t, 11, claims, maxItems, nil)
	if calls != 2 {
		t.Fatalf("synthesizer called %d times, want 2 -- the prod shape needs the stage-3 retry", calls)
	}

	// NO NUMBER CHANGES: the plan's own steps, and the served cohort, are the
	// prod shape.
	steps := chaos6561MemberSteps(result.AnswerPlan)
	if len(steps) != 2 ||
		steps[0].Stage != contractsv1.ContextFabricPlanNarrowingCardinality || steps[0].Before != 20 || steps[0].After != 10 ||
		steps[1].Stage != contractsv1.ContextFabricPlanNarrowingAssembledResult || steps[1].Before != 10 || steps[1].After != 5 {
		t.Fatalf("plan member steps = %+v, want cardinality 20->10 then assembled_result 10->5", steps)
	}
	if result.Cohort == nil || len(result.Cohort.Members) != 5 {
		t.Fatalf("served cohort = %v, want 5 members", result.Cohort)
	}

	// (a) The reader-facing sentence: FROM 11, TO 5, and both steps.
	want := "This answer lists 5 of the 11 project subjects found, because the list was narrowed to fit the response budget: " +
		"11 to 10 when the plan lowered the member limit from 20 to 10 before retrieval; " +
		"10 to 5 after the assembled answer exceeded the item budget. " +
		"Ask a narrower question or allow a larger response budget to see the rest."
	if got := chaos6561NarrowingLimitations(result); len(got) != 1 || got[0] != want {
		t.Fatalf("cohort-narrowing limitations = %q, want exactly [%q]", got, want)
	}

	// (b) The structured half: the member read row keeps 5/11, now names the
	// recorded narrowing as its cause, OBSERVED, with both steps.
	row := chaos6561AssembledRow(t, result, chaos6561MemberRequirement)
	if row.Outcome != contractsv1.ContextFabricRequirementNarrowed || row.Served != 5 || row.Declared != 11 {
		t.Fatalf("%s row = %s %d/%d, want narrowed 5/11 (numbers must not change)", chaos6561MemberRequirement, row.Outcome, row.Served, row.Declared)
	}
	if !row.CauseObserved {
		t.Fatalf("%s row cause_observed = false; the narrowing was recorded by the plan, so it was observed: %+v", chaos6561MemberRequirement, row)
	}
	if row.CauseCoverage != "" ||
		row.CauseNarrowing != contractsv1.ContextFabricNarrowingBasisCanonicalIDLexical ||
		row.CauseOverrun != contractsv1.ContextFabricBudgetOverrunItems {
		t.Fatalf("%s row causes = coverage %q narrowing %q overrun %q, want narrowing canonical_id_lexical, overrun items, no coverage cause",
			chaos6561MemberRequirement, row.CauseCoverage, row.CauseNarrowing, row.CauseOverrun)
	}
	wantRefinements := []contractsv1.ContextFabricRequirementRefinement{
		{Stage: contractsv1.ContextFabricOutcomeStagePlanning, Basis: contractsv1.ContextFabricNarrowingBasisCanonicalIDLexical, Before: 11, After: 10},
		{Stage: contractsv1.ContextFabricOutcomeStageAssembledResult, Basis: contractsv1.ContextFabricNarrowingBasisCanonicalIDLexical, Overrun: contractsv1.ContextFabricBudgetOverrunItems, Before: 10, After: 5},
	}
	if !reflect.DeepEqual(row.Refinements, wantRefinements) {
		t.Fatalf("%s row refinements = %+v, want %+v", chaos6561MemberRequirement, row.Refinements, wantRefinements)
	}
	if err := contractsv1.ValidateContextFabricPlanRequirementOutcomeRow(row); err != nil {
		t.Fatalf("%s row does not validate: %v", chaos6561MemberRequirement, err)
	}
}

// TestCHAOS6561NoNarrowingAddsNoDisclosure: a cohort the plan never cut says
// nothing about narrowing -- no false disclosure, and no narrowing cause.
func TestCHAOS6561NoNarrowingAddsNoDisclosure(t *testing.T) {
	t.Parallel()
	result, _ := chaos6561Investigate(t, 3, 1, 1000, nil)
	if steps := chaos6561MemberSteps(result.AnswerPlan); len(steps) != 0 {
		t.Fatalf("plan recorded member steps %+v; this fixture must not narrow", steps)
	}
	if got := chaos6561NarrowingLimitations(result); len(got) != 0 {
		t.Fatalf("unnarrowed answer carries cohort-narrowing limitations %q", got)
	}
	row := chaos6561AssembledRow(t, result, chaos6561MemberRequirement)
	if row.Outcome != contractsv1.ContextFabricRequirementSatisfied || row.Served != 3 || row.Declared != 3 ||
		row.CauseNarrowing != "" || row.CauseOverrun != "" || len(row.Refinements) != 0 {
		t.Fatalf("%s row = %+v, want satisfied 3/3 with no cause and no refinement", chaos6561MemberRequirement, row)
	}
}

// TestCHAOS6561SingleClampStepWording: only the pre-read clamp cut the list
// (11 found, limit 20 -> 10, and the 10 fit), so the sentence names that one
// step and no re-synthesis.
func TestCHAOS6561SingleClampStepWording(t *testing.T) {
	t.Parallel()
	maxItems := chaos6561HeadroomItems(t, 10)
	result, calls := chaos6561Investigate(t, 11, 0, maxItems, nil)
	if calls != 1 {
		t.Fatalf("synthesizer called %d times, want 1 -- this fixture must fit without a retry", calls)
	}
	steps := chaos6561MemberSteps(result.AnswerPlan)
	if len(steps) != 1 || steps[0].Stage != contractsv1.ContextFabricPlanNarrowingCardinality || steps[0].Before != 20 || steps[0].After != 10 {
		t.Fatalf("plan member steps = %+v, want only cardinality 20->10", steps)
	}
	want := "This answer lists 10 of the 11 project subjects found, because the list was narrowed to fit the response budget: " +
		"11 to 10 when the plan lowered the member limit from 20 to 10 before retrieval. " +
		"Ask a narrower question or allow a larger response budget to see the rest."
	if got := chaos6561NarrowingLimitations(result); len(got) != 1 || got[0] != want {
		t.Fatalf("cohort-narrowing limitations = %q, want exactly [%q]", got, want)
	}
	row := chaos6561AssembledRow(t, result, chaos6561MemberRequirement)
	wantRefinements := []contractsv1.ContextFabricRequirementRefinement{
		{Stage: contractsv1.ContextFabricOutcomeStagePlanning, Basis: contractsv1.ContextFabricNarrowingBasisCanonicalIDLexical, Before: 11, After: 10},
	}
	if row.Served != 10 || row.Declared != 11 || !row.CauseObserved || !reflect.DeepEqual(row.Refinements, wantRefinements) {
		t.Fatalf("%s row = %+v, want 10/11, observed, refinements %+v", chaos6561MemberRequirement, row, wantRefinements)
	}
}
