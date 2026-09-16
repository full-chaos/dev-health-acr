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

// scopeAnchorMatch is the candidate resolution records for a subject that
// matched the fixture frame's anchor term "a" (scopedExpression's anchor).
func scopeAnchorMatch(subject SubjectRef, matched ...string) SubjectCandidate {
	if len(matched) == 0 {
		matched = []string{"a"}
	}
	return SubjectCandidate{
		ReceiptID: "receipt_scope_anchor", Subject: subject, State: ResolutionCommitted,
		MatchedTerms: matched, MatchReasons: []string{"matched"}, Confidence: 1, EvidenceRefIDs: []string{},
	}
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
	population int
	status     InvestigationStatus
	anchorKind SubjectKind
	bases      CommitBasisSet

	wantDecision  CountPopulationScopeDecision
	wantCounted   bool
	wantServed    int
	wantAnchors   int
	wantUnbound   int
	wantAnchorID  string
	wantSentence  string
	wantAssembled contractsv1.ContextFabricPlanRequirementOutcome
	// wantSubjectKind/wantSubjectID are the served cardinality claim's own
	// subject -- checked only when wantCounted, since an uncounted cell mints
	// no claim at all. anchor_committed wants the resolved anchor's own kind
	// and id; organization_scope wants the investigation's own organization.
	wantSubjectKind SubjectKind
	wantSubjectID   string
}

