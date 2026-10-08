package contextfabric

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/answerprojection"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const (
	listingPopulation = 19
	capPopulation     = 100
	listingArchived   = 5
	archivedReason    = "Project state: completed; archived."
)

// ownedProjectsGraph answers like discovery over a team that owns
// listingPopulation projects: it honours the request's MaxCohortMembers and
// reports the pool it counted.
type ownedProjectsGraph struct {
	*capturingGraphReader
	population int
}

func ownedProjectsCohort(population int) *Cohort {
	cohort := budgetStageCohort(0)
	for i := 0; i < population; i++ {
		id := fmt.Sprintf("project_%03d", i)
		cohort.Members = append(cohort.Members, CohortMember{
			Subject: SubjectRef{Kind: SubjectProject, CanonicalID: id, Label: id}, Rank: i + 1,
			InclusionReasons: []string{"Graph retrieval associated this subject with the requested condition."},
		})
	}
	for i := range cohort.Members {
		cohort.Members[i].InclusionReasons = []string{"Project the named team owns, reached from the team in the authorized Context Fabric graph."}
		cohort.Members[i].EvidenceRefIDs = []string{fmt.Sprintf("ref_member_%02d", i)}
		if i < listingArchived {
			cohort.Members[i].InclusionReasons = append(cohort.Members[i].InclusionReasons, archivedReason)
		}
	}
	return cohort
}

func (g ownedProjectsGraph) DiscoverContext(ctx context.Context, principal storage.Principal, request GraphDiscoveryRequest) (GraphContext, error) {
	graph, err := g.capturingGraphReader.DiscoverContext(ctx, principal, request)
	if err != nil {
		return graph, err
	}
	cohort := ownedProjectsCohort(g.population)
	if limit := request.Request.Options.MaxCohortMembers; limit > 0 && limit < len(cohort.Members) {
		cohort.Members = cohort.Members[:limit]
		cohort.Complete = false
		cohort.Truncated = true
		cohort.Population = g.population
	}
	graph.Cohort = cohort
	graph.CohortPopulation = g.population
	return graph, nil
}

func investigateOwnedProjects(t *testing.T, family QuestionFamily, maxItems, callerMembers int) InvestigationResult {
	t.Helper()
	return investigateOwnedPopulation(t, family, maxItems, callerMembers, listingPopulation)
}

func investigateOwnedPopulation(t *testing.T, family QuestionFamily, maxItems, callerMembers, population int) InvestigationResult {
	t.Helper()
	frame := QuestionFrame{
		Goals: []InvestigationGoal{GoalCountOrAggregate},
		SubjectExpression: SubjectExpression{
			Kind:   SubjectExpressionChildrenOfScope,
			Scoped: &ScopedSetExpression{AnchorTerms: []string{"Platform"}, MemberKind: SubjectProject},
		},
		Temporal: TemporalIntentCurrent,
		Version:  QuestionFrameVersion,
	}
	validated := ValidateFrame(frame, nil, ShapeDiscoveredCohort)
	engine, err := NewEngine(EngineDependencies{
		Interpreter: familyInterpreter{
			interpreted: InterpretedQuestion{
				Shape: ShapeDiscoveredCohort, RequestedJudgment: "status",
				TimeContext:      TimeContext{Axis: TemporalCurrent},
				FactRequirements: []FactRequirement{{Kind: FactStatus}},
			},
			outcome: QuestionFamilyOutcome{Frame: &validated.Frame, Family: family, Source: QuestionFamilySourceModel},
		},
		Graph: ownedProjectsGraph{&capturingGraphReader{
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
			context: GraphContext{
				Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{},
				FactRequirements: []FactRequirement{}, EvidenceRefIDs: []string{},
				Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
			},
		}, population},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{
				Facts: []CanonicalFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				Version: "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{},
			}, nil
		}),
		Synthesizer: synthesizerFunc(func(_ context.Context, _ storage.Principal, input SynthesisInput) (InvestigationResult, error) {
			return InvestigationResult{
				Status: InvestigationComplete, DirectJudgment: "Fine.", CurrentState: "Nominal.",
				StrongestPressures: []string{}, Drivers: []DriverJudgment{}, RemainingWork: []Finding{},
				ReadinessGaps: []Finding{}, Paths: []RelationshipPath{}, Conflicts: []Finding{},
				Limitations: []string{}, EvidenceRefIDs: []string{}, ClaimedFacts: []ClaimedFact{},
				Coverage:            input.Facts.Coverage,
				DeterministicAnswer: "Fine, based on available context.", Warnings: []string{},
				Versions: VersionSet{
					Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
					InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1",
				},
			}, nil
		}),
		Requirements: registryDeriver{},
		Telemetry:    &recordingTelemetry{},
	}, budgetStageOptions(maxItems, time.Second))
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	request := validInvestigationRequestWithConfirmedWindow()
	request.Options.MaxCohortMembers = callerMembers
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_listing"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	return result
}

