package contextfabric

// A count describes the requested population or it describes nothing.
//
// Every cell drives Engine.Investigate (or the reuse path through it) and
// reads the SERVED document and the recorded decision. None builds the row,
// claim or sentence it asserts on.

import (
	"context"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func scopeAnchorRepository() SubjectRef {
	return SubjectRef{Kind: SubjectRepository, CanonicalID: "repository:SCOPE_ANCHOR", Label: "scope anchor"}
}

func scopeTeamAnchor() SubjectRef {
	return SubjectRef{Kind: SubjectTeam, CanonicalID: "team:SCOPE_ANCHOR", Label: "scope anchor team"}
}

func scopeCandidate(subject SubjectRef, receipt string) SubjectCandidate {
	return SubjectCandidate{
		ReceiptID: receipt, Subject: subject, State: ResolutionAmbiguous,
		MatchReasons: []string{"matched"}, Confidence: 0.5, EvidenceRefIDs: []string{},
	}
}

// kindCohort is countingCohort for any member kind, with ids of that kind.
func kindCohort(kind SubjectKind, size int) *Cohort {
	cohort := countingCohort(kind, size)
	for index := range cohort.Members {
		cohort.Members[index].Subject.CanonicalID = string(kind) + ":COUNTED_" + string(rune('A'+index))
	}
	return cohort
}

type scopeCell struct {
	name       string
	frame      *QuestionFrame
	family     QuestionFamily
	resolution SubjectResolution
	cohort     *Cohort
	status     InvestigationStatus

	wantDecision  CountPopulationScopeDecision
	wantCounted   bool
	wantServed    int
	wantAnchors   int
	wantSentence  string
	wantAssembled contractsv1.ContextFabricPlanRequirementOutcome
}

func newScopeEngine(t *testing.T, cell scopeCell, telemetry EngineTelemetry) *Engine {
	t.Helper()
	engine, err := NewEngine(EngineDependencies{
		Interpreter: familyInterpreter{
			interpreted: InterpretedQuestion{
				Shape: ShapeSingleSubject, RequestedJudgment: "count",
				TimeContext: TimeContext{Axis: TemporalCurrent}, FactRequirements: []FactRequirement{},
			},
			outcome: QuestionFamilyOutcome{
				Frame: cell.frame, FrameObligations: cell.frame.Obligations,
				Family: cell.family, Source: QuestionFamilySourceModel,
			},
		},
		Graph: graphReaderStub{
			resolution: cell.resolution,
			context: GraphContext{
				Cohort: cell.cohort, Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{},
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
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return InvestigationResult{
				Status: cell.status, DirectJudgment: "Answered.",
				CurrentState: "Nominal.", StrongestPressures: []string{}, Drivers: []DriverJudgment{},
				RemainingWork: []Finding{}, ReadinessGaps: []Finding{}, Paths: []RelationshipPath{},
				Conflicts: []Finding{}, Limitations: []string{}, EvidenceRefIDs: []string{},
				ClaimedFacts:        []ClaimedFact{},
				Coverage:            Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				DeterministicAnswer: "Answered.", Warnings: []string{},
				Versions: VersionSet{
					Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
					InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1",
				},
			}, nil
		}),
		Results:      &resultStoreStub{},
		Telemetry:    telemetry,
		Requirements: registryDeriver{},
	}, EngineOptions{
		ServiceVersion: "acr-test",
		Now:            func() time.Time { return time.Unix(500, 0).UTC() },
		NewResultID:    func() string { return "result_57750001" },
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	return engine
}

func runScopeCell(t *testing.T, ctx context.Context, engine *Engine) InvestigationResult {
	t.Helper()
	request := validInvestigationRequestWithConfirmedWindow()
	request.RequestID = "request_57750001"
	request.Question = "scope fixture"
	result, err := engine.Investigate(ctx, storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	return result
}

func scopeCells() []scopeCell {
	orgTeam := SubjectTeam
	return []scopeCell{
		{
			// The observed defect's shape: the anchor committed nothing, a
			// member set of the right kind still reached assembly, and
			// synthesis ended no_match.
			name: "scoped count, anchor unresolved, no_match", frame: countingFrame(SubjectTeam), family: QuestionFamilyScopedCohortStatus,
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
			cohort:     kindCohort(SubjectTeam, 3), status: InvestigationNoMatch,
			wantDecision: CountPopulationScopeAnchorUnresolved, wantAssembled: contractsv1.ContextFabricRequirementUnavailable,
		},
		{
			name: "scoped count, anchor unresolved, synthesis complete", frame: countingFrame(SubjectTeam), family: QuestionFamilyScopedCohortStatus,
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
			cohort:     kindCohort(SubjectTeam, 3), status: InvestigationComplete,
			wantDecision: CountPopulationScopeAnchorUnresolved, wantAssembled: contractsv1.ContextFabricRequirementUnavailable,
		},
		{
			name: "scoped count, one uncommitted candidate", frame: countingFrame(SubjectTeam), family: QuestionFamilyScopedCohortStatus,
			resolution: SubjectResolution{Candidates: []SubjectCandidate{scopeCandidate(scopeAnchorRepository(), "receipt_scope_01")}, Committed: []SubjectRef{}},
			cohort:     kindCohort(SubjectTeam, 3), status: InvestigationComplete,
			wantDecision: CountPopulationScopeAnchorUnresolved, wantAssembled: contractsv1.ContextFabricRequirementUnavailable,
		},
		{
			name: "scoped count, anchor ambiguous", frame: countingFrame(SubjectTeam), family: QuestionFamilyScopedCohortStatus,
			resolution: SubjectResolution{Candidates: []SubjectCandidate{
				scopeCandidate(scopeAnchorRepository(), "receipt_scope_01"),
				scopeCandidate(SubjectRef{Kind: SubjectRepository, CanonicalID: "repository:SCOPE_OTHER", Label: "other"}, "receipt_scope_02"),
			}, Committed: []SubjectRef{}},
			cohort: kindCohort(SubjectTeam, 3), status: InvestigationComplete,
			wantDecision: CountPopulationScopeAnchorAmbiguous, wantAssembled: contractsv1.ContextFabricRequirementUnavailable,
		},
		{
			// A committed subject of the MEMBER kind is a member, not the
			// anchor the members hang off.
			name: "scoped count, only a member-kind subject committed", frame: countingFrame(SubjectTeam), family: QuestionFamilyScopedCohortStatus,
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{{Kind: SubjectTeam, CanonicalID: "team:COUNTED_A", Label: "Counted A"}}},
			cohort:     kindCohort(SubjectTeam, 3), status: InvestigationComplete,
			wantDecision: CountPopulationScopeAnchorUnresolved, wantAssembled: contractsv1.ContextFabricRequirementUnavailable,
		},
		{
			name: "scoped count, anchor committed", frame: countingFrame(SubjectTeam), family: QuestionFamilyScopedCohortStatus,
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{scopeAnchorRepository()}},
			cohort:     kindCohort(SubjectTeam, 3), status: InvestigationComplete,
			wantDecision: CountPopulationScopeAnchorCommitted, wantCounted: true, wantServed: 3, wantAnchors: 1,
			wantSentence: "Counted 3 teams.", wantAssembled: contractsv1.ContextFabricRequirementSatisfied,
		},
		{
			// A MEANINGFUL ZERO: the anchor resolved and its measured member
			// set is empty.
			name: "scoped count, anchor committed, empty measured population", frame: countingFrame(SubjectTeam), family: QuestionFamilyScopedCohortStatus,
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{scopeAnchorRepository()}},
			cohort:     &Cohort{Kind: SubjectTeam, Rationale: "scope census match", Members: []CohortMember{}, Complete: true}, status: InvestigationComplete,
			wantDecision: CountPopulationScopeAnchorCommitted, wantCounted: true, wantServed: 0, wantAnchors: 1,
			wantSentence: "Counted 0 teams.", wantAssembled: contractsv1.ContextFabricRequirementSatisfied,
		},
		{
			name: "scoped count, anchor committed, population unmeasured", frame: countingFrame(SubjectTeam), family: QuestionFamilyScopedCohortStatus,
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{scopeAnchorRepository()}},
			cohort:     nil, status: InvestigationComplete,
			wantDecision: CountPopulationScopeAnchorCommitted, wantAnchors: 1, wantAssembled: contractsv1.ContextFabricRequirementUnavailable,
		},
		{
			// Structurally different: repositories under a team anchor.
			name: "scoped repository count, team anchor committed", frame: countingFrame(SubjectRepository), family: QuestionFamilyScopedCohortStatus,
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{scopeTeamAnchor()}},
			cohort:     kindCohort(SubjectRepository, 2), status: InvestigationComplete,
			wantDecision: CountPopulationScopeAnchorCommitted, wantCounted: true, wantServed: 2, wantAnchors: 1,
			wantSentence: "Counted 2 repositorys.", wantAssembled: contractsv1.ContextFabricRequirementSatisfied,
		},
		{
			name: "scoped project count, anchor unresolved", frame: countingFrame(SubjectProject), family: QuestionFamilyScopedCohortStatus,
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
			cohort:     kindCohort(SubjectProject, 4), status: InvestigationPartial,
			wantDecision: CountPopulationScopeAnchorUnresolved, wantAssembled: contractsv1.ContextFabricRequirementUnavailable,
		},
		{
			// A LEGITIMATE ORGANIZATION COUNT: a discovered kind has no anchor
			// to resolve, so nothing committed is not a scope loss.
			name: "organization-level discovered count, nothing committed", frame: frameWithPointer([]InvestigationGoal{GoalCountOrAggregate}, discoveredExpression(SubjectTeam)), family: QuestionFamilyDiscoveredCohortRanking,
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
			cohort:     kindCohort(SubjectTeam, 5), status: InvestigationComplete,
			wantDecision: CountPopulationScopeOrganization, wantCounted: true, wantServed: 5,
			wantSentence: "Counted 5 teams.", wantAssembled: contractsv1.ContextFabricRequirementSatisfied,
		},
		{
			name: "organization scope count, nothing committed", frame: frameWithPointer([]InvestigationGoal{GoalCountOrAggregate}, orgExpression(&orgTeam)), family: QuestionFamilySubjectInvestigation,
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
			cohort:     kindCohort(SubjectTeam, 2), status: InvestigationComplete,
			wantDecision: CountPopulationScopeOrganization, wantCounted: true, wantServed: 2,
			wantSentence: "Counted 2 teams.", wantAssembled: contractsv1.ContextFabricRequirementSatisfied,
		},
	}
}

