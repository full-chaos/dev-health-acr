package contextfabric

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// A counting question over a flat cohort whose answer overruns the item
// budget, with the retry unavailable, is served with its trailing uncited
// members cut. The count obligation's outcome row is the cardinality of the
// member set the answer carries: it must describe the cut list and its declared
// size, once, and agree with the member list and the cut sentence.
const (
	memberCutCountMembers = 50
	memberCutCountGaps    = 21
)

// memberCutCountAnswer runs the counting fixture with a service byte ceiling
// and a caller answer budget (zero for none) and returns the served answer.
func memberCutCountAnswer(t *testing.T, maxBytes int64, answerBudget int) InvestigationResult {
	t.Helper()
	const (
		members = memberCutCountMembers
		gaps    = memberCutCountGaps
	)
	anchor := SubjectRef{Kind: SubjectOrganization, CanonicalID: "org_1", Label: "Org"}
	cohort := &Cohort{Kind: SubjectTeam, Rationale: "scope census match", Complete: true}
	for index := 0; index < members; index++ {
		id := fmt.Sprintf("team:COUNTED_%03d", index)
		cohort.Members = append(cohort.Members, CohortMember{
			Subject:          SubjectRef{Kind: SubjectTeam, CanonicalID: id, Label: id},
			Rank:             index + 1,
			InclusionReasons: []string{"matched"},
		})
	}
	telemetry := &recordingTelemetry{}
	frame := countingFrame(SubjectTeam)
	engine, err := NewEngine(EngineDependencies{
		Interpreter: familyInterpreter{
			interpreted: InterpretedQuestion{
				Shape: ShapeDiscoveredCohort, RequestedJudgment: "count",
				TimeContext: TimeContext{Axis: TemporalCurrent}, FactRequirements: []FactRequirement{},
			},
			outcome: QuestionFamilyOutcome{Frame: frame, FrameObligations: frame.Obligations, Family: QuestionFamilyScopedCohortStatus, Source: QuestionFamilySourceModel},
		},
		Graph: graphReaderStub{
			resolution: SubjectResolution{Candidates: []SubjectCandidate{scopeAnchorMatch(anchor)}, Committed: []SubjectRef{anchor}},
			bases:      provenCommitBases(anchor),
			context: GraphContext{
				Cohort: cohort, Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{},
				FactRequirements: []FactRequirement{}, EvidenceRefIDs: []string{},
				Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
			},
		},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{Facts: []CanonicalFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}, Version: "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{}}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			findings := make([]Finding, 0, gaps)
			for index := 0; index < gaps; index++ {
				findings = append(findings, Finding{FindingID: fmt.Sprintf("finding_gap_%02d", index), Kind: "relationship", Summary: "A readiness gap appears open.", EvidenceRefIDs: []string{"evidence_gap_01"}})
			}
			return InvestigationResult{
				Status: InvestigationComplete, DirectJudgment: "Counted.", CurrentState: "Nominal.",
				StrongestPressures: []string{}, Drivers: []DriverJudgment{}, RemainingWork: []Finding{},
				ReadinessGaps: findings, Paths: []RelationshipPath{}, Conflicts: []Finding{},
				Limitations: []string{}, EvidenceRefIDs: []string{"evidence_gap_01"}, ClaimedFacts: []ClaimedFact{},
				Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}, DeterministicAnswer: "Counted.", Warnings: []string{},
				Versions: VersionSet{Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1", InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1"},
			}, nil
		}),
		Results:      &resultStoreStub{},
		Telemetry:    telemetry,
		Requirements: registryDeriver{},
	}, EngineOptions{
		ServiceVersion: "acr-test", MaxItems: serverItemCeiling, MaxSerializedBytes: maxBytes,
		Now:         func() time.Time { return time.Unix(500, 0).UTC() },
		NewResultID: func() string { return "result_50210001" },
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	request := validInvestigationRequestWithConfirmedWindow()
	request.RequestID = "request_50210001"
	request.Question = "count question"
	request.Options.MaxCohortMembers = members
	request.Options.AnswerBudgetBytes = answerBudget
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	return result
}