func archivedServed(cohort *Cohort) int {
	n := 0
	for _, m := range cohort.Members {
		if strings.Contains(strings.Join(m.InclusionReasons, "|"), archivedReason) {
			n++
		}
	}
	return n
}

func TestFlatCohortListingServesEveryOwnedMemberUnderTheDefaultBudget(t *testing.T) {
	for _, caller := range []int{20, 25} {
		t.Run(fmt.Sprintf("caller_%d", caller), func(t *testing.T) {
			result := investigateOwnedProjects(t, QuestionFamilyScopedCohortStatus, serverItemCeiling, caller)
			cohort := result.Cohort
			if cohort == nil {
				t.Fatal("no cohort served")
			}
			if len(cohort.Members) != listingPopulation {
				t.Fatalf("served %d members, want %d (plan budget %+v)", len(cohort.Members), listingPopulation, result.AnswerPlan.Budget)
			}
			if !cohort.Complete || cohort.Truncated {
				t.Fatalf("complete=%v truncated=%v, want true/false", cohort.Complete, cohort.Truncated)
			}
			if got := archivedServed(cohort); got != listingArchived {
				t.Fatalf("archived-flagged rows = %d, want %d", got, listingArchived)
			}
		})
	}
}

func TestFlatCohortListingCallerBudgetCutsAndSaysSo(t *testing.T) {
	result := investigateOwnedProjects(t, QuestionFamilyScopedCohortStatus, serverItemCeiling, 5)
	if got := len(result.Cohort.Members); got != 5 {
		t.Fatalf("served %d members, want the caller's 5", got)
	}
	if result.Cohort.Complete || !result.Cohort.Truncated {
		t.Fatalf("a caller cut must read complete=false truncated=true, got %v/%v", result.Cohort.Complete, result.Cohort.Truncated)
	}
	projection := answerprojection.Project(result, answerprojection.Budget{MaxCohortMembers: 5})
	if projection.Cohort.Population != listingPopulation || len(projection.Cohort.Members) != 5 {
		t.Fatalf("the count of %d is not disclosed: population=%d members=%d", listingPopulation, projection.Cohort.Population, len(projection.Cohort.Members))
	}
}

func TestPlanBudgetFlatCohortDerivesItemsFromTheCallerMembers(t *testing.T) {
	flat := func(items, caller int) AnswerPlanBudget {
		return planBudget(PlanBudgetFlatCohort, ResponseBudget{MaxItems: items, MaxSerializedBytes: 131072}, caller, true)
	}
	if got := flat(serverItemCeiling, 25); got.MaxMembers != 25 || got.MaxItems < 25+got.SynthesisHeadroom {
		t.Fatalf("caller 25 under 30 items: members=%d items=%d headroom=%d, want 25 and items >= members+headroom", got.MaxMembers, got.MaxItems, got.SynthesisHeadroom)
	}
	if got := flat(serverItemCeiling, 5); got.MaxMembers != 5 || got.MaxItems != serverItemCeiling {
		t.Fatalf("caller 5: members=%d items=%d, want 5 and the unlowered ceiling %d", got.MaxMembers, got.MaxItems, serverItemCeiling)
	}
	if got := flat(16, 25); got.MaxMembers != 1 {
		t.Fatalf("a ceiling at its own headroom: members=%d, want the floor 1", got.MaxMembers)
	}
	if got := flat(serverItemCeiling, 0); got.MaxMembers != 14 {
		t.Fatalf("no caller cap: members=%d, want the 14 default", got.MaxMembers)
	}
	if got := flat(serverItemCeiling, 5000); got.MaxMembers != flatCohortListingMemberCap {
		t.Fatalf("caller 5000: members=%d, want the hard cap %d", got.MaxMembers, flatCohortListingMemberCap)
	}
	if got := planBudget(PlanBudgetGroupedCohort, ResponseBudget{MaxItems: serverItemCeiling}, 25, true); got.MaxMembers != 10 {
		t.Fatalf("grouped profile moved: members=%d, want 10", got.MaxMembers)
	}
}

func TestFlatCohortListingAtTheHardCapIsServedWhole(t *testing.T) {
	result := investigateOwnedPopulation(t, QuestionFamilyScopedCohortStatus, serverItemCeiling, flatCohortListingMemberCap, capPopulation)
	if got := len(result.Cohort.Members); got != capPopulation {
		t.Fatalf("served %d members, want all %d", got, capPopulation)
	}
	if !result.Cohort.Complete || result.Cohort.Truncated {
		t.Fatalf("complete=%v truncated=%v, want true/false", result.Cohort.Complete, result.Cohort.Truncated)
	}
	if result.AnswerPlan.Budget.MaxItems < capPopulation+result.AnswerPlan.Budget.SynthesisHeadroom {
		t.Fatalf("plan ceiling %d does not hold %d members and the headroom", result.AnswerPlan.Budget.MaxItems, capPopulation)
	}
}