func frameWithPointer(goals []InvestigationGoal, expression SubjectExpression) *QuestionFrame {
	frame := frameWith(goals, expression, TemporalIntentCurrent, nil)
	return &frame
}

func TestACountIsServedOnlyOverTheRequestedPopulation(t *testing.T) {
	t.Parallel()
	for _, cell := range scopeCells() {
		cell := cell
		t.Run(cell.name, func(t *testing.T) {
			t.Parallel()
			telemetry := &recordingTelemetry{}
			result := runScopeCell(t, context.Background(), newScopeEngine(t, cell, telemetry))

			// The terminal is never rewritten to make room for a count.
			if result.Status != cell.status {
				t.Fatalf("status = %q, want %q -- the scope decision must not change the terminal", result.Status, cell.status)
			}
			claim := cardinalityClaimOf(result)
			rows := countOutcomeRows(result, contractsv1.ContextFabricOutcomeStageAssembledResult)
			if len(rows) != 1 {
				t.Fatalf("assembled count rows = %d, want 1", len(rows))
			}
			if rows[0].Outcome != cell.wantAssembled {
				t.Errorf("assembled count row outcome = %q, want %q (row %+v)", rows[0].Outcome, cell.wantAssembled, rows[0])
			}
			hasSentence := strings.Contains(result.DeterministicAnswer, "Counted ")
			if cell.wantCounted {
				if claim == nil || claim.Value.Integer == nil || *claim.Value.Integer != int64(cell.wantServed) {
					t.Errorf("count claim = %+v, want value %d", claim, cell.wantServed)
				}
				if rows[0].Served != cell.wantServed {
					t.Errorf("row served = %d, want %d", rows[0].Served, cell.wantServed)
				}
				if !strings.Contains(result.DeterministicAnswer, cell.wantSentence) {
					t.Errorf("answer = %q, want it to state %q", result.DeterministicAnswer, cell.wantSentence)
				}
			} else {
				if claim != nil {
					t.Errorf("a count claim was served over a population that is not the requested one: %+v", *claim)
				}
				if hasSentence {
					t.Errorf("the answer states a count over a population that is not the requested one: %q", result.DeterministicAnswer)
				}
				if rows[0].CauseCoverage == "" || !rows[0].CauseObserved {
					t.Errorf("withheld count row names no observed cause: %+v", rows[0])
				}
				if result.Completeness.State == contractsv1.ContextFabricAnswerCompletenessComplete {
					t.Errorf("completeness = complete on an answer whose owed count was not established")
				}
			}

			if len(telemetry.countPopulationScopes) != 1 {
				t.Fatalf("count population scope events = %d, want exactly 1", len(telemetry.countPopulationScopes))
			}
			event := telemetry.countPopulationScopes[0]
			if event.Scope.Decision != cell.wantDecision {
				t.Errorf("decision = %q, want %q", event.Scope.Decision, cell.wantDecision)
			}
			if event.Counted != cell.wantCounted || event.Served != rows[0].Served || event.Assembly != rows[0].Outcome {
				t.Errorf("event post-decision = counted %t served %d assembled %q; served document says counted %t served %d assembled %q",
					event.Counted, event.Served, event.Assembly, cell.wantCounted, rows[0].Served, rows[0].Outcome)
			}
			if event.Scope.Committed != len(cell.resolution.Committed) || event.Scope.CommittedAnchors != cell.wantAnchors || event.Scope.Candidates != len(cell.resolution.Candidates) {
				t.Errorf("event measured committed=%d anchors=%d candidates=%d, want %d/%d/%d",
					event.Scope.Committed, event.Scope.CommittedAnchors, event.Scope.Candidates,
					len(cell.resolution.Committed), cell.wantAnchors, len(cell.resolution.Candidates))
			}
			if event.MemberSetResolved != (cell.cohort != nil) || (cell.cohort != nil && event.Members != len(cell.cohort.Members)) {
				t.Errorf("event member set resolved=%t members=%d, fixture cohort %+v", event.MemberSetResolved, event.Members, cell.cohort)
			}
			if event.Scope.Family != cell.family || event.Reused {
				t.Errorf("event family=%q reused=%t, want %q/false", event.Scope.Family, event.Reused, cell.family)
			}
		})
	}
}