func newScopeEngine(t *testing.T, cell scopeCell, telemetry EngineTelemetry, gates ...AnswerReuseGate) *Engine {
	t.Helper()
	var gate AnswerReuseGate
	if len(gates) > 0 {
		gate = gates[0]
	}
	engine, err := NewEngine(EngineDependencies{
		ReuseGate: gate,
		Interpreter: familyInterpreter{
			interpreted: InterpretedQuestion{
				Shape: ShapeSingleSubject, RequestedJudgment: "count",
				TimeContext: TimeContext{Axis: TemporalCurrent}, FactRequirements: []FactRequirement{},
			},
			outcome: QuestionFamilyOutcome{
				Frame: cell.frame, FrameObligations: cell.frame.Obligations,
				Family: cell.family, Source: QuestionFamilySourceModel,
				WinningSampleIndex: 0,
				WinningSample:      FamilySample{ScopeAnchorKind: cell.anchorKind},
			},
		},
		Graph: graphReaderStub{
			resolution: cell.resolution,
			bases:      cell.bases,
			context: GraphContext{
				Cohort: cell.cohort, CohortPopulation: cell.population, Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{},
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
			// Candidates of the member kind cannot be the anchor, so many of
			// them do not make an unbound anchor ambiguous.
			name: "scoped count, many member candidates, no anchor candidate", frame: countingFrame(SubjectTeam), family: QuestionFamilyScopedCohortStatus,
			resolution: SubjectResolution{Candidates: []SubjectCandidate{
				scopeCandidate(SubjectRef{Kind: SubjectTeam, CanonicalID: "team:COUNTED_A", Label: "Counted A"}, "receipt_scope_11"),
				scopeCandidate(SubjectRef{Kind: SubjectTeam, CanonicalID: "team:COUNTED_B", Label: "Counted B"}, "receipt_scope_12"),
			}, Committed: []SubjectRef{}},
			cohort: kindCohort(SubjectTeam, 3), status: InvestigationComplete,
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
			resolution: SubjectResolution{Candidates: []SubjectCandidate{scopeAnchorMatch(scopeAnchorRepository())}, Committed: []SubjectRef{scopeAnchorRepository()}},
			cohort:     kindCohort(SubjectTeam, 3), status: InvestigationComplete,
			wantDecision: CountPopulationScopeAnchorCommitted, wantCounted: true, wantServed: 3, wantAnchors: 1, wantAnchorID: "repository:SCOPE_ANCHOR",
			wantSentence: "Counted 3 teams.", wantAssembled: contractsv1.ContextFabricRequirementSatisfied,
			wantSubjectKind: SubjectRepository, wantSubjectID: "repository:SCOPE_ANCHOR",
		},
		{
			// A partial truth: retrieval counted more members than the answer
			// carries, under a committed anchor. Stated as narrowed, not withheld.
			name: "scoped count, anchor committed, population larger than served", frame: countingFrame(SubjectTeam), family: QuestionFamilyScopedCohortStatus,
			resolution: SubjectResolution{Candidates: []SubjectCandidate{scopeAnchorMatch(scopeAnchorRepository())}, Committed: []SubjectRef{scopeAnchorRepository()}},
			cohort:     kindCohort(SubjectTeam, 3), population: 5, status: InvestigationComplete,
			wantDecision: CountPopulationScopeAnchorCommitted, wantCounted: true, wantServed: 3, wantAnchors: 1, wantAnchorID: "repository:SCOPE_ANCHOR",
			wantSentence: "Counted 3 teams of 5 found.", wantAssembled: contractsv1.ContextFabricRequirementNarrowed,
			wantSubjectKind: SubjectRepository, wantSubjectID: "repository:SCOPE_ANCHOR",
		},
		{
			// A MEANINGFUL ZERO: the anchor resolved and its measured member
			// set is empty.
			name: "scoped count, anchor committed, empty measured population", frame: countingFrame(SubjectTeam), family: QuestionFamilyScopedCohortStatus,
			resolution: SubjectResolution{Candidates: []SubjectCandidate{scopeAnchorMatch(scopeAnchorRepository())}, Committed: []SubjectRef{scopeAnchorRepository()}},
			cohort:     &Cohort{Kind: SubjectTeam, Rationale: "scope census match", Members: []CohortMember{}, Complete: true}, status: InvestigationComplete,
			wantDecision: CountPopulationScopeAnchorCommitted, wantCounted: true, wantServed: 0, wantAnchors: 1, wantAnchorID: "repository:SCOPE_ANCHOR",
			wantSentence: "Counted 0 teams.", wantAssembled: contractsv1.ContextFabricRequirementSatisfied,
			wantSubjectKind: SubjectRepository, wantSubjectID: "repository:SCOPE_ANCHOR",
		},
		{
			name: "scoped count, anchor committed, population unmeasured", frame: countingFrame(SubjectTeam), family: QuestionFamilyScopedCohortStatus,
			resolution: SubjectResolution{Candidates: []SubjectCandidate{scopeAnchorMatch(scopeAnchorRepository())}, Committed: []SubjectRef{scopeAnchorRepository()}},
			cohort:     nil, status: InvestigationComplete,
			wantDecision: CountPopulationScopeAnchorCommitted, wantAnchors: 1, wantAnchorID: "repository:SCOPE_ANCHOR", wantAssembled: contractsv1.ContextFabricRequirementUnavailable,
		},
		{
			// Structurally different: repositories under a team anchor.
			name: "scoped repository count, team anchor committed", frame: countingFrame(SubjectRepository), family: QuestionFamilyScopedCohortStatus,
			resolution: SubjectResolution{Candidates: []SubjectCandidate{scopeAnchorMatch(scopeTeamAnchor())}, Committed: []SubjectRef{scopeTeamAnchor()}},
			cohort:     kindCohort(SubjectRepository, 2), status: InvestigationComplete,
			wantDecision: CountPopulationScopeAnchorCommitted, wantCounted: true, wantServed: 2, wantAnchors: 1, wantAnchorID: "team:SCOPE_ANCHOR",
			wantSentence: "Counted 2 repositorys.", wantAssembled: contractsv1.ContextFabricRequirementSatisfied,
			wantSubjectKind: SubjectTeam, wantSubjectID: "team:SCOPE_ANCHOR",
		},
		{
			name: "scoped project count, anchor unresolved", frame: countingFrame(SubjectProject), family: QuestionFamilyScopedCohortStatus,
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
			cohort:     kindCohort(SubjectProject, 4), status: InvestigationPartial,
			wantDecision: CountPopulationScopeAnchorUnresolved, wantAssembled: contractsv1.ContextFabricRequirementUnavailable,
		},
		{
			// A committed subject of another identity, with nothing in the
			// resolution tying it to the anchor, is not the anchor.
			name: "scoped count, unrelated committed subject", frame: countingFrame(SubjectTeam), family: QuestionFamilyScopedCohortStatus,
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{SubjectRef{Kind: SubjectRepository, CanonicalID: "repository:OTHER", Label: "other"}}},
			cohort:     kindCohort(SubjectTeam, 3), status: InvestigationComplete,
			wantDecision: CountPopulationScopeAnchorUnresolved, wantUnbound: 1, wantAssembled: contractsv1.ContextFabricRequirementUnavailable,
		},
		{
			name: "scoped count, committed subject matched a different term", frame: countingFrame(SubjectTeam), family: QuestionFamilyScopedCohortStatus,
			resolution: SubjectResolution{Candidates: []SubjectCandidate{scopeAnchorMatch(SubjectRef{Kind: SubjectRepository, CanonicalID: "repository:OTHER", Label: "other"}, "b", "[full question]")}, Committed: []SubjectRef{SubjectRef{Kind: SubjectRepository, CanonicalID: "repository:OTHER", Label: "other"}}},
			cohort:     kindCohort(SubjectTeam, 3), status: InvestigationComplete,
			wantDecision: CountPopulationScopeAnchorUnresolved, wantUnbound: 1, wantAssembled: contractsv1.ContextFabricRequirementUnavailable,
		},
		{
			name: "scoped count, anchor term matched but the reading's anchor kind differs", frame: countingFrame(SubjectTeam), family: QuestionFamilyScopedCohortStatus,
			anchorKind: SubjectProject,
			resolution: SubjectResolution{Candidates: []SubjectCandidate{scopeAnchorMatch(scopeAnchorRepository())}, Committed: []SubjectRef{scopeAnchorRepository()}},
			cohort:     kindCohort(SubjectTeam, 3), status: InvestigationComplete,
			wantDecision: CountPopulationScopeAnchorUnresolved, wantUnbound: 1, wantAssembled: contractsv1.ContextFabricRequirementUnavailable,
		},
		{
			name: "scoped count, anchor term matched under the reading's anchor kind, normalized", frame: countingFrame(SubjectTeam), family: QuestionFamilyScopedCohortStatus,
			anchorKind: SubjectRepository,
			resolution: SubjectResolution{Candidates: []SubjectCandidate{scopeAnchorMatch(scopeAnchorRepository(), "A")}, Committed: []SubjectRef{scopeAnchorRepository()}},
			cohort:     kindCohort(SubjectTeam, 3), status: InvestigationComplete,
			wantDecision: CountPopulationScopeAnchorCommitted, wantCounted: true, wantServed: 3, wantAnchors: 1, wantAnchorID: "repository:SCOPE_ANCHOR",
			wantSentence: "Counted 3 teams.", wantAssembled: contractsv1.ContextFabricRequirementSatisfied,
			wantSubjectKind: SubjectRepository, wantSubjectID: "repository:SCOPE_ANCHOR",
		},
		{
			name: "scoped count, anchor committed on the caller's canonical id", frame: countingFrame(SubjectTeam), family: QuestionFamilyScopedCohortStatus,
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{scopeAnchorRepository()}},
			bases:      CommitBasisSet{SubjectMapKey(scopeAnchorRepository()): CommitBasisCallerCanonicalID},
			cohort:     kindCohort(SubjectTeam, 3), status: InvestigationComplete,
			wantDecision: CountPopulationScopeAnchorCommitted, wantCounted: true, wantServed: 3, wantAnchors: 1, wantAnchorID: "repository:SCOPE_ANCHOR",
			wantSentence: "Counted 3 teams.", wantAssembled: contractsv1.ContextFabricRequirementSatisfied,
			wantSubjectKind: SubjectRepository, wantSubjectID: "repository:SCOPE_ANCHOR",
		},
		{
			name: "scoped count, statistical basis without an anchor match", frame: countingFrame(SubjectTeam), family: QuestionFamilyScopedCohortStatus,
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{scopeAnchorRepository()}},
			bases:      CommitBasisSet{SubjectMapKey(scopeAnchorRepository()): CommitBasisStatistical},
			cohort:     kindCohort(SubjectTeam, 3), status: InvestigationComplete,
			wantDecision: CountPopulationScopeAnchorUnresolved, wantUnbound: 1, wantAssembled: contractsv1.ContextFabricRequirementUnavailable,
		},
		{
			// THE FAMILY IS NOT READ. A discovered-kind frame whose turn carried
			// the scoped family counts the organization-level population, even
			// with only a member-kind subject committed.
			name: "discovered frame under a scoped family, member-kind subject committed", frame: frameWithPointer([]InvestigationGoal{GoalCountOrAggregate}, discoveredExpression(SubjectTeam)), family: QuestionFamilyScopedCohortStatus,
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{{Kind: SubjectTeam, CanonicalID: "team:COUNTED_A", Label: "Counted A"}}},
			cohort:     kindCohort(SubjectTeam, 3), status: InvestigationComplete,
			wantDecision: CountPopulationScopeOrganization, wantCounted: true, wantServed: 3,
			wantSentence: "Counted 3 teams.", wantAssembled: contractsv1.ContextFabricRequirementSatisfied,
			wantSubjectKind: SubjectOrganization, wantSubjectID: "org_1",
		},
		{
			// A LEGITIMATE ORGANIZATION COUNT: a discovered kind has no anchor
			// to resolve, so nothing committed is not a scope loss.
			name: "organization-level discovered count, nothing committed", frame: frameWithPointer([]InvestigationGoal{GoalCountOrAggregate}, discoveredExpression(SubjectTeam)), family: QuestionFamilyDiscoveredCohortRanking,
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
			cohort:     kindCohort(SubjectTeam, 5), status: InvestigationComplete,
			wantDecision: CountPopulationScopeOrganization, wantCounted: true, wantServed: 5,
			wantSentence: "Counted 5 teams.", wantAssembled: contractsv1.ContextFabricRequirementSatisfied,
			wantSubjectKind: SubjectOrganization, wantSubjectID: "org_1",
		},
		{
			name: "organization scope count, nothing committed", frame: frameWithPointer([]InvestigationGoal{GoalCountOrAggregate}, orgExpression(&orgTeam)), family: QuestionFamilySubjectInvestigation,
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
			cohort:     kindCohort(SubjectTeam, 2), status: InvestigationComplete,
			wantDecision: CountPopulationScopeOrganization, wantCounted: true, wantServed: 2,
			wantSentence: "Counted 2 teams.", wantAssembled: contractsv1.ContextFabricRequirementSatisfied,
			wantSubjectKind: SubjectOrganization, wantSubjectID: "org_1",
		},
	}
}

