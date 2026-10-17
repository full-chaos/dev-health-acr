package contextfabric

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// memberCutExtraGoals adds goals to the fixture's frame; a row that needs a
// derived read requirement sets it for its own duration.
var memberCutExtraGoals []InvestigationGoal

// memberCutGraphDetails are coverage details the fixture's graph reader mints
// and its synthesizer carries into the draft, as the production synthesizer
// merges the graph's coverage into the result.
var memberCutGraphDetails []CoverageDetail

// memberCutWrapSynthesizer, when set, wraps the fixture's synthesizer for a
// row that needs the model call to fail.
// memberCutGraphPaths is the relationship paths the fixture's graph reader
// mints, and its synthesizer carries into the answer.
var memberCutGraphPaths []RelationshipPath

var memberCutWrapSynthesizer func(AnswerSynthesizer) AnswerSynthesizer

// memberCutDegradedReasons pairs each degrading detail with its reason, as the
// graph reader mints them.
func memberCutDegradedReasons() []string {
	reasons := []string{}
	for _, detail := range memberCutGraphDetails {
		if detail.Degrading {
			reasons = append(reasons, detail.Raw)
		}
	}
	return reasons
}

const (
	memberCutPopulation = 60
	memberCutCallerCap  = 50
	memberCutGaps       = 21
	// memberCutKept is the fixture's measured keep: 71 items against 66 over 50
	// members needs 4 cut, and the margin adds 5.
	memberCutKept = 41
)

// memberCutEngine is the flat owned-projects listing with a synthesizer that
// writes `gaps` readiness findings and cites `cited` members by a claim. The
// engine is composed with the given retry reserve: zero is the deployment the
// retry is declined on.
func memberCutEngine(t *testing.T, gaps int, cited []int, reserve time.Duration, telemetry *recordingTelemetry) *Engine {
	t.Helper()
	return memberCutEngineAtCeiling(t, gaps, cited, reserve, 0, memberCutPopulation, telemetry)
}

func memberCutEngineAtCeiling(t *testing.T, gaps int, cited []int, reserve time.Duration, maxBytes int64, population int, telemetry *recordingTelemetry) *Engine {
	return memberCutEngineNaming(t, gaps, cited, reserve, maxBytes, population, telemetry, nil, registryDeriver{})
}

// memberCutEngineNaming is the fixture with a hook that writes the draft's
// current_state from the cohort it was given, so a row can have the prose name
// a member that no claim, driver or finding does.
func memberCutEngineNaming(t *testing.T, gaps int, cited []int, reserve time.Duration, maxBytes int64, population int, telemetry *recordingTelemetry, currentState func(*Cohort) string, deriver RequirementDeriver) *Engine {
	t.Helper()
	frame := QuestionFrame{
		Goals: append([]InvestigationGoal{GoalCountOrAggregate}, memberCutExtraGoals...),
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
			context: GraphContext{Paths: append([]RelationshipPath{}, memberCutGraphPaths...), DriverCandidates: []DriverJudgment{}, FactRequirements: []FactRequirement{}, EvidenceRefIDs: []string{},
				Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: memberCutDegradedReasons(), Details: slices.Clone(memberCutGraphDetails)}},
		}, population},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{Facts: []CanonicalFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}, Version: "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{}}, nil
		}),
		Synthesizer: memberCutWrapped(synthesizerFunc(func(_ context.Context, _ storage.Principal, input SynthesisInput) (InvestigationResult, error) {
			claims := []ClaimedFact{}
			refs := []string{}
			if gaps > 0 {
				refs = []string{"evidence_gap_01"}
			}
			// The result-level list is the union of what the answer was
			// given, members' refs included, as a synthesis writes it.
			if input.Graph.Cohort != nil {
				for _, member := range input.Graph.Cohort.Members {
					refs = append(refs, member.EvidenceRefIDs...)
				}
			}
			findings := []Finding{}
			for i := 0; i < gaps; i++ {
				finding := Finding{FindingID: fmt.Sprintf("finding_gap_%02d", i), Kind: "relationship", Summary: "A readiness gap appears open.", EvidenceRefIDs: []string{"evidence_gap_01"}}
				if i < len(cited) && input.Graph.Cohort != nil && cited[i] < len(input.Graph.Cohort.Members) {
					finding.Subjects = []SubjectRef{input.Graph.Cohort.Members[cited[i]].Subject}
				}
				if i == 0 && input.Graph.Cohort != nil && len(input.Graph.Cohort.Members) > 0 {
					// A kept finding cites the last member's ref although it
					// names no subject: the ref stays when that member goes.
					last := input.Graph.Cohort.Members[len(input.Graph.Cohort.Members)-1]
					finding.EvidenceRefIDs = append(finding.EvidenceRefIDs, last.EvidenceRefIDs...)
				}
				findings = append(findings, finding)
			}
			return InvestigationResult{
				Status: InvestigationComplete, DirectJudgment: "Fine.", CurrentState: currentStateFor(currentState, input.Graph.Cohort),
				StrongestPressures: []string{}, Drivers: []DriverJudgment{}, RemainingWork: []Finding{},
				ReadinessGaps: findings, Paths: append([]RelationshipPath{}, input.Graph.Paths...), Conflicts: []Finding{},
				Limitations: []string{}, EvidenceRefIDs: refs, ClaimedFacts: claims,
				Coverage: func() Coverage {
					merged := input.Facts.Coverage
					merged.Details = slices.Clone(input.Graph.Coverage.Details)
					merged.DegradedReasons = slices.Clone(input.Graph.Coverage.DegradedReasons)
					return merged
				}(), DeterministicAnswer: "Fine, based on available context.", Warnings: []string{},
				Versions: VersionSet{Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1", InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1"},
			}, nil
		})),
		Requirements: deriver,
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