// TestDecideCountPopulationScopeCoversItsInputDomain enumerates the decision's
// whole input domain: plan absent, every family, and committed/candidate
// shapes at their boundaries.
func TestDecideCountPopulationScopeCoversItsInputDomain(t *testing.T) {
	t.Parallel()
	member := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:M", Label: "m"}
	anchor := scopeAnchorRepository()
	candidate := scopeCandidate(anchor, "receipt_scope_01")
	resolutions := []struct {
		name       string
		resolution SubjectResolution
		anchors    int
	}{
		{"empty", SubjectResolution{}, 0},
		{"one candidate", SubjectResolution{Candidates: []SubjectCandidate{candidate}}, 0},
		{"two candidates", SubjectResolution{Candidates: []SubjectCandidate{candidate, candidate}}, 0},
		{"member committed", SubjectResolution{Committed: []SubjectRef{member}}, 0},
		{"member committed, two candidates", SubjectResolution{Committed: []SubjectRef{member}, Candidates: []SubjectCandidate{candidate, candidate}}, 0},
		{"anchor committed", SubjectResolution{Committed: []SubjectRef{anchor}}, 1},
		{"anchor and member committed", SubjectResolution{Committed: []SubjectRef{member, anchor}}, 1},
		{"two anchors committed", SubjectResolution{Committed: []SubjectRef{anchor, scopeTeamAnchor()}}, 1},
	}
	for _, resolution := range resolutions {
		got := DecideCountPopulationScope(nil, resolution.resolution)
		if got.Decision != CountPopulationScopePlanAbsent || got.Counts() {
			t.Errorf("plan absent / %s: decision %q counts %t, want plan_absent and not counted", resolution.name, got.Decision, got.Counts())
		}
	}
	for _, family := range QuestionFamilyVocabulary() {
		for _, resolution := range resolutions {
			plan := &AnswerPlan{Family: family, MemberKind: SubjectTeam}
			got := DecideCountPopulationScope(plan, resolution.resolution)
			want := CountPopulationScopeOrganization
			if family == QuestionFamilyScopedCohortStatus {
				switch {
				case resolution.anchors > 0:
					want = CountPopulationScopeAnchorCommitted
				case len(resolution.resolution.Candidates) > 1:
					want = CountPopulationScopeAnchorAmbiguous
				default:
					want = CountPopulationScopeAnchorUnresolved
				}
			}
			if got.Decision != want {
				t.Errorf("%s / %s: decision %q, want %q", family, resolution.name, got.Decision, want)
			}
			wantAnchors := 0
			for _, subject := range resolution.resolution.Committed {
				if subject.Kind != SubjectTeam {
					wantAnchors++
				}
			}
			if got.CommittedAnchors != wantAnchors || got.Committed != len(resolution.resolution.Committed) || got.Candidates != len(resolution.resolution.Candidates) {
				t.Errorf("%s / %s: measured %d/%d/%d", family, resolution.name, got.Committed, got.CommittedAnchors, got.Candidates)
			}
			if got.Counts() != (want == CountPopulationScopeOrganization || want == CountPopulationScopeAnchorCommitted) {
				t.Errorf("%s / %s: counts %t for decision %q", family, resolution.name, got.Counts(), got.Decision)
			}
		}
	}
}