// engineCellAnchorCandidates is each engine cell's expected anchor-candidate
// count, written out; a cell absent here expects zero.
var engineCellAnchorCandidates = map[string]int{
	"scoped count, one uncommitted candidate":                                       1,
	"scoped count, anchor ambiguous":                                                2,
	"scoped count, anchor committed":                                                1,
	"scoped count, anchor committed, population larger than served":                 1,
	"scoped count, anchor committed, empty measured population":                     1,
	"scoped count, anchor committed, population unmeasured":                         1,
	"scoped repository count, team anchor committed":                                1,
	"scoped count, committed subject matched a different term":                      1,
	"scoped count, anchor term matched under the reading's anchor kind, normalized": 1,
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
				// THE CLAIM'S SUBJECT IS THE COUNTED POPULATION: the resolved
				// anchor under anchor_committed, the organization under
				// organization_scope -- never a constant, and never the
				// other decision's subject.
				if claim != nil && (claim.Subject.Kind != cell.wantSubjectKind || claim.Subject.CanonicalID != cell.wantSubjectID) {
					t.Errorf("claim subject = %s/%s, want %s/%s", claim.Subject.Kind, claim.Subject.CanonicalID, cell.wantSubjectKind, cell.wantSubjectID)
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
			if cell.wantCounted && (event.SubjectKind != cell.wantSubjectKind || event.SubjectID != cell.wantSubjectID) {
				t.Errorf("event subject = %s/%s, want %s/%s", event.SubjectKind, event.SubjectID, cell.wantSubjectKind, cell.wantSubjectID)
			}
			if !cell.wantCounted && (event.SubjectKind != "" || event.SubjectID != "") {
				t.Errorf("event subject = %s/%s, want empty -- no claim was minted", event.SubjectKind, event.SubjectID)
			}
			if event.Counted != cell.wantCounted || event.Served != rows[0].Served || event.Assembly != rows[0].Outcome {
				t.Errorf("event post-decision = counted %t served %d assembled %q; served document says counted %t served %d assembled %q",
					event.Counted, event.Served, event.Assembly, cell.wantCounted, rows[0].Served, rows[0].Outcome)
			}
			if got, want := event.Scope.AnchorCandidates, engineCellAnchorCandidates[cell.name]; got != want {
				t.Errorf("event anchor_candidates = %d, want %d", got, want)
			}
			if event.Scope.CommittedUnbound != cell.wantUnbound || event.Scope.AnchorID != cell.wantAnchorID || event.Scope.AnchorKind != cell.anchorKind {
				t.Errorf("event unbound=%d anchor_id=%q anchor_kind=%q, want %d/%q/%q", event.Scope.CommittedUnbound, event.Scope.AnchorID, event.Scope.AnchorKind, cell.wantUnbound, cell.wantAnchorID, cell.anchorKind)
			}
			if event.Scope.Committed != len(cell.resolution.Committed) || event.Scope.CommittedAnchors != cell.wantAnchors || event.Scope.Candidates != len(cell.resolution.Candidates) {
				t.Errorf("event measured committed=%d anchors=%d candidates=%d, want %d/%d/%d",
					event.Scope.Committed, event.Scope.CommittedAnchors, event.Scope.Candidates,
					len(cell.resolution.Committed), cell.wantAnchors, len(cell.resolution.Candidates))
			}
			if event.MemberSetResolved != (cell.cohort != nil) || (cell.cohort != nil && event.Members != len(cell.cohort.Members)) {
				t.Errorf("event member set resolved=%t members=%d, fixture cohort %+v", event.MemberSetResolved, event.Members, cell.cohort)
			}
			if event.Scope.ExpressionKind != cell.frame.SubjectExpression.Kind || event.Reused {
				t.Errorf("event expression=%q reused=%t, want %q/false", event.Scope.ExpressionKind, event.Reused, cell.frame.SubjectExpression.Kind)
			}
		})
	}
}

