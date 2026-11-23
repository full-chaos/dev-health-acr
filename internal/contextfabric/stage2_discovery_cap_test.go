package contextfabric

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// withDiscoveryCap applies the request's member cap to a discovered graph
// context the way graphrank.DiscoveredCohort does for the production reader:
// members past the cap are not carried, the population still counts them, and
// the cohort says it is truncated.
func withDiscoveryCap(discovered GraphContext, limit int) GraphContext {
	if discovered.Cohort == nil || limit <= 0 || len(discovered.Cohort.Members) <= limit {
		return discovered
	}
	cohort := *discovered.Cohort
	discovered.CohortPopulation = max(discovered.CohortPopulation, len(cohort.Members))
	cohort.Members = slices.Clone(cohort.Members[:limit])
	cohort.Complete, cohort.Truncated = false, true
	kept := map[string]bool{}
	for _, member := range cohort.Members {
		kept[member.Subject.CanonicalID] = true
	}
	groups := make([]contractsv1.ContextFabricCohortGroup, 0, len(cohort.Groups))
	for _, group := range cohort.Groups {
		ids := []string{}
		for _, id := range group.MemberCanonicalIDs {
			if kept[id] {
				ids = append(ids, id)
			}
		}
		if len(ids) > 0 {
			group.MemberCanonicalIDs, group.Total, group.Complete = ids, len(ids), false
			groups = append(groups, group)
		}
	}
	if len(cohort.Groups) > 0 {
		cohort.Groups = groups
	}
	discovered.Cohort = &cohort
	return discovered
}

// discoveryCappedGraphStub is graphReaderStub with the production discovery cap.
type discoveryCappedGraphStub struct {
	graphReaderStub
}

func (g discoveryCappedGraphStub) DiscoverContext(ctx context.Context, principal storage.Principal, request GraphDiscoveryRequest) (GraphContext, error) {
	discovered, err := g.graphReaderStub.DiscoverContext(ctx, principal, request)
	return withDiscoveryCap(discovered, request.Request.Options.MaxCohortMembers), err
}

// capHonouringGraphReader is capturingGraphReader with the production
// discovery cap, recording the cap it was given.
type capHonouringGraphReader struct {
	capturingGraphReader
	caps []int
}

func (g *capHonouringGraphReader) DiscoverContext(ctx context.Context, principal storage.Principal, request GraphDiscoveryRequest) (GraphContext, error) {
	discovered, err := g.capturingGraphReader.DiscoverContext(ctx, principal, request)
	g.caps = append(g.caps, request.Request.Options.MaxCohortMembers)
	return withDiscoveryCap(discovered, request.Request.Options.MaxCohortMembers), err
}

// On the normal (non work-item) path, stage 1 clamps the discovery cap to the
// plan's member budget and discovery never returns more than that cap, so the
// synthesis-input stage's `len(members) > MaxMembers` guard cannot hold and
// neither its grouped nor its flat arm narrows anything.
func TestNormalPathDiscoveryCapLeavesNothingForTheSynthesisInputStage(t *testing.T) {
	t.Parallel()
	for _, grouped := range []bool{false, true} {
		for _, maxItems := range []int{1, 2, 5} {
			for _, requestCap := range []int{0, 1, 3, 50} {
				name := fmt.Sprintf("grouped=%t/max_items=%d/request_cap=%d(0=request default)", grouped, maxItems, requestCap)
				t.Run(name, func(t *testing.T) {
					t.Parallel()
					cohort := budgetStageCohort(8)
					if grouped {
						ids := make([]string, 0, len(cohort.Members))
						for _, member := range cohort.Members {
							ids = append(ids, member.Subject.CanonicalID)
						}
						cohort.Groups = []contractsv1.ContextFabricCohortGroup{{
							Subject:            SubjectRef{Kind: SubjectTeam, CanonicalID: "team_a", Label: "team_a"},
							MemberCanonicalIDs: ids, Complete: true, Total: len(ids),
						}}
					}
					calls := 0
					telemetry := &recordingTelemetry{}
					engine := budgetStageEngine(t, cohort, 0, budgetStageOptions(maxItems, time.Second), &calls, telemetry)
					graph := &capHonouringGraphReader{capturingGraphReader: capturingGraphReader{
						resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
						context: GraphContext{
							Cohort: cohort,
							Paths:  []RelationshipPath{}, DriverCandidates: []DriverJudgment{},
							FactRequirements: []FactRequirement{}, EvidenceRefIDs: []string{},
							Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
						},
					}}
					engine.graph = graph
					request := validInvestigationRequestWithConfirmedWindow()
					if requestCap > 0 {
						request.Options.MaxCohortMembers = requestCap
					}

					result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
					if err != nil {
						t.Fatalf("Investigate() error = %v", err)
					}
					if result.AnswerPlan == nil {
						t.Fatal("served result carries no plan")
					}
					maxMembers := result.AnswerPlan.Budget.MaxMembers
					if maxMembers <= 0 {
						t.Fatalf("plan MaxMembers = %d; this fixture needs a member budget to say anything", maxMembers)
					}
					if len(graph.caps) != 1 {
						t.Fatalf("discovery ran %d times, want 1", len(graph.caps))
					}
					if discoveryCap := graph.caps[0]; discoveryCap <= 0 || discoveryCap > maxMembers {
						t.Fatalf("discovery cap = %d, want 1..%d (the plan's member budget)", discoveryCap, maxMembers)
					}
					for _, event := range telemetry.planNarrowings {
						if event.Stage == contractsv1.ContextFabricPlanNarrowingSynthesisInput {
							t.Fatalf("synthesis-input stage narrowed %d -> %d on the normal path", event.Before, event.After)
						}
					}
					for _, step := range result.AnswerPlan.Narrowing {
						if step.Stage == contractsv1.ContextFabricPlanNarrowingSynthesisInput {
							t.Fatalf("served plan records a synthesis-input step %d -> %d on the normal path", step.Before, step.After)
						}
					}
				})
			}
		}
	}
}

// A work-item walk is the one path stage 1 does not clamp. A family that admits
// that walk must not be a grouped family: only grouped families carry a group
// axis on the plan, so a walk never reaches a grouped narrowing.
func TestWorkItemWalkFamiliesCarryNoGroupAxis(t *testing.T) {
	t.Parallel()
	admitting := 0
	for _, definition := range QuestionFamilyDefinitions() {
		if !definition.allowsWorkItemTuple {
			continue
		}
		admitting++
		if definition.SubjectAxis == SubjectAxisManyGrouped {
			t.Errorf("family %q admits a work-item walk and is grouped", definition.Family)
		}
	}
	if admitting == 0 {
		t.Fatal("no family admits a work-item walk; this cell checks nothing")
	}
}