// The Members array stays in pool order and says nothing of attention, so a
// ranked list is cut by AttentionRank, the highest numbers first, never from
// the end of the array. The fixture is built so the end of the array and the
// worst attention differ; the test asserts they do, so it cannot pass by
// coincidence of the two orders.
func TestARankedListIsCutByAttentionRankNotByPoolPosition(t *testing.T) {
	attention := []int{3, 1, 6, 2, 5, 4}
	members := make([]CohortMember, 0, len(attention))
	for index, rank := range attention {
		id := fmt.Sprintf("project_%03d", index)
		members = append(members, CohortMember{
			Subject: SubjectRef{Kind: SubjectProject, CanonicalID: id, Label: id}, Rank: index + 1,
			InclusionReasons: []string{"owned"}, RankingComputed: true, AttentionRank: rank,
		})
	}
	// project_005 (attention 4) is named by a finding and is cut last.
	result := InvestigationResult{
		Cohort:        &Cohort{Kind: SubjectProject, Members: members, Rationale: "owned", Complete: true, ScoreMeaning: contractsv1.ContextFabricCohortScoreMeaningAttention},
		ReadinessGaps: []Finding{{FindingID: "finding_gap_00", Kind: "relationship", Summary: "A gap.", Subjects: []SubjectRef{members[5].Subject}, EvidenceRefIDs: []string{"evidence_gap_01"}}},
	}
	basis, ok := memberCutBasis(result.Cohort)
	order, _ := memberCutOrder(result.Cohort, basis, result)
	if !ok || basis != contractsv1.ContextFabricNarrowingBasisAttentionRank {
		t.Fatalf("basis %q ok=%v, want attention_rank", basis, ok)
	}
	const cut = 2
	poolTail := []string{"project_004", "project_003"}
	cutIDs := []string{}
	for _, index := range order[:cut] {
		cutIDs = append(cutIDs, members[index].Subject.CanonicalID)
	}
	if cutIDs[0] == poolTail[0] || cutIDs[1] == poolTail[1] {
		t.Fatalf("fixture does not discriminate: cut %v equals the pool tail %v", cutIDs, poolTail)
	}
	if cutIDs[0] != "project_002" || cutIDs[1] != "project_004" {
		t.Fatalf("cut %v, want the two worst attention ranks, project_002 (6) then project_004 (5)", cutIDs)
	}
}

func currentStateFor(hook func(*Cohort) string, cohort *Cohort) string {
	if hook == nil {
		return "Nominal."
	}
	return hook(cohort)
}

// A one- or two-character label is matched as a whole word: "AI" in "the AI
// team" keeps its member listed, and "said" does not.
func TestAShortLabelIsMatchedAsAWholeWordInTheDraftProse(t *testing.T) {
	member := SubjectRef{Kind: SubjectProject, CanonicalID: "project_001", Label: "AI"}
	if !namedInProse(member, answerProse(InvestigationResult{CurrentState: "The AI team is stretched."})) {
		t.Fatal("a one-word mention of the two-character label is not matched")
	}
	if namedInProse(member, answerProse(InvestigationResult{CurrentState: "Reviewers said it is fine."})) {
		t.Fatal("the label matched inside another word")
	}
	if !namedInProse(SubjectRef{Kind: SubjectProject, CanonicalID: "x", Label: "A"}, answerProse(InvestigationResult{CurrentState: "Team A is late."})) {
		t.Fatal("a one-character label mentioned as a word is not matched")
	}
}