// TestDecideCountPopulationScopeCoversItsInputDomain enumerates the decision's
// input domain with every expectation written out: frame absent, all six
// expression kinds, and resolution shapes covering identity, provenance, kind
// and candidate boundaries.
func TestDecideCountPopulationScopeCoversItsInputDomain(t *testing.T) {
	t.Parallel()
	member := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:M", Label: "m"}
	anchor := scopeAnchorRepository()
	other := SubjectRef{Kind: SubjectRepository, CanonicalID: "repository:OTHER", Label: "other"}
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project:P", Label: "p"}
	offered := scopeCandidate(anchor, "receipt_scope_01")
	rows := []struct {
		name       string
		anchorKind SubjectKind
		resolution SubjectResolution
		bases      CommitBasisSet
		// scoped is the children_of_scope expectation; every other
		// expression is organization_scope over the same measurements.
		scoped   CountPopulationScopeDecision
		anchors  int
		unbound  int
		anchorID string
	}{
		{"empty", "", SubjectResolution{}, nil, CountPopulationScopeAnchorUnresolved, 0, 0, ""},
		{"one candidate", "", SubjectResolution{Candidates: []SubjectCandidate{offered}}, nil, CountPopulationScopeAnchorUnresolved, 0, 0, ""},
		{"two candidates", "", SubjectResolution{Candidates: []SubjectCandidate{offered, offered}}, nil, CountPopulationScopeAnchorAmbiguous, 0, 0, ""},
		{"member committed", "", SubjectResolution{Committed: []SubjectRef{member}}, nil, CountPopulationScopeAnchorUnresolved, 0, 0, ""},
		{"member committed with anchor match", "", SubjectResolution{Committed: []SubjectRef{member}, Candidates: []SubjectCandidate{scopeAnchorMatch(member)}}, nil, CountPopulationScopeAnchorUnresolved, 0, 0, ""},
		{"unbound non-member committed", "", SubjectResolution{Committed: []SubjectRef{other}}, nil, CountPopulationScopeAnchorUnresolved, 0, 1, ""},
		{"unbound non-member committed, two candidates", "", SubjectResolution{Committed: []SubjectRef{other}, Candidates: []SubjectCandidate{offered, offered}}, nil, CountPopulationScopeAnchorAmbiguous, 0, 1, ""},
		{"anchor match on another subject", "", SubjectResolution{Committed: []SubjectRef{other}, Candidates: []SubjectCandidate{scopeAnchorMatch(anchor)}}, nil, CountPopulationScopeAnchorUnresolved, 0, 1, ""},
		{"same id other kind matched", "", SubjectResolution{Committed: []SubjectRef{anchor}, Candidates: []SubjectCandidate{scopeAnchorMatch(SubjectRef{Kind: SubjectProject, CanonicalID: anchor.CanonicalID, Label: "x"})}}, nil, CountPopulationScopeAnchorUnresolved, 0, 1, ""},
		{"anchor matched", "", SubjectResolution{Committed: []SubjectRef{anchor}, Candidates: []SubjectCandidate{scopeAnchorMatch(anchor)}}, nil, CountPopulationScopeAnchorCommitted, 1, 0, anchor.CanonicalID},
		{"anchor matched after normalization", "", SubjectResolution{Committed: []SubjectRef{anchor}, Candidates: []SubjectCandidate{scopeAnchorMatch(anchor, "  A")}}, nil, CountPopulationScopeAnchorCommitted, 1, 0, anchor.CanonicalID},
		{"anchor matched only the question marker", "", SubjectResolution{Committed: []SubjectRef{anchor}, Candidates: []SubjectCandidate{scopeAnchorMatch(anchor, "[full question]")}}, nil, CountPopulationScopeAnchorUnresolved, 0, 1, ""},
		{"anchor matched an empty term", "", SubjectResolution{Committed: []SubjectRef{anchor}, Candidates: []SubjectCandidate{scopeAnchorMatch(anchor, "", " ")}}, nil, CountPopulationScopeAnchorUnresolved, 0, 1, ""},
		{"anchor matched, reading kind agrees", SubjectRepository, SubjectResolution{Committed: []SubjectRef{anchor}, Candidates: []SubjectCandidate{scopeAnchorMatch(anchor)}}, nil, CountPopulationScopeAnchorCommitted, 1, 0, anchor.CanonicalID},
		{"anchor matched, reading kind differs", SubjectProject, SubjectResolution{Committed: []SubjectRef{anchor}, Candidates: []SubjectCandidate{scopeAnchorMatch(anchor)}}, nil, CountPopulationScopeAnchorUnresolved, 0, 1, ""},
		{"reading kind equal to member kind is dropped", SubjectTeam, SubjectResolution{Committed: []SubjectRef{anchor}, Candidates: []SubjectCandidate{scopeAnchorMatch(anchor)}}, nil, CountPopulationScopeAnchorCommitted, 1, 0, anchor.CanonicalID},
		{"reading kind out of vocabulary is dropped", SubjectKind("not_a_kind"), SubjectResolution{Committed: []SubjectRef{anchor}, Candidates: []SubjectCandidate{scopeAnchorMatch(anchor)}}, nil, CountPopulationScopeAnchorCommitted, 1, 0, anchor.CanonicalID},
		{"caller canonical id basis", "", SubjectResolution{Committed: []SubjectRef{anchor}}, CommitBasisSet{SubjectMapKey(anchor): CommitBasisCallerCanonicalID}, CountPopulationScopeAnchorCommitted, 1, 0, anchor.CanonicalID},
		{"caller canonical id basis, reading kind differs", SubjectProject, SubjectResolution{Committed: []SubjectRef{anchor}}, CommitBasisSet{SubjectMapKey(anchor): CommitBasisCallerCanonicalID}, CountPopulationScopeAnchorUnresolved, 0, 1, ""},
		{"authoritative identity basis alone", "", SubjectResolution{Committed: []SubjectRef{anchor}}, CommitBasisSet{SubjectMapKey(anchor): CommitBasisAuthoritativeIdentity}, CountPopulationScopeAnchorUnresolved, 0, 1, ""},
		{"statistical basis alone", "", SubjectResolution{Committed: []SubjectRef{anchor}}, CommitBasisSet{SubjectMapKey(anchor): CommitBasisStatistical}, CountPopulationScopeAnchorUnresolved, 0, 1, ""},
		{"bound anchor beside an unbound subject", "", SubjectResolution{Committed: []SubjectRef{other, anchor, member}, Candidates: []SubjectCandidate{scopeAnchorMatch(anchor)}}, nil, CountPopulationScopeAnchorCommitted, 1, 1, anchor.CanonicalID},
		{"two bound anchors, first id reported", "", SubjectResolution{Committed: []SubjectRef{project, anchor}, Candidates: []SubjectCandidate{scopeAnchorMatch(anchor), scopeAnchorMatch(project)}}, nil, CountPopulationScopeAnchorCommitted, 2, 0, project.CanonicalID},
		{"two member candidates", "", SubjectResolution{Candidates: []SubjectCandidate{scopeCandidate(member, "receipt_m1"), scopeCandidate(member, "receipt_m2")}}, nil, CountPopulationScopeAnchorUnresolved, 0, 0, ""},
		{"one anchor candidate beside a member candidate", "", SubjectResolution{Candidates: []SubjectCandidate{offered, scopeCandidate(member, "receipt_m1")}}, nil, CountPopulationScopeAnchorUnresolved, 0, 0, ""},
		{"two candidates of a kind the reading excludes", SubjectProject, SubjectResolution{Candidates: []SubjectCandidate{offered, offered}}, nil, CountPopulationScopeAnchorUnresolved, 0, 0, ""},
		{"two candidates of the reading's anchor kind", SubjectRepository, SubjectResolution{Candidates: []SubjectCandidate{offered, offered}}, nil, CountPopulationScopeAnchorAmbiguous, 0, 0, ""},
	}
	for _, row := range rows {
		got := DecideCountPopulationScope(nil, row.anchorKind, row.resolution, row.bases)
		if got.Decision != CountPopulationScopeFrameAbsent || got.Counts() || got.CommittedAnchors != 0 || got.AnchorID != "" {
			t.Errorf("frame absent / %s: %+v, want frame_absent with nothing bound", row.name, got)
		}
	}
	team := SubjectTeam
	expressions := []SubjectExpression{
		namedExpression(SubjectTeam),
		{Kind: SubjectExpressionExplicitSet, Explicit: &ExplicitSetExpression{}},
		discoveredExpression(SubjectTeam),
		scopedExpression(SubjectTeam),
		groupedExpression(SubjectTeam, SubjectProject),
		orgExpression(&team),
	}
	for _, expression := range expressions {
		frame := frameWithPointer([]InvestigationGoal{GoalCountOrAggregate}, expression)
		wantMember, _ := expression.MemberKind()
		scoped := expression.Kind == SubjectExpressionChildrenOfScope
		for _, row := range rows {
			got := DecideCountPopulationScope(frame, row.anchorKind, row.resolution, row.bases)
			want := CountPopulationScopeOrganization
			wantAnchors, wantUnbound, wantID := 0, 0, ""
			if scoped {
				want, wantAnchors, wantUnbound, wantID = row.scoped, row.anchors, row.unbound, row.anchorID
			}
			if got.Decision != want {
				t.Errorf("%s / %s: decision %q, want %q", expression.Kind, row.name, got.Decision, want)
			}
			if scoped && got.AnchorCandidates != domainRowAnchorCandidates[row.name] {
				t.Errorf("%s / %s: anchor_candidates=%d, want %d", expression.Kind, row.name, got.AnchorCandidates, domainRowAnchorCandidates[row.name])
			}
			if scoped && (got.CommittedAnchors != wantAnchors || got.CommittedUnbound != wantUnbound || got.AnchorID != wantID) {
				t.Errorf("%s / %s: anchors=%d unbound=%d id=%q, want %d/%d/%q", expression.Kind, row.name, got.CommittedAnchors, got.CommittedUnbound, got.AnchorID, wantAnchors, wantUnbound, wantID)
			}
			if got.ExpressionKind != expression.Kind || got.MemberKind != wantMember || got.Committed != len(row.resolution.Committed) || got.Candidates != len(row.resolution.Candidates) {
				t.Errorf("%s / %s: measured %+v", expression.Kind, row.name, got)
			}
			if got.Counts() != (want == CountPopulationScopeOrganization || want == CountPopulationScopeAnchorCommitted) {
				t.Errorf("%s / %s: counts %t for decision %q", expression.Kind, row.name, got.Counts(), got.Decision)
			}
		}
	}
}

