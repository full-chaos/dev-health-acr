package contextfabric

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const (
	memberCutPopulation = 60
	memberCutCallerCap  = 50
	memberCutGaps       = 21
)

// memberCutEngine is the flat owned-projects listing with a synthesizer that
// writes `gaps` readiness findings and cites `cited` members by a claim. The
// engine is composed with the given retry reserve: zero is the deployment the
// retry is declined on.
func memberCutEngine(t *testing.T, gaps int, cited []int, reserve time.Duration, telemetry *recordingTelemetry) *Engine {
	t.Helper()
	return memberCutEngineAtCeiling(t, gaps, cited, reserve, 0, telemetry)
}

func memberCutEngineAtCeiling(t *testing.T, gaps int, cited []int, reserve time.Duration, maxBytes int64, telemetry *recordingTelemetry) *Engine {
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
			interpreted: InterpretedQuestion{Shape: ShapeDiscoveredCohort, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}, FactRequirements: []FactRequirement{{Kind: FactStatus}}},
			outcome:     QuestionFamilyOutcome{Frame: &validated.Frame, Family: QuestionFamilyScopedCohortStatus, Source: QuestionFamilySourceModel},
		},
		Graph: ownedProjectsGraph{&capturingGraphReader{
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
			context: GraphContext{Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{}, FactRequirements: []FactRequirement{}, EvidenceRefIDs: []string{},
				Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}},
		}, memberCutPopulation},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{Facts: []CanonicalFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}, Version: "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{}}, nil
		}),
		Synthesizer: synthesizerFunc(func(_ context.Context, _ storage.Principal, input SynthesisInput) (InvestigationResult, error) {
			claims := []ClaimedFact{}
			refs := []string{}
			if gaps > 0 {
				refs = []string{"evidence_gap_01"}
			}
			findings := []Finding{}
			for i := 0; i < gaps; i++ {
				finding := Finding{FindingID: fmt.Sprintf("finding_gap_%02d", i), Kind: "relationship", Summary: "A readiness gap appears open.", EvidenceRefIDs: []string{"evidence_gap_01"}}
				if i < len(cited) && input.Graph.Cohort != nil && cited[i] < len(input.Graph.Cohort.Members) {
					finding.Subjects = []SubjectRef{input.Graph.Cohort.Members[cited[i]].Subject}
				}
				findings = append(findings, finding)
			}
			return InvestigationResult{
				Status: InvestigationComplete, DirectJudgment: "Fine.", CurrentState: "Nominal.",
				StrongestPressures: []string{}, Drivers: []DriverJudgment{}, RemainingWork: []Finding{},
				ReadinessGaps: findings, Paths: []RelationshipPath{}, Conflicts: []Finding{},
				Limitations: []string{}, EvidenceRefIDs: refs, ClaimedFacts: claims,
				Coverage: input.Facts.Coverage, DeterministicAnswer: "Fine, based on available context.", Warnings: []string{},
				Versions: VersionSet{Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1", InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1"},
			}, nil
		}),
		Requirements: registryDeriver{},
		Telemetry:    telemetry,
	}, func() EngineOptions {
		options := budgetStageOptions(serverItemCeiling, reserve)
		options.MaxSerializedBytes = maxBytes
		return options
	}())
	if err != nil {
		t.Fatal(err)
	}
	return engine
}

func investigateMemberCut(t *testing.T, engine *Engine) (InvestigationResult, error) {
	t.Helper()
	request := validInvestigationRequestWithConfirmedWindow()
	request.Options.MaxCohortMembers = memberCutCallerCap
	return engine.Investigate(context.Background(), storage.Principal{OrgID: "org_listing"}, request)
}