// A counting question over a flat cohort whose answer overruns the item
// budget, with the retry unavailable, is served with its uncited members cut.
// The count obligation's outcome row is the cardinality of the member set the
// answer carries: it must describe the cut list and its declared size, once,
// and agree with the member list, the claim, the sentence and the cut sentence.
func TestAMemberCutAnswerToACountQuestionReconcilesItsCountRow(t *testing.T) {
	const members = memberCutCountMembers
	result := memberCutCountAnswer(t, 0, 0)

	served := len(result.Cohort.Members)
	if served >= members {
		t.Fatalf("served %d members: the case did not exercise the cut", served)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("the served answer is not valid: %v", err)
	}
	rows := countOutcomeRows(result, contractsv1.ContextFabricOutcomeStageAssembledResult)
	if len(rows) != 1 {
		t.Fatalf("assembled-result count rows = %d, want exactly 1: %+v", len(rows), result.Completeness.Outcomes)
	}
	row := rows[0]
	if row.Served != served || row.Declared != members {
		t.Fatalf("count row served/declared = %d/%d, want %d/%d (the cut list and its declared size)", row.Served, row.Declared, served, members)
	}
	if row.Outcome != contractsv1.ContextFabricRequirementNarrowed || row.CauseOverrun != contractsv1.ContextFabricBudgetOverrunItems || !row.CauseObserved {
		t.Fatalf("count row = %+v, want narrowed, caused by the item budget, observed", row)
	}
	sentence, _ := contractsv1.ContextFabricMemberCutLimitation(members-served, members, result.Cohort.ScoreMeaning != "")
	found := false
	for _, limitation := range result.Limitations {
		if limitation == sentence {
			found = true
		}
	}
	if !found {
		t.Fatalf("the cut sentence for %d of %d is missing: %q", members-served, members, result.Limitations)
	}
	// Every surface that states the count comes from the cut cohort's one
	// assembly: the claim, the answer sentence and the cohort chain.
	var claim *ClaimedFact
	for index := range result.ClaimedFacts {
		if result.ClaimedFacts[index].Field == "team_count" {
			claim = &result.ClaimedFacts[index]
		}
	}
	if claim == nil || claim.Value.Integer == nil || *claim.Value.Integer != int64(served) {
		t.Fatalf("cardinality claim = %+v, want %d (the listed members)", claim, served)
	}
	wantSentence := fmt.Sprintf("Counted %d teams of %d found.", served, members)
	if !strings.Contains(result.DeterministicAnswer, wantSentence) || strings.Contains(result.DeterministicAnswer, fmt.Sprintf("Counted %d teams.", members)) {
		t.Fatalf("answer %q, want it to state %q and not the uncut %d as the count", result.DeterministicAnswer, wantSentence, members)
	}
	chain := false
	for _, limitation := range result.Limitations {
		if contractsv1.IsContextFabricCohortNarrowingLimitation(limitation) && strings.Contains(limitation, fmt.Sprintf("lists %d of the %d ", served, members)) && strings.Contains(limitation, "exceeded the item budget") {
			chain = true
		}
	}
	if !chain {
		t.Fatalf("no cohort sentence stating %d of %d after the item budget: %q", served, members, result.Limitations)
	}
	scope := 0
	for _, outcome := range result.Completeness.Outcomes {
		if outcome.Impact == contractsv1.ContextFabricAnswerImpactScope && outcome.Declared == members && outcome.Served == served {
			scope++
		}
	}
	if scope < 1 {
		t.Fatalf("no outcome row states %d of %d at scope: %+v", served, members, result.Completeness.Outcomes)
	}
}

// A cut that continues through the service-ceiling fit and the decisive
// finalization still states the cut once and keeps the count row consistent:
// the caller's answer budget is below the cut answer's size, so the document is
// served at the ceiling after the cut.
func TestACutThatContinuesThroughTheCeilingFitKeepsTheScopeRowOnceAndTheCountRowConsistent(t *testing.T) {
	const (
		members = memberCutCountMembers
		ceiling = 65536
		option  = 8192
	)
	result := memberCutCountAnswer(t, ceiling, option)
	served := len(result.Cohort.Members)
	if served >= members {
		t.Fatalf("served %d members: the case did not exercise the cut", served)
	}
	if size := servedBytes(t, result); size <= option || size > ceiling {
		t.Fatalf("served %d bytes, want above the %d option and within the %d ceiling (the ceiling fit)", size, option, ceiling)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("the served answer is not valid: %v", err)
	}
	scope := 0
	for _, outcome := range result.Completeness.Outcomes {
		if outcome.Requirement == "" && outcome.Impact == contractsv1.ContextFabricAnswerImpactScope &&
			outcome.CauseOverrun == contractsv1.ContextFabricBudgetOverrunItems && outcome.Declared == members && outcome.Served == served {
			scope++
		}
	}
	if scope != 1 {
		t.Fatalf("scope rows for the cut = %d, want exactly 1: %+v", scope, result.Completeness.Outcomes)
	}
	sentence, _ := contractsv1.ContextFabricMemberCutLimitation(members-served, members, result.Cohort.ScoreMeaning != "")
	stated := 0
	for _, limitation := range result.Limitations {
		if limitation == sentence {
			stated++
		}
	}
	if stated != 1 {
		t.Fatalf("the cut sentence is stated %d times, want once: %q", stated, result.Limitations)
	}
	rows := countOutcomeRows(result, contractsv1.ContextFabricOutcomeStageAssembledResult)
	if len(rows) != 1 || rows[0].Served != served || rows[0].Declared != members {
		t.Fatalf("count rows = %+v, want one row %d of %d", rows, served, members)
	}
	var claim *ClaimedFact
	for index := range result.ClaimedFacts {
		if result.ClaimedFacts[index].Field == "team_count" {
			claim = &result.ClaimedFacts[index]
		}
	}
	if claim == nil || claim.Value.Integer == nil || *claim.Value.Integer != int64(served) {
		t.Fatalf("cardinality claim = %+v, want %d", claim, served)
	}
	if want := fmt.Sprintf("Counted %d teams of %d found.", served, members); !strings.Contains(result.DeterministicAnswer, want) {
		t.Fatalf("answer %q, want it to state %q", result.DeterministicAnswer, want)
	}
}