// TestAReusedCountIsHeldToTheSameScopeDecision drives the reuse backfill: a
// stored document that owes a count and carries no count row is judged by the
// same decision, over its own stored plan and resolution.
func TestAReusedCountIsHeldToTheSameScopeDecision(t *testing.T) {
	t.Parallel()
	scopedTeams := &AnswerPlan{Family: QuestionFamilyScopedCohortStatus, FamilyVersion: QuestionFamilyTableVersion, MemberKind: SubjectTeam}
	for _, cell := range []struct {
		name         string
		plan         *AnswerPlan
		withAnchor   bool
		wantDecision CountPopulationScopeDecision
		wantCounted  bool
	}{
		{"anchor committed", scopedTeams, true, CountPopulationScopeAnchorCommitted, true},
		{"anchor unresolved", scopedTeams, false, CountPopulationScopeAnchorUnresolved, false},
		{"plan absent", nil, true, CountPopulationScopePlanAbsent, false},
	} {
		cell := cell
		t.Run(cell.name, func(t *testing.T) {
			t.Parallel()
			project, candidate := reusableCandidate()
			candidate.Cohort = countingCohort(SubjectTeam, 3)
			candidate.Completeness.Outcomes = []RequirementOutcomeRow{{
				Stage:       contractsv1.ContextFabricOutcomeStagePlanning,
				Requirement: string(ObligationCount) + "/" + string(SubjectRoleMember) + "/" + string(SubjectTeam),
				Obligation:  string(ObligationCount),
				Outcome:     contractsv1.ContextFabricRequirementSatisfied,
				Impact:      contractsv1.ContextFabricAnswerImpactNone,
			}}
			candidate.AnswerPlan = cell.plan
			committed := []SubjectRef{}
			if cell.withAnchor {
				committed = append(committed, project)
			}
			for _, member := range candidate.Cohort.Members {
				committed = append(committed, member.Subject)
			}
			candidate.SubjectResolution = SubjectResolution{Candidates: []SubjectCandidate{}, Committed: committed}
			candidate.Completeness = ComputeAnswerCompleteness(candidate)
			telemetry := &recordingTelemetry{}
			engine := mustReuseTestEngine(t, EngineDependencies{
				Graph:     graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: committed}},
				Results:   &resultStoreStub{},
				Telemetry: telemetry,
				ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
					return candidate, true, nil
				}),
			})
			served, err := engine.Investigate(context.Background(), reusePrincipal(), validInvestigationRequest())
			if err != nil {
				t.Fatalf("Investigate() error = %v", err)
			}
			if !served.Reused {
				t.Fatal("the fixture did not take the reuse path, so it proves nothing about it")
			}
			rows := countOutcomeRows(served, contractsv1.ContextFabricOutcomeStageAssembledResult)
			if len(rows) != 1 {
				t.Fatalf("assembled count rows = %d, want 1", len(rows))
			}
			claim := cardinalityClaimOf(served)
			sentence := strings.Contains(served.DeterministicAnswer, "Counted ")
			if cell.wantCounted {
				if rows[0].Outcome != contractsv1.ContextFabricRequirementSatisfied || rows[0].Served != 3 || claim == nil || !sentence {
					t.Errorf("reused count not stated: row %+v claim %+v answer %q", rows[0], claim, served.DeterministicAnswer)
				}
			} else if rows[0].Outcome != contractsv1.ContextFabricRequirementUnavailable || claim != nil || sentence {
				t.Errorf("reused count stated over an unestablished population: row %+v claim %+v answer %q", rows[0], claim, served.DeterministicAnswer)
			}
			if len(telemetry.countPopulationScopes) != 1 {
				t.Fatalf("count population scope events = %d, want 1", len(telemetry.countPopulationScopes))
			}
			event := telemetry.countPopulationScopes[0]
			if event.Scope.Decision != cell.wantDecision || event.Counted != cell.wantCounted || !event.Reused {
				t.Errorf("event decision=%q counted=%t reused=%t, want %q/%t/true", event.Scope.Decision, event.Counted, event.Reused, cell.wantDecision, cell.wantCounted)
			}
		})
	}
}
