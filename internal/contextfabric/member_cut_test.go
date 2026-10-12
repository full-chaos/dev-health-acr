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

// memberCutExtraGoals adds goals to the fixture's frame; a row that needs a
// derived read requirement sets it for its own duration.
var memberCutExtraGoals []InvestigationGoal

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
			context: GraphContext{Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{}, FactRequirements: []FactRequirement{}, EvidenceRefIDs: []string{},
				Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}},
		}, population},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{Facts: []CanonicalFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}, Version: "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{}}, nil
		}),
		Synthesizer: synthesizerFunc(func(_ context.Context, _ storage.Principal, input SynthesisInput) (InvestigationResult, error) {
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
				ReadinessGaps: findings, Paths: []RelationshipPath{}, Conflicts: []Finding{},
				Limitations: []string{}, EvidenceRefIDs: refs, ClaimedFacts: claims,
				Coverage: input.Facts.Coverage, DeterministicAnswer: "Fine, based on available context.", Warnings: []string{},
				Versions: VersionSet{Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1", InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1"},
			}, nil
		}),
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
	if result.Status != InvestigationPartial || !result.Coverage.Partial {
		t.Fatalf("status %q partial=%v: a cut answer is partial", result.Status, result.Coverage.Partial)
	}
	// The caller's member limit cut 60 to 50 before planning and no plan step
	// records it, so the cohort sentence (which would attribute the whole drop
	// from 60 to the item budget) is not written; the cut's own sentence states
	// the 5 of 50.
	for _, limitation := range result.Limitations {
		if contractsv1.IsContextFabricCohortNarrowingLimitation(limitation) {
			t.Fatalf("a cohort-narrowing sentence blames the item budget for a drop the caller's limit began: %q", limitation)
		}
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("the served answer is not valid: %v", err)
	}
	wantSentence, _ := contractsv1.ContextFabricMemberCutLimitation(memberCutCallerCap-len(result.Cohort.Members), memberCutCallerCap, result.Cohort.ScoreMeaning != "")
	stated := 0
	for _, limitation := range result.Limitations {
		if limitation == wantSentence {
			stated++
		}
	}
	if stated != 1 {
		t.Fatalf("the cut rule is stated %d times (%q), want once: %q", stated, wantSentence, result.Limitations)
	}
	cutRows := 0
	for _, row := range result.Completeness.Outcomes {
		if row.Impact == contractsv1.ContextFabricAnswerImpactScope && row.CauseOverrun == contractsv1.ContextFabricBudgetOverrunItems &&
			row.Declared == memberCutCallerCap && row.Served == len(result.Cohort.Members) {
			cutRows++
		}
	}
	if cutRows != 1 {
		t.Fatalf("scope outcome rows for the cut = %d, want 1: %+v", cutRows, result.Completeness.Outcomes)
	}
	// Evidence refs: a cut member's ref leaves the closure unless a kept site
	// cites it; a kept member's stays.
	keptMembers := map[string]bool{}
	for _, member := range result.Cohort.Members {
		keptMembers[fmt.Sprintf("ref_member_%02d", member.Rank-1)] = true
	}
	inClosure := contractsv1.ContextFabricEvidenceRefClosure(result)
	lastRef := fmt.Sprintf("ref_member_%02d", memberCutCallerCap-1)
	for index := 0; index < memberCutCallerCap; index++ {
		ref := fmt.Sprintf("ref_member_%02d", index)
		_, present := inClosure[ref]
		_, labelled := result.EvidenceRefLabels[ref]
		switch {
		case ref == lastRef:
			if !present || !labelled {
				t.Fatalf("%s is cited by a kept finding and must stay (closure %v, label %v)", ref, present, labelled)
			}
		case index < len(result.Cohort.Members):
			if !present {
				t.Fatalf("%s belongs to a kept member and left the closure", ref)
			}
		default:
			if present || labelled {
				t.Fatalf("%s belongs to a cut member nothing kept cites and is still on the result (closure %v, label %v)", ref, present, labelled)
			}
		}
	}
	cuts := 0
	for _, event := range telemetry.memberCuts {
		if event.Served {
			cuts++
			if event.Basis != contractsv1.ContextFabricNarrowingBasisAttentionRank || event.Listed != memberCutCallerCap || event.Kept != len(result.Cohort.Members) || event.Declined != MemberCutNotApplicable {
				t.Fatalf("member-cut event = %+v, want attention_rank, listed %d, kept %d", event, memberCutCallerCap, len(result.Cohort.Members))
			}
		}
	}
	// The cardinality claim and the answer sentence come from the cut cohort's
	// own assembly: they state the members the document lists.
	servedMembers := int64(len(result.Cohort.Members))
	for _, claim := range result.ClaimedFacts {
		if claim.Field == "project_count" && (claim.Value.Integer == nil || *claim.Value.Integer != servedMembers) {
			t.Fatalf("cardinality claim %+v does not state the %d listed members", claim.Value, servedMembers)
		}
	}
	if cuts != 1 {
		t.Fatalf("served member-cut events = %d, want 1", cuts)
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
	telemetry := &recordingTelemetry{}
	_, err := investigateMemberCut(t, memberCutEngine(t, 70, nil, 0, telemetry))
	var refusal AnswerBudgetRefusal
	if !errors.As(err, &refusal) || refusal.Overrun != contractsv1.ContextFabricBudgetOverrunItems {
		t.Fatalf("err = %v, want an items refusal", err)
	}
	// The refusal says why this lever declined: fitting would take every member.
	if len(telemetry.memberCuts) != 1 || telemetry.memberCuts[0].Served || telemetry.memberCuts[0].Declined != MemberCutWouldRemoveAll {
		t.Fatalf("member-cut events = %+v, want one declined would_remove_all", telemetry.memberCuts)
	}
}

