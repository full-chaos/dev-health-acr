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
func TestAMemberCutAnswerToACountQuestionReconcilesItsCountRow(t *testing.T) {
	const (
		members = 50
		gaps    = 21
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
		ServiceVersion: "acr-test", MaxItems: serverItemCeiling,
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
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	served := len(result.Cohort.Members)
	for _, e := range telemetry.readRequirementObservationCovers {
		t.Logf("cover pass=%d evaluated=%d served=%v req=%s", e.Pass, e.EvaluatedPass, e.Served, e.Requirement)
	}
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