// domainRowAnchorCandidates is each domain row's expected anchor-candidate
// count under the scoped expression (member kind team), written out; a row
// absent here expects zero.
var domainRowAnchorCandidates = map[string]int{
	"one candidate":  1,
	"two candidates": 2,
	"unbound non-member committed, two candidates":   2,
	"anchor match on another subject":                1,
	"same id other kind matched":                     1,
	"anchor matched":                                 1,
	"anchor matched after normalization":             1,
	"anchor matched only the question marker":        1,
	"anchor matched an empty term":                   1,
	"anchor matched, reading kind agrees":            1,
	"reading kind equal to member kind is dropped":   1,
	"reading kind out of vocabulary is dropped":      1,
	"bound anchor beside an unbound subject":         1,
	"two bound anchors, first id reported":           2,
	"one anchor candidate beside a member candidate": 1,
	"two candidates of the reading's anchor kind":    2,
}

// TestNormalizeRetrievalTermIsResolutionsNormalization pins the comparison's
// normalization cell by cell.
func TestNormalizeRetrievalTermIsResolutionsNormalization(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{"": "", " ": "", "a": "a", " A ": "a", "Dev-Health-ACR": "dev-health-acr", "\tMixed Case\n": "mixed case", "[full question]": "[full question]"} {
		if got := NormalizeRetrievalTerm(in); got != want {
			t.Errorf("NormalizeRetrievalTerm(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestStoredCountReadingIsEmptyUnlessAFrameWasRead enumerates the stored
// reading the reuse decision reads its frame and anchor kind from.
func TestStoredCountReadingIsEmptyUnlessAFrameWasRead(t *testing.T) {
	t.Parallel()
	frame := countingFrame(SubjectTeam)
	anchored := SemanticScopeAnchor{Kind: SubjectRepository, Term: "a"}
	for _, status := range []SemanticStateReadStatus{"", SemanticStateReadAvailable, SemanticStateReadAbsent, SemanticStateReadUnsupportedVersion, SemanticStateReadMalformed, SemanticStateReadOversized} {
		for _, cell := range []struct {
			name  string
			state *PersistedSemanticState
			read  bool
		}{
			{"no state", nil, false},
			{"state without a frame", &PersistedSemanticState{FramePresent: false, ScopeAnchor: anchored}, false},
			{"frame flagged absent but set", &PersistedSemanticState{FramePresent: false, Frame: frame, ScopeAnchor: anchored}, false},
			{"frame present", &PersistedSemanticState{FramePresent: true, Frame: frame, ScopeAnchor: anchored}, true},
		} {
			got := storedCountReadingOf(StoredInvestigationResult{SemanticState: cell.state, SemanticStateRead: status})
			want := status == SemanticStateReadAvailable && cell.read
			if want && (got.Frame != frame || got.AnchorKind != SubjectRepository) {
				t.Errorf("read %q / %s: %+v, want the frame and anchor kind", status, cell.name, got)
			}
			if !want && (got.Frame != nil || got.AnchorKind != "") {
				t.Errorf("read %q / %s: %+v, want the zero reading", status, cell.name, got)
			}
		}
	}
}

// TestStoredDocumentStatesCountReadsTheRowAndTheClaim enumerates what a stored
// document can carry of a count.
func TestStoredDocumentStatesCountReadsTheRowAndTheClaim(t *testing.T) {
	t.Parallel()
	requirement := string(ObligationCount) + "/" + string(SubjectRoleMember) + "/" + string(SubjectTeam)
	row := func(stage contractsv1.ContextFabricOutcomeStage, obligation string, outcome contractsv1.ContextFabricPlanRequirementOutcome) RequirementOutcomeRow {
		return RequirementOutcomeRow{Stage: stage, Requirement: requirement, Obligation: obligation, Outcome: outcome}
	}
	claim := ClaimedFact{ClaimID: cardinalityClaimIDPrefix + "team", Kind: contractsv1.ContextFabricFactCardinality}
	for _, cell := range []struct {
		name   string
		rows   []RequirementOutcomeRow
		claims []ClaimedFact
		want   bool
	}{
		{"nothing", nil, nil, false},
		{"planning satisfied only", []RequirementOutcomeRow{row(contractsv1.ContextFabricOutcomeStagePlanning, "count", contractsv1.ContextFabricRequirementSatisfied)}, nil, false},
		{"assembled satisfied", []RequirementOutcomeRow{row(contractsv1.ContextFabricOutcomeStageAssembledResult, "count", contractsv1.ContextFabricRequirementSatisfied)}, nil, true},
		{"assembled narrowed", []RequirementOutcomeRow{row(contractsv1.ContextFabricOutcomeStageAssembledResult, "count", contractsv1.ContextFabricRequirementNarrowed)}, nil, true},
		{"assembled unavailable", []RequirementOutcomeRow{row(contractsv1.ContextFabricOutcomeStageAssembledResult, "count", contractsv1.ContextFabricRequirementUnavailable)}, nil, false},
		{"assembled satisfied of another obligation", []RequirementOutcomeRow{row(contractsv1.ContextFabricOutcomeStageAssembledResult, "state", contractsv1.ContextFabricRequirementSatisfied)}, nil, false},
		{"claim only", nil, []ClaimedFact{claim}, true},
		{"claim beside an unavailable row", []RequirementOutcomeRow{row(contractsv1.ContextFabricOutcomeStageAssembledResult, "count", contractsv1.ContextFabricRequirementUnavailable)}, []ClaimedFact{claim}, true},
		{"a non-cardinality claim", nil, []ClaimedFact{{ClaimID: "claim_other", Kind: FactStatus}}, false},
	} {
		doc := InvestigationResult{ClaimedFacts: cell.claims}
		doc.Completeness.Outcomes = cell.rows
		if got := storedDocumentStatesCount(doc); got != cell.want {
			t.Errorf("%s: states count = %t, want %t", cell.name, got, cell.want)
		}
	}
}

// readingReuseGate serves one stored row with the reading persisted beside it;
// a nil frame serves the row with no readable reading.
type readingReuseGate struct {
	stored     InvestigationResult
	frame      *QuestionFrame
	anchorKind SubjectKind
}

func (g readingReuseGate) FindReusable(context.Context, storage.Principal, ReuseKey) (StoredInvestigationResult, bool, ReuseMissReason, error) {
	if g.frame == nil {
		return StoredInvestigationResult{Result: g.stored, SemanticStateRead: SemanticStateReadAbsent}, true, "", nil
	}
	return StoredInvestigationResult{
		Result:            g.stored,
		SemanticState:     &PersistedSemanticState{FramePresent: true, Frame: g.frame, ScopeAnchor: SemanticScopeAnchor{Kind: g.anchorKind}},
		SemanticStateRead: SemanticStateReadAvailable,
	}, true, "", nil
}

// TestAReusedCountIsHeldToTheSameScopeDecision drives the reuse backfill: a
// stored document that owes a count and carries no count row is judged by the
// same decision, over its stored reading's frame and its stored resolution.
func TestAReusedCountIsHeldToTheSameScopeDecision(t *testing.T) {
	t.Parallel()
	scopedTeams := countingFrame(SubjectTeam)
	for _, cell := range []struct {
		name            string
		frame           *QuestionFrame
		withAnchor      bool
		wantDecision    CountPopulationScopeDecision
		wantCounted     bool
		wantSubjectKind SubjectKind
	}{
		{"anchor committed", scopedTeams, true, CountPopulationScopeAnchorCommitted, true, SubjectProject},
		{"anchor unresolved", scopedTeams, false, CountPopulationScopeAnchorUnresolved, false, ""},
		{"stored reading absent", nil, true, CountPopulationScopeFrameAbsent, false, ""},
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
			committed := []SubjectRef{}
			storedCandidates := []SubjectCandidate{}
			if cell.withAnchor {
				committed = append(committed, project)
				storedCandidates = append(storedCandidates, scopeAnchorMatch(project))
			}
			for _, member := range candidate.Cohort.Members {
				committed = append(committed, member.Subject)
			}
			candidate.SubjectResolution = SubjectResolution{Candidates: storedCandidates, Committed: committed}
			candidate.Completeness = ComputeAnswerCompleteness(candidate)
			telemetry := &recordingTelemetry{}
			engine := mustReuseTestEngine(t, EngineDependencies{
				Graph:     graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: committed}},
				Results:   &resultStoreStub{},
				Telemetry: telemetry,
				ReuseGate: readingReuseGate{stored: candidate, frame: cell.frame},
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
				// THE REUSE BACKFILL NAMES THE SAME ANCHOR THE FRESH PATH
				// WOULD: the stored resolution's committed project, never the
				// organization -- cardinalityClaim is the one authority both
				// paths call through.
				if claim != nil && (claim.Subject.Kind != cell.wantSubjectKind || claim.Subject.CanonicalID != project.CanonicalID) {
					t.Errorf("reused claim subject = %s/%s, want %s/%s", claim.Subject.Kind, claim.Subject.CanonicalID, cell.wantSubjectKind, project.CanonicalID)
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

// TestAnAnswerThatOwesNoCountEmitsNoScopeDecision pins the line's trigger: the
// same scoped member set, asked a question with no count obligation, produces
// no count surface and no decision line.
func TestAnAnswerThatOwesNoCountEmitsNoScopeDecision(t *testing.T) {
	t.Parallel()
	cell := scopeCell{
		frame: nonCountingFrame(SubjectTeam), family: QuestionFamilyScopedCohortStatus,
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
		cohort:     kindCohort(SubjectTeam, 3), status: InvestigationComplete,
	}
	telemetry := &recordingTelemetry{}
	result := runScopeCell(t, context.Background(), newScopeEngine(t, cell, telemetry))
	if requirement, _ := countRequirement(result.Completeness.Outcomes); requirement != "" {
		t.Fatalf("fixture control: the non-counting frame seeded count requirement %q", requirement)
	}
	if result.Cohort == nil || len(result.Cohort.Members) != 3 {
		t.Fatalf("fixture control: served cohort %+v, want the 3-member set", result.Cohort)
	}
	if got := len(telemetry.countPopulationScopes); got != 0 {
		t.Errorf("count population scope events = %d for an answer that owes no count, want 0", got)
	}
}

// storedScopedCountDoc is a stored answer to a scoped team count over a
// three-team member set. withAnchor commits the project as resolution matched
// it; withSurfaces carries an assembled satisfied count row, the cardinality
// claim and the sentence, the way a document stored by an earlier build does.
func storedScopedCountDoc(withAnchor, withSurfaces bool) (InvestigationResult, []SubjectRef) {
	project, candidate := reusableCandidate()
	candidate.Cohort = countingCohort(SubjectTeam, 3)
	requirement := string(ObligationCount) + "/" + string(SubjectRoleMember) + "/" + string(SubjectTeam)
	candidate.Completeness.Outcomes = []RequirementOutcomeRow{{
		Stage: contractsv1.ContextFabricOutcomeStagePlanning, Requirement: requirement, Obligation: string(ObligationCount),
		Outcome: contractsv1.ContextFabricRequirementSatisfied, Impact: contractsv1.ContextFabricAnswerImpactNone,
	}}
	org := SubjectRef{Kind: SubjectOrganization, CanonicalID: reusePrincipal().OrgID, Label: reusePrincipal().OrgID}
	if withSurfaces {
		served := int64(3)
		candidate.Completeness.Outcomes = append(candidate.Completeness.Outcomes, RequirementOutcomeRow{
			Stage: contractsv1.ContextFabricOutcomeStageAssembledResult, Requirement: requirement, Obligation: string(ObligationCount),
			Outcome: contractsv1.ContextFabricRequirementSatisfied, Impact: contractsv1.ContextFabricAnswerImpactNone, Served: 3, Declared: 3,
		})
		candidate.ClaimedFacts = append(candidate.ClaimedFacts, ClaimedFact{
			ClaimID: cardinalityClaimIDPrefix + "team", Kind: contractsv1.ContextFabricFactCardinality,
			Subject: org, Field: "team_count", Value: ScalarValue{Integer: &served},
		})
		candidate.DeterministicAnswer = strings.TrimSpace(candidate.DeterministicAnswer + " Counted 3 teams.")
	}
	committed := []SubjectRef{}
	candidates := []SubjectCandidate{}
	if withAnchor {
		committed = append(committed, project)
		candidates = append(candidates, scopeAnchorMatch(project))
	}
	for _, member := range candidate.Cohort.Members {
		committed = append(committed, member.Subject)
	}
	candidate.SubjectResolution = SubjectResolution{Candidates: candidates, Committed: committed}
	candidate.Completeness = ComputeAnswerCompleteness(candidate)
	return candidate, append([]SubjectRef{project, org}, committed...)
}

// TestEveryCountSurfaceOnEveryPathFollowsTheScopeDecision is the surface x
// path matrix: the row, the claim and the sentence, on the fresh path, a
// reuse hit and a reuse miss, each derived from the one decision.
func TestEveryCountSurfaceOnEveryPathFollowsTheScopeDecision(t *testing.T) {
	t.Parallel()
	type want struct {
		reused  bool
		outcome AnswerReuseOutcome
		// row is the served assembled count row's outcome; "" means the
		// served document carries none.
		row      contractsv1.ContextFabricPlanRequirementOutcome
		claim    bool
		sentence bool
		lines    int
	}
	for _, cell := range []struct {
		name string
		run  func(t *testing.T, telemetry *recordingTelemetry) InvestigationResult
		want want
	}{
		{"fresh, unrelated committed subject", func(t *testing.T, telemetry *recordingTelemetry) InvestigationResult {
			other := SubjectRef{Kind: SubjectRepository, CanonicalID: "repository:OTHER", Label: "other"}
			cell := scopeCell{frame: countingFrame(SubjectTeam), family: QuestionFamilyScopedCohortStatus,
				resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{other}},
				cohort:     kindCohort(SubjectTeam, 3), status: InvestigationComplete}
			return runScopeCell(t, context.Background(), newScopeEngine(t, cell, telemetry))
		}, want{row: contractsv1.ContextFabricRequirementUnavailable, lines: 1}},
		{"fresh, bound anchor", func(t *testing.T, telemetry *recordingTelemetry) InvestigationResult {
			cell := scopeCell{frame: countingFrame(SubjectTeam), family: QuestionFamilyScopedCohortStatus,
				resolution: SubjectResolution{Candidates: []SubjectCandidate{scopeAnchorMatch(scopeAnchorRepository())}, Committed: []SubjectRef{scopeAnchorRepository()}},
				cohort:     kindCohort(SubjectTeam, 3), status: InvestigationComplete}
			return runScopeCell(t, context.Background(), newScopeEngine(t, cell, telemetry))
		}, want{row: contractsv1.ContextFabricRequirementSatisfied, claim: true, sentence: true, lines: 1}},
		{"reuse hit, stored count over a bound anchor", func(t *testing.T, telemetry *recordingTelemetry) InvestigationResult {
			stored, recheck := storedScopedCountDoc(true, true)
			engine := mustReuseTestEngine(t, EngineDependencies{
				Graph: graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: recheck}}, Results: &resultStoreStub{}, Telemetry: telemetry,
				ReuseGate: readingReuseGate{stored: stored, frame: countingFrame(SubjectTeam)},
			})
			result, err := engine.Investigate(context.Background(), reusePrincipal(), validInvestigationRequest())
			if err != nil {
				t.Fatalf("Investigate() error = %v", err)
			}
			return result
		}, want{reused: true, outcome: AnswerReuseHit, row: contractsv1.ContextFabricRequirementSatisfied, claim: true, sentence: true, lines: 1}},
		{"reuse hit, no stored count and no anchor: backfilled as withheld", func(t *testing.T, telemetry *recordingTelemetry) InvestigationResult {
			stored, recheck := storedScopedCountDoc(false, false)
			engine := mustReuseTestEngine(t, EngineDependencies{
				Graph: graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: recheck}}, Results: &resultStoreStub{}, Telemetry: telemetry,
				ReuseGate: readingReuseGate{stored: stored, frame: countingFrame(SubjectTeam)},
			})
			result, err := engine.Investigate(context.Background(), reusePrincipal(), validInvestigationRequest())
			if err != nil {
				t.Fatalf("Investigate() error = %v", err)
			}
			return result
		}, want{reused: true, outcome: AnswerReuseHit, row: contractsv1.ContextFabricRequirementUnavailable, lines: 1}},
		{"reuse miss, stored count contradicts the decision", func(t *testing.T, telemetry *recordingTelemetry) InvestigationResult {
			stored, recheck := storedScopedCountDoc(false, true)
			fresh := scopeCell{frame: countingFrame(SubjectTeam), family: QuestionFamilyScopedCohortStatus,
				resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: recheck[2:]},
				cohort:     kindCohort(SubjectTeam, 3), status: InvestigationComplete}
			// The request the stored row was keyed under (no explicit window),
			// so the lookup reaches the stored row. The fresh path it falls to
			// stops at its own window confirmation and states no count.
			result, err := newScopeEngine(t, fresh, telemetry, readingReuseGate{stored: stored, frame: countingFrame(SubjectTeam)}).Investigate(context.Background(), reusePrincipal(), validInvestigationRequest())
			if err != nil {
				t.Fatalf("Investigate() error = %v", err)
			}
			return result
		}, want{outcome: AnswerReuseMissCountScope}},
	} {
		cell := cell
		t.Run(cell.name, func(t *testing.T) {
			t.Parallel()
			telemetry := &recordingTelemetry{}
			result := cell.run(t, telemetry)
			if result.Reused != cell.want.reused {
				t.Fatalf("reused = %t, want %t (reuse outcomes %v)", result.Reused, cell.want.reused, telemetry.answerReuseOutcomes)
			}
			if cell.want.outcome != "" && (len(telemetry.answerReuseOutcomes) == 0 || telemetry.answerReuseOutcomes[0] != cell.want.outcome) {
				t.Errorf("reuse outcomes = %v, want first %q", telemetry.answerReuseOutcomes, cell.want.outcome)
			}
			rows := countOutcomeRows(result, contractsv1.ContextFabricOutcomeStageAssembledResult)
			if cell.want.row == "" && len(rows) != 0 {
				t.Errorf("assembled count rows = %+v, want none -- the stored row must not be served", rows)
			}
			if cell.want.row != "" && (len(rows) != 1 || rows[0].Outcome != cell.want.row) {
				t.Errorf("assembled count rows = %+v, want one %q row", rows, cell.want.row)
			}
			if got := cardinalityClaimOf(result) != nil; got != cell.want.claim {
				t.Errorf("claim present = %t, want %t", got, cell.want.claim)
			}
			if got := strings.Contains(result.DeterministicAnswer, "Counted 3 teams."); got != cell.want.sentence {
				t.Errorf("sentence present = %t, want %t (answer %q)", got, cell.want.sentence, result.DeterministicAnswer)
			}
			if len(telemetry.countPopulationScopes) != cell.want.lines {
				t.Errorf("count population scope lines = %d, want %d on this path", len(telemetry.countPopulationScopes), cell.want.lines)
			} else if cell.want.lines == 1 {
				event := telemetry.countPopulationScopes[0]
				if event.Counted != (cell.want.row != contractsv1.ContextFabricRequirementUnavailable) || event.Reused != cell.want.reused {
					t.Errorf("scope line counted=%t reused=%t, want counted=%t reused=%t", event.Counted, event.Reused, cell.want.row != contractsv1.ContextFabricRequirementUnavailable, cell.want.reused)
				}
			}
		})
	}
}

// TestAnEmptyAnchorTermBindsNothing pins the empty-term guard over its input
// domain: an empty or whitespace anchor term on the frame never binds a
// candidate, whatever resolution recorded as matched. Only a real anchor term
// binds.
func TestAnEmptyAnchorTermBindsNothing(t *testing.T) {
	t.Parallel()
	anchor := scopeAnchorRepository()
	for _, cell := range []struct {
		name        string
		anchorTerms []string
		matched     []string
		wantBound   bool
	}{
		{"empty anchor term, empty match", []string{""}, []string{""}, false},
		{"empty anchor term, whitespace match", []string{""}, []string{" "}, false},
		{"whitespace anchor term, empty match", []string{" "}, []string{""}, false},
		{"whitespace anchor term, whitespace match", []string{" "}, []string{"  "}, false},
		{"empty and whitespace anchor terms, empty match", []string{"", " "}, []string{""}, false},
		{"whitespace beside a real anchor term, empty match", []string{" ", "a"}, []string{""}, false},
		{"whitespace beside a real anchor term, whitespace match", []string{" ", "a"}, []string{" "}, false},
		{"whitespace beside a real anchor term, real match", []string{" ", "a"}, []string{"a"}, true},
		{"empty anchor term beside a real one, real match", []string{"", "a"}, []string{"a"}, true},
	} {
		frame := frameWithPointer([]InvestigationGoal{GoalCountOrAggregate}, SubjectExpression{
			Kind:   SubjectExpressionChildrenOfScope,
			Scoped: &ScopedSetExpression{AnchorTerms: cell.anchorTerms, MemberKind: SubjectTeam},
		})
		resolution := SubjectResolution{Committed: []SubjectRef{anchor}, Candidates: []SubjectCandidate{scopeAnchorMatch(anchor, cell.matched...)}}
		got := DecideCountPopulationScope(frame, "", resolution, nil)
		if bound := got.Decision == CountPopulationScopeAnchorCommitted; bound != cell.wantBound {
			t.Errorf("%s: decision %q (anchors=%d unbound=%d), want bound=%t", cell.name, got.Decision, got.CommittedAnchors, got.CommittedUnbound, cell.wantBound)
		}
	}
}