// The caller's byte option is below the answer's size and the item budget is
// over as well. Items are reported first, so no byte lever ran: the member cut
// fits the items and the byte ladder continues from the cut document, which is
// served at the service ceiling instead of being refused on items.
func TestAnItemOverrunUnderACallerByteOptionIsServedAtTheServiceCeiling(t *testing.T) {
	const ceiling = 65536
	const option = 8192
	engine := memberCutEngineAtCeiling(t, memberCutGaps, nil, 0, ceiling, memberCutPopulation, &recordingTelemetry{})
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
	// project_005 (attention 4) is named by a finding and is never cut.
	result := InvestigationResult{
		Cohort:        &Cohort{Kind: SubjectProject, Members: members, Rationale: "owned", Complete: true, ScoreMeaning: contractsv1.ContextFabricCohortScoreMeaningAttention},
		ReadinessGaps: []Finding{{FindingID: "finding_gap_00", Kind: "relationship", Summary: "A gap.", Subjects: []SubjectRef{members[5].Subject}, EvidenceRefIDs: []string{"evidence_gap_01"}}},
	}
	basis, order, ok := memberCutOrder(result, nil)
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

// An unranked list sorted by canonical id is cut from the end of the list and
// says so, in the sentence variant that names no attention rank.
func TestAnUnrankedSortedListIsCutFromTheEndAndTheSentenceSaysSo(t *testing.T) {
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
	basis, order, ok := memberCutOrder(result, nil)
	if !ok || basis != contractsv1.ContextFabricNarrowingBasisCanonicalIDLexical {
		t.Fatalf("basis %q ok=%v, want canonical_id_lexical", basis, ok)
	}
	// Cited project_005 stays; the two cut are the last two uncited, from the end.
	if got := fmt.Sprint(order[:2]); got != fmt.Sprint([]int{4, 3}) {
		t.Fatalf("cut order %v, want [4 3]", order[:2])
	}
	wantSentence, _ := contractsv1.ContextFabricMemberCutLimitation(2, 6, false)
	if !strings.Contains(wantSentence, "from the end of the list") || strings.Contains(wantSentence, "attention") {
		t.Fatalf("sentence %q, want the end-of-list variant", wantSentence)
	}
	if !contractsv1.IsContextFabricMemberCutLimitation(wantSentence) {
		t.Fatal("the end-of-list variant is not recognised by the parser")
	}
}

func currentStateFor(hook func(*Cohort) string, cohort *Cohort) string {
	if hook == nil {
		return "Nominal."
	}
	return hook(cohort)
}

// A member the draft's prose names, and no claim, driver or finding does, is
// not cuttable: the sentence would describe a list the answer no longer has.
func TestAMemberNamedOnlyInTheDraftProseIsNotCut(t *testing.T) {
	last := memberCutCallerCap - 1
	named := fmt.Sprintf("project_%03d", last)
	engine := memberCutEngineNaming(t, memberCutGaps, nil, 0, 0, memberCutPopulation, &recordingTelemetry{}, func(cohort *Cohort) string {
		return "The review queue on " + strings.ToUpper(named) + " is the one to watch."
	}, registryDeriver{})
	result, err := investigateMemberCut(t, engine)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if len(result.Cohort.Members) >= memberCutCallerCap {
		t.Fatalf("served %d members: the case did not cut", len(result.Cohort.Members))
	}
	kept := false
	for _, member := range result.Cohort.Members {
		if member.Subject.CanonicalID == named {
			kept = true
		}
	}
	if !kept {
		t.Fatalf("%s is named in the draft's current_state and was cut", named)
	}
	// The same fixture without the prose cuts it: the lowest attention rank
	// goes first, and with equal scores that is the last of the pool.
	plain, err := investigateMemberCut(t, memberCutEngine(t, memberCutGaps, nil, 0, &recordingTelemetry{}))
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range plain.Cohort.Members {
		if member.Subject.CanonicalID == named {
			t.Fatalf("control: %s was kept without the prose naming it; the row cannot discriminate", named)
		}
	}
}

// The cut pass is a pass of its own: its observation-cover events carry a pass
// number above every discarded attempt's, are published once, and are the only
// ones marked served; pass one's are kept as not served.
func TestTheCutPassObservationCoverIsPublishedOnceAndMarkedServed(t *testing.T) {
	memberCutExtraGoals = []InvestigationGoal{GoalAssessState}
	defer func() { memberCutExtraGoals = nil }()
	capability := FactCapability{
		Kind: FactHealth, SupportedSubjectKinds: []SubjectKind{SubjectProject},
		Obligations: map[SubjectKind][]AnswerObligation{SubjectProject: {ObligationState}},
	}
	telemetry := &recordingTelemetry{}
	engine := memberCutEngineNaming(t, memberCutGaps, nil, 0, 0, memberCutPopulation, telemetry, nil, registryDeriver{capabilities: []FactCapability{capability}})
	result, err := investigateMemberCut(t, engine)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if len(result.Cohort.Members) >= memberCutCallerCap {
		t.Fatalf("served %d members: the case did not cut", len(result.Cohort.Members))
	}
	covers := telemetry.readRequirementObservationCovers
	if len(covers) != 2 {
		t.Fatalf("observation-cover lines = %d, want 2 (pass one not served, the cut pass served): %+v", len(covers), covers)
	}
	var served, discarded *ReadRequirementObservationCoverEvent
	for index := range covers {
		if covers[index].Served {
			if served != nil {
				t.Fatalf("two lines are marked served: %+v", covers)
			}
			served = &covers[index]
		} else {
			discarded = &covers[index]
		}
	}
	if served == nil || discarded == nil {
		t.Fatalf("want one served and one discarded line: %+v", covers)
	}
	if served.Pass != answerPassThird || discarded.Pass != answerPassFirst {
		t.Fatalf("served pass %d, discarded pass %d, want %d and %d: the cut pass is numbered above every earlier attempt", served.Pass, discarded.Pass, answerPassThird, answerPassFirst)
	}
	if telemetry.memberCuts[0].Passes < 2 {
		t.Fatalf("member-cut event reports %d assembly passes, want the search's count", telemetry.memberCuts[0].Passes)
	}
}

// A one- or two-character label is matched as a whole word: "AI" in "the AI
// team" keeps its member listed, and "said" does not.
func TestAShortLabelIsMatchedAsAWholeWordInTheDraftProse(t *testing.T) {
	member := SubjectRef{Kind: SubjectProject, CanonicalID: "project_001", Label: "AI"}
	if !namedInProse(member, draftProse(InvestigationResult{CurrentState: "The AI team is stretched."})) {
		t.Fatal("a one-word mention of the two-character label is not matched")
	}
	if namedInProse(member, draftProse(InvestigationResult{CurrentState: "Reviewers said it is fine."})) {
		t.Fatal("the label matched inside another word")
	}
	if !namedInProse(SubjectRef{Kind: SubjectProject, CanonicalID: "x", Label: "A"}, draftProse(InvestigationResult{CurrentState: "Team A is late."})) {
		t.Fatal("a one-character label mentioned as a word is not matched")
	}
}