// A model-written answer that overran on items, with no deadline reserve for a
// second model call, is refused: the cut is not composed without the model it
// would need, and the trace says what it would have kept.
func TestAModelWrittenItemsOverrunWithNoSynthesisTimeIsRefused(t *testing.T) {
	telemetry := &recordingTelemetry{}
	_, err := investigateMemberCut(t, memberCutEngine(t, memberCutGaps, nil, 0, telemetry))
	var refusal AnswerBudgetRefusal
	if !errors.As(err, &refusal) || refusal.Overrun != contractsv1.ContextFabricBudgetOverrunItems || refusal.RetryAttempted {
		t.Fatalf("err = %v, want the items refusal with no second synthesis", err)
	}
	if len(telemetry.memberCuts) != 1 {
		t.Fatalf("member-cut events = %d, want 1: %+v", len(telemetry.memberCuts), telemetry.memberCuts)
	}
	event := telemetry.memberCuts[0]
	if event.Declined != MemberCutNoSynthesisTime || event.Served || event.Recomposed || event.Listed != memberCutCallerCap || event.Uncited != memberCutCallerCap-1 ||
		event.Kept != memberCutKept || event.Need != memberCutCallerCap-memberCutKept || event.KeepBy != MemberCutKeepMeasured || event.Basis != contractsv1.ContextFabricNarrowingBasisAttentionRank || event.ItemsBefore <= event.MaxItems {
		t.Fatalf("member-cut event = %+v, want no_synthesis_time, listed %d, uncited %d (the top-ranked member is never cut), kept %d measured, attention_rank, over the item budget", event, memberCutCallerCap, memberCutCallerCap-1, memberCutKept)
	}
}

// A member the answer's prose names is not counted as cuttable: the assessment
// excludes it, and the control without the prose counts every member.
func TestAMemberNamedOnlyInTheDraftProseIsNotCountedAsCuttable(t *testing.T) {
	named := fmt.Sprintf("project_%03d", memberCutCallerCap-1)
	telemetry := &recordingTelemetry{}
	engine := memberCutEngineNaming(t, memberCutGaps, nil, 0, 0, memberCutPopulation, telemetry, func(cohort *Cohort) string {
		return "The review queue on " + strings.ToUpper(named) + " is the one to watch."
	}, registryDeriver{})
	if _, err := investigateMemberCut(t, engine); err == nil {
		t.Fatal("want the items refusal")
	}
	if len(telemetry.memberCuts) != 1 || telemetry.memberCuts[0].Uncited != memberCutCallerCap-2 {
		t.Fatalf("uncited = %+v, want %d: the member the prose names is not cuttable", telemetry.memberCuts, memberCutCallerCap-2)
	}
}

// A member the answer cites (a finding names it as a subject) is not cuttable.
func TestAMemberAFindingNamesIsNotCountedAsCuttable(t *testing.T) {
	telemetry := &recordingTelemetry{}
	last := memberCutCallerCap - 1
	engine := memberCutEngine(t, memberCutGaps, []int{last, last - 1}, 0, telemetry)
	if _, err := investigateMemberCut(t, engine); err == nil {
		t.Fatal("want the items refusal")
	}
	if len(telemetry.memberCuts) != 1 || telemetry.memberCuts[0].Uncited != memberCutCallerCap-3 {
		t.Fatalf("uncited = %+v, want %d", telemetry.memberCuts, memberCutCallerCap-3)
	}
}

// An unranked list sorted by canonical id has a stated order (the end of the
// list) and the assessment says so.
func TestAnUnrankedSortedListIsOrderedFromTheEnd(t *testing.T) {
	members := make([]CohortMember, 0, 6)
	for index := 0; index < 6; index++ {
		id := fmt.Sprintf("project_%03d", index)
		members = append(members, CohortMember{
			Subject: SubjectRef{Kind: SubjectProject, CanonicalID: id, Label: id}, Rank: index + 1,
			InclusionReasons: []string{"owned"},
		})
	}
	result := InvestigationResult{
		Cohort:        &Cohort{Kind: SubjectProject, Members: members, Rationale: "owned", Complete: true},
		ReadinessGaps: []Finding{{FindingID: "finding_gap_00", Kind: "relationship", Summary: "A gap.", Subjects: []SubjectRef{members[5].Subject}, EvidenceRefIDs: []string{"evidence_gap_01"}}},
	}
	basis, ok := memberCutBasis(result.Cohort)
	order, _ := memberCutOrder(result.Cohort, basis, result)
	if !ok || basis != contractsv1.ContextFabricNarrowingBasisCanonicalIDLexical {
		t.Fatalf("basis %q ok=%v, want canonical_id_lexical", basis, ok)
	}
	if got := fmt.Sprint(order); got != fmt.Sprint([]int{4, 3, 2, 1, 0, 5}) {
		t.Fatalf("order %v, want [4 3 2 1 0 5] (the cited member 5 is cut last)", order)
	}
}