func TestAFlatCohortOverTheItemBudgetIsServedWithItsTrailingMembersCut(t *testing.T) {
	telemetry := &recordingTelemetry{}
	result, err := investigateMemberCut(t, memberCutEngine(t, memberCutGaps, nil, 0, telemetry))
	if err != nil {
		var refusal AnswerBudgetRefusal
		if errors.As(err, &refusal) {
			t.Fatalf("refused (%s) at %d items against %d with no retry available: members nothing cites were still cuttable", refusal.Overrun, refusal.MeasuredItems, refusal.MaxItems)
		}
		t.Fatalf("Investigate() error = %v", err)
	}
	items := contractsv1.CountContextFabricResultItems(result).Budgeted()
	if items > result.AnswerPlan.Budget.MaxItems {
		t.Fatalf("served %d items against %d", items, result.AnswerPlan.Budget.MaxItems)
	}
	if got, want := len(result.Cohort.Members), result.AnswerPlan.Budget.MaxItems-memberCutGaps; got != want {
		t.Fatalf("served %d members, want %d: the fewest cut that fit", got, want)
	}
	if result.Cohort.Complete || !result.Cohort.Truncated {
		t.Fatalf("complete=%v truncated=%v, want false/true", result.Cohort.Complete, result.Cohort.Truncated)
	}
	disclosed := false
	for _, limitation := range result.Limitations {
		if contractsv1.IsContextFabricCohortNarrowingLimitation(limitation) && strings.Contains(limitation, "exceeded the item budget") {
			disclosed = true
		}
	}
	if !disclosed {
		t.Fatalf("no cohort-narrowing sentence naming the item budget: %q", result.Limitations)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("the served answer is not valid: %v", err)
	}
	cuts := 0
	for _, event := range telemetry.planNarrowings {
		if event.Overrun == contractsv1.ContextFabricBudgetOverrunItems && event.After < event.Before && event.RetryDeclined != RetryDeclinedNotApplicable {
			cuts++
		}
	}
	if cuts != 1 {
		t.Fatalf("member-cut decision events = %d, want 1", cuts)
	}
}

// A member the answer cites is never cut, whatever its place in the list.
func TestAMemberCutNeverDropsAMemberTheAnswerCites(t *testing.T) {
	last := memberCutCallerCap - 1
	result, err := investigateMemberCut(t, memberCutEngine(t, memberCutGaps, []int{last, last - 1}, 0, &recordingTelemetry{}))
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	kept := map[string]bool{}
	for _, member := range result.Cohort.Members {
		kept[member.Subject.CanonicalID] = true
	}
	named := 0
	for _, finding := range result.ReadinessGaps {
		for _, subject := range finding.Subjects {
			named++
			if !kept[subject.CanonicalID] {
				t.Fatalf("finding %s names member %s, which the cut dropped", finding.FindingID, subject.CanonicalID)
			}
		}
	}
	if named != 2 {
		t.Fatalf("findings name %d members, want the 2 the fixture cites", named)
	}
	if items := contractsv1.CountContextFabricResultItems(result).Budgeted(); items > result.AnswerPlan.Budget.MaxItems {
		t.Fatalf("served %d items against %d", items, result.AnswerPlan.Budget.MaxItems)
	}
}

// When the non-member items alone exceed the budget no member cut can fit it,
// and the refusal stands.
func TestAMemberCutDoesNotServeWhenTheOtherItemsAloneOverrun(t *testing.T) {
	_, err := investigateMemberCut(t, memberCutEngine(t, 70, nil, 0, &recordingTelemetry{}))
	var refusal AnswerBudgetRefusal
	if !errors.As(err, &refusal) || refusal.Overrun != contractsv1.ContextFabricBudgetOverrunItems {
		t.Fatalf("err = %v, want an items refusal", err)
	}
}

// The caller's byte option is below the answer's size and the item budget is
// over as well. Items are reported first, so no byte lever ran: the member cut
// fits the items and the byte ladder continues from the cut document, which is
// served at the service ceiling instead of being refused on items.
func TestAnItemOverrunUnderACallerByteOptionIsServedAtTheServiceCeiling(t *testing.T) {
	const ceiling = 65536
	const option = 8192
	engine := memberCutEngineAtCeiling(t, memberCutGaps, nil, 0, ceiling, &recordingTelemetry{})
	request := validInvestigationRequestWithConfirmedWindow()
	request.Options.MaxCohortMembers = memberCutCallerCap
	request.Options.AnswerBudgetBytes = option
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_listing"}, request)
	if err != nil {
		var refusal AnswerBudgetRefusal
		if errors.As(err, &refusal) {
			t.Fatalf("refused (%s) at %d items / %d bytes against %d items / %d bytes", refusal.Overrun, refusal.MeasuredItems, refusal.MeasuredBytes, refusal.MaxItems, refusal.MaxSerializedBytes)
		}
		t.Fatalf("Investigate() error = %v", err)
	}
	if bytes := servedBytes(t, result); bytes <= option || bytes > ceiling {
		t.Fatalf("served %d bytes, want above the %d option and within the %d ceiling", bytes, option, ceiling)
	}
	if items := contractsv1.CountContextFabricResultItems(result).Budgeted(); items > result.AnswerPlan.Budget.MaxItems {
		t.Fatalf("served %d items against %d", items, result.AnswerPlan.Budget.MaxItems)
	}
	if len(result.Cohort.Members) >= memberCutCallerCap {
		t.Fatalf("served %d members: the cut did not run", len(result.Cohort.Members))
	}
}