func memberCutWrapped(synthesizer AnswerSynthesizer) AnswerSynthesizer {
	if memberCutWrapSynthesizer != nil {
		return memberCutWrapSynthesizer(synthesizer)
	}
	return synthesizer
}

// The number kept comes from the measured overrun, not from half the list; the
// half target applies only when items per member cannot be measured.
func TestTheMemberCutKeepComesFromTheMeasuredOverrun(t *testing.T) {
	for _, row := range []struct {
		name               string
		listed, protected  int
		items, maxItems    int
		wantKeep, wantNeed int
		wantBy             MemberCutKeepBasis
	}{
		{"measured_with_margin", 50, 0, 71, 66, 41, 9, MemberCutKeepMeasured},
		{"large_overrun", 50, 0, 132, 66, 20, 30, MemberCutKeepMeasured},
		{"protected_members_raise_keep", 50, 45, 71, 66, 45, 9, MemberCutKeepMeasured},
		{"keeps_one", 4, 0, 400, 66, 1, 5, MemberCutKeepMeasured},
		{"no_ceiling_falls_back_to_half", 50, 0, 71, 0, 25, 25, MemberCutKeepHalf},
		{"no_overrun_falls_back_to_half", 50, 0, 60, 66, 25, 25, MemberCutKeepHalf},
	} {
		keep, need, by := memberCutKeep(row.listed, row.protected, row.items, row.maxItems)
		if keep != row.wantKeep || need != row.wantNeed || by != row.wantBy {
			t.Errorf("%s: keep=%d need=%d by=%s, want keep=%d need=%d by=%s", row.name, keep, need, by, row.wantKeep, row.wantNeed, row.wantBy)
		}
	}
}

// The member ranking put first is never in the cut order, however many are
// cut: the cut keeps the highest-attention members.
func TestTheTopAttentionMemberSurvivesEveryCut(t *testing.T) {
	members := make([]CohortMember, 0, 8)
	for index, rank := range []int{4, 2, 8, 1, 6, 3, 7, 5} {
		id := fmt.Sprintf("project_%03d", index)
		members = append(members, CohortMember{
			Subject: SubjectRef{Kind: SubjectProject, CanonicalID: id, Label: id}, Rank: index + 1,
			InclusionReasons: []string{"owned"}, RankingComputed: true, AttentionRank: rank,
		})
	}
	cohort := &Cohort{Kind: SubjectProject, Members: members, Rationale: "owned", Complete: true, ScoreMeaning: contractsv1.ContextFabricCohortScoreMeaningAttention}
	order, _ := memberCutOrder(cohort, contractsv1.ContextFabricNarrowingBasisAttentionRank, InvestigationResult{Cohort: cohort})
	if len(order) != len(members)-1 {
		t.Fatalf("order = %v, want every member but the top-ranked", order)
	}
	for _, index := range order {
		if members[index].AttentionRank == 1 {
			t.Fatalf("the top-attention member %s is in the cut order %v", members[index].Subject.CanonicalID, order)
		}
	}
}

// A list that is not teams, repositories or projects is never cut, and the
// event says why.
func TestACohortOfAnotherKindIsNotCut(t *testing.T) {
	for _, kind := range []SubjectKind{SubjectKind("person"), SubjectPullRequest, SubjectWorkItem} {
		cohort := &Cohort{Kind: kind, Members: []CohortMember{
			{Subject: SubjectRef{Kind: kind, CanonicalID: "a_001", Label: "a"}, RankingComputed: true, AttentionRank: 1},
			{Subject: SubjectRef{Kind: kind, CanonicalID: "a_002", Label: "b"}, RankingComputed: true, AttentionRank: 2},
		}, ScoreMeaning: contractsv1.ContextFabricCohortScoreMeaningAttention}
		attempt := (&Engine{}).planMemberCut(context.Background(), synthesisAssemblyParams{Graph: GraphContext{Cohort: cohort}}, assemblyTelemetry{}, InvestigationResult{Cohort: cohort}, 71, 66)
		if attempt.Declined != MemberCutCohortKind {
			t.Errorf("kind %q: declined = %q, want %q", kind, attempt.Declined, MemberCutCohortKind)
		}
	}
}
