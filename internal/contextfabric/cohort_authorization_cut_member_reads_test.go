package contextfabric

import (
	"context"
	"reflect"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12153: a STATUS question over a cohort that authorization cut.
//
// THE CASE. Since CHAOS-12147 the COUNT row of such a cohort is narrowed. The
// MEMBER READ rows were not: the read-population layer built the each_member
// population from the cohort's own Complete/Truncated, and the cohort can say
// complete (its census was complete, the cut came after). So a status question
// over 4 authorized teams of 34 served status `partial` (the status floor read
// the cut) and completeness `complete` (the member read row read satisfied
// 4/4). Red on acr 1818784e: completeness.state complete, row satisfied 4/4.

const memberReadsRequirement = "state/member/team"

// runAuthorizationCutCohortQuestion drives Engine.Investigate end to end over
// a scoped team cohort of cohortSize, with the given graph coverage and goals.
// TWO kinds serve `state` for a team, so the row is corroborated and the
// population arm reads per member. The synthesizer merges graph and fact
// coverage as the production one does (model_runtime.go, MergeCoverage).
func runAuthorizationCutCohortQuestion(t *testing.T, cohortSize int, graphCoverage Coverage, goals []InvestigationGoal) InvestigationResult {
	t.Helper()
	return runAuthorizationCutCohortQuestionReading(t, cohortSize, graphCoverage, goals, cohortSize)
}

// runAuthorizationCutCohortQuestionReading is runAuthorizationCutCohortQuestion
// whose fact reader returns facts for the first readMembers members only.
func runAuthorizationCutCohortQuestionReading(t *testing.T, cohortSize int, graphCoverage Coverage, goals []InvestigationGoal, readMembers int) InvestigationResult {
	t.Helper()
	frame := frameWithPointer(goals, scopedExpression(SubjectTeam))
	anchor := SubjectRef{Kind: SubjectOrganization, CanonicalID: "org_1", Label: "Org"}
	cohort := countingCohort(SubjectTeam, cohortSize)
	stateReader := func(kind FactKind) FactCapability {
		return FactCapability{
			Kind:                  kind,
			SupportedSubjectKinds: []SubjectKind{SubjectTeam},
			Obligations:           map[SubjectKind][]AnswerObligation{SubjectTeam: {ObligationState}},
		}
	}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: familyInterpreter{
			interpreted: InterpretedQuestion{
				Shape: ShapeDiscoveredCohort, RequestedJudgment: "status",
				TimeContext:      TimeContext{Axis: TemporalCurrent},
				FactRequirements: []FactRequirement{{Kind: FactHealth}, {Kind: FactFlow}},
			},
			outcome: QuestionFamilyOutcome{
				Frame: frame, FrameObligations: frame.Obligations,
				Family: QuestionFamilyScopedCohortStatus, Source: QuestionFamilySourceModel,
			},
		},
		Graph: graphReaderStub{
			resolution: SubjectResolution{Candidates: []SubjectCandidate{scopeAnchorMatch(anchor)}, Committed: []SubjectRef{anchor}},
			bases:      provenCommitBases(anchor),
			context: GraphContext{
				Cohort: cohort, Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{},
				FactRequirements: []FactRequirement{}, EvidenceRefIDs: []string{},
				Coverage: graphCoverage,
			},
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
			for _, member := range cohort.Members[:readMembers] {
				bundle.Facts = append(bundle.Facts,
					CanonicalFact{Kind: FactHealth, Subject: member.Subject, SourceState: SourceAvailable},
					CanonicalFact{Kind: FactFlow, Subject: member.Subject, SourceState: SourceAvailable})
			}
			return bundle, nil
		}),
		Synthesizer: synthesizerFunc(func(_ context.Context, _ storage.Principal, input SynthesisInput) (InvestigationResult, error) {
			return InvestigationResult{
				Status: InvestigationComplete, DirectJudgment: "Fine.", CurrentState: "Nominal.",
				StrongestPressures: []string{}, Drivers: []DriverJudgment{}, RemainingWork: []Finding{},
				ReadinessGaps: []Finding{}, Paths: []RelationshipPath{}, Conflicts: []Finding{},
				Limitations: []string{}, EvidenceRefIDs: []string{}, ClaimedFacts: []ClaimedFact{},
				Coverage:            MergeCoverage("org_1", input.Graph.Coverage, input.Facts.Coverage),
				DeterministicAnswer: "Fine.", Warnings: []string{},
				Versions: VersionSet{
					Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
					InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1",
				},
			}, nil
		}),
		Results:      &resultStoreStub{},
		Telemetry:    &recordingTelemetry{},
		Requirements: registryDeriver{capabilities: []FactCapability{stateReader(FactHealth), stateReader(FactFlow)}},
	}, EngineOptions{
		ServiceVersion: "acr-test",
		Now:            func() time.Time { return time.Unix(500, 0).UTC() },
		NewResultID:    func() string { return "result_12153001" },
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	request := validInvestigationRequestWithConfirmedWindow()
	request.RequestID = "request_12153001"
	request.Question = "status question"
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("served result does not validate: %v", err)
	}
	return result
}

func runAuthorizationCutStatus(t *testing.T, cohortSize int, graphCoverage Coverage) InvestigationResult {
	t.Helper()
	return runAuthorizationCutCohortQuestion(t, cohortSize, graphCoverage, []InvestigationGoal{GoalAssessState})
}

func memberReadRows(result InvestigationResult) []RequirementOutcomeRow {
	var rows []RequirementOutcomeRow
	for _, row := range result.Completeness.Outcomes {
		if row.Requirement == memberReadsRequirement && row.Stage == contractsv1.ContextFabricOutcomeStageAssembledResult {
			rows = append(rows, row)
		}
	}
	return rows
}

func assembledMemberReadRow(t *testing.T, result InvestigationResult) RequirementOutcomeRow {
	t.Helper()
	rows := memberReadRows(result)
	if len(rows) != 1 {
		t.Fatalf("assembled %s rows = %d, want 1 -- the fixture never reached the member read, so it proves nothing", memberReadsRequirement, len(rows))
	}
	return rows[0]
}

func TestAMemberReadOverAnAuthorizationCutCohortIsNotComplete(t *testing.T) {
	t.Parallel()
	result := runAuthorizationCutStatus(t, 4, cohortAuthorizationCutCoverage(cohortAuthorizationCutDenied, true))
	row := assembledMemberReadRow(t, result)

	if got := result.Completeness.State; got != contractsv1.ContextFabricAnswerCompletenessPartial {
		t.Fatalf("completeness.state = %q, want %q -- a member set authorization cut by %d was read as the whole one",
			got, contractsv1.ContextFabricAnswerCompletenessPartial, cohortAuthorizationCutDenied)
	}
	if row.Outcome != contractsv1.ContextFabricRequirementNarrowed || row.Impact != contractsv1.ContextFabricAnswerImpactScope {
		t.Fatalf("member read row = %q/%q, want narrowed/scope", row.Outcome, row.Impact)
	}
	if row.Served != 4 || row.Declared != 4+cohortAuthorizationCutDenied {
		t.Fatalf("member read row served/declared = %d/%d, want 4/%d -- the members read plus the members the graph counted and denied",
			row.Served, row.Declared, 4+cohortAuthorizationCutDenied)
	}
	if row.CauseCoverage != contractsv1.ContextFabricCoverageDetailGraphCohortDeniedByAuthorization || !row.CauseObserved {
		t.Fatalf("member read row cause = %q observed=%v, want %q observed", row.CauseCoverage, row.CauseObserved,
			contractsv1.ContextFabricCoverageDetailGraphCohortDeniedByAuthorization)
	}
	want := []contractsv1.ContextFabricRequirementRefinement{{
		Stage:    contractsv1.ContextFabricOutcomeStageAssembledResult,
		Coverage: contractsv1.ContextFabricCoverageDetailGraphCohortDeniedByAuthorization,
		Before:   4 + cohortAuthorizationCutDenied, After: 4,
	}}
	if !reflect.DeepEqual(row.Refinements, want) {
		t.Fatalf("member read refinements = %+v, want %+v", row.Refinements, want)
	}
	if err := contractsv1.ValidateContextFabricPlanRequirementOutcomeRow(row); err != nil {
		t.Fatalf("member read row does not validate: %v", err)
	}
}

// The control: no denial, the same fixture keeps a satisfied member read and
// a complete answer, so the test above is not partial for another reason.
func TestAMemberReadOverAnUncutCohortStaysComplete(t *testing.T) {
	t.Parallel()
	result := runAuthorizationCutStatus(t, 4, Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}})
	row := assembledMemberReadRow(t, result)
	if row.Outcome != contractsv1.ContextFabricRequirementSatisfied || row.Served != 4 || row.Declared != 4 || len(row.Refinements) != 0 {
		t.Fatalf("uncut member read row = %q %d/%d refinements %d, want satisfied 4/4 and no chain", row.Outcome, row.Served, row.Declared, len(row.Refinements))
	}
	if got := result.Completeness.State; got != contractsv1.ContextFabricAnswerCompletenessComplete {
		t.Fatalf("uncut completeness.state = %q, want %q", got, contractsv1.ContextFabricAnswerCompletenessComplete)
	}
}

// A denial detail that does not degrade, or counts nobody, measured no cut.
func TestAnAuthorizationDetailThatMeasuredNoCutLeavesTheMemberReadsAlone(t *testing.T) {
	t.Parallel()
	for name, coverage := range map[string]Coverage{
		"not degrading": cohortAuthorizationCutCoverage(cohortAuthorizationCutDenied, false),
		"zero denied":   cohortAuthorizationCutCoverage(0, true),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			row := assembledMemberReadRow(t, runAuthorizationCutStatus(t, 4, coverage))
			if row.Outcome != contractsv1.ContextFabricRequirementSatisfied || row.Declared != 4 {
				t.Fatalf("member read row = %q %d/%d, want satisfied 4/4", row.Outcome, row.Served, row.Declared)
			}
		})
	}
}

// One answer, one cut: a question that both counts and reads the members
// states the SAME denied number on its count row and on its member read row,
// because both read MembershipCardinality.AuthorizationDenied.
func TestTheMemberReadAndTheCountOfOneAnswerStateTheSameCut(t *testing.T) {
	t.Parallel()
	result := runAuthorizationCutCohortQuestion(t, 4, cohortAuthorizationCutCoverage(cohortAuthorizationCutDenied, true),
		[]InvestigationGoal{GoalAssessState, GoalCountOrAggregate})
	member := assembledMemberReadRow(t, result)
	count := assembledCountRow(t, result)
	deniedOf := func(name string, row RequirementOutcomeRow) int {
		t.Helper()
		for _, refinement := range row.Refinements {
			if refinement.Coverage == contractsv1.ContextFabricCoverageDetailGraphCohortDeniedByAuthorization {
				return refinement.Before - refinement.After
			}
		}
		if row.CauseCoverage == contractsv1.ContextFabricCoverageDetailGraphCohortDeniedByAuthorization {
			return row.Declared - row.Served
		}
		t.Fatalf("%s row names no authorization cut: %+v", name, row)
		return 0
	}
	if m, c := deniedOf("member read", member), deniedOf("count", count); m != c || m != cohortAuthorizationCutDenied {
		t.Fatalf("denied: member read %d, count %d, want both %d (the coverage detail's count)", m, c, cohortAuthorizationCutDenied)
	}
	if member.Declared != count.Declared || member.Served != count.Served {
		t.Fatalf("member read %d/%d and count %d/%d disagree about one member set", member.Served, member.Declared, count.Served, count.Declared)
	}
}

// withMemberAuthorizationCut over each chain shape: the cut leads a chain
// that reconciles, is the whole chain when nothing else cut the list, and is
// not stated as a chain when the plan's own steps did not reconcile.
func TestWithMemberAuthorizationCutChainShapes(t *testing.T) {
	t.Parallel()
	subjects := func(n int) []SubjectRef {
		out := make([]SubjectRef, n)
		for i := range out {
			out[i] = SubjectRef{Kind: SubjectTeam, CanonicalID: "team:" + string(rune('A'+i))}
		}
		return out
	}
	cut := func(before, after int) contractsv1.ContextFabricRequirementRefinement {
		return contractsv1.ContextFabricRequirementRefinement{
			Stage: contractsv1.ContextFabricOutcomeStageAssembledResult, Coverage: contractsv1.ContextFabricCoverageDetailGraphCohortDeniedByAuthorization,
			Before: before, After: after,
		}
	}
	planStep := contractsv1.ContextFabricRequirementRefinement{
		Stage: contractsv1.ContextFabricOutcomeStageAssembledResult, Basis: contractsv1.ContextFabricNarrowingBasisCanonicalIDLexical, Before: 6, After: 4,
	}
	for name, tc := range map[string]struct {
		in   readPopulation
		want readPopulation
	}{
		"nothing else cut the list": {
			in:   readPopulation{Subjects: subjects(4), Declared: 4, Census: populationEnumerated},
			want: readPopulation{Subjects: subjects(4), Declared: 34, Census: populationEnumerated, AuthorizationDenied: 30, Narrowing: []contractsv1.ContextFabricRequirementRefinement{cut(34, 4)}},
		},
		"a reconciled plan chain": {
			in:   readPopulation{Subjects: subjects(4), Declared: 6, Census: populationEnumerated, Narrowing: []contractsv1.ContextFabricRequirementRefinement{planStep}},
			want: readPopulation{Subjects: subjects(4), Declared: 36, Census: populationEnumerated, AuthorizationDenied: 30, Narrowing: []contractsv1.ContextFabricRequirementRefinement{cut(36, 6), planStep}},
		},
		"an unreconciled plan chain": {
			in:   readPopulation{Subjects: subjects(4), Declared: 6, Census: populationEnumerated},
			want: readPopulation{Subjects: subjects(4), Declared: 36, Census: populationEnumerated, AuthorizationDenied: 30},
		},
		"an absent population": {
			in:   readPopulation{Census: populationAbsent},
			want: readPopulation{Census: populationAbsent},
		},
	} {
		if got := withMemberAuthorizationCut(tc.in, cohortAuthorizationCutDenied); !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s: got %+v, want %+v", name, got, tc.want)
		}
	}
	untouched := readPopulation{Subjects: subjects(4), Declared: 4, Census: populationEnumerated}
	if got := withMemberAuthorizationCut(untouched, 0); !reflect.DeepEqual(got, untouched) {
		t.Fatalf("no denial: got %+v, want it untouched", got)
	}
}

// storedPreFixCutAnswer is a SERVED uncut answer with the authorization-cut
// detail added to its coverage: exactly what a result stored before this fix
// looks like -- the denial on the coverage, satisfied member read rows, and
// completeness complete.
func storedPreFixCutAnswer(t *testing.T) InvestigationResult {
	t.Helper()
	stored := runAuthorizationCutStatus(t, 4, Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}})
	cut := cohortAuthorizationCutCoverage(cohortAuthorizationCutDenied, true)
	stored.Coverage.Details = append(stored.Coverage.Details, cut.Details...)
	stored.Coverage.DegradedReasons = append(stored.Coverage.DegradedReasons, cut.DegradedReasons...)
	stored.Coverage.Partial = true
	stored.Completeness = ComputeAnswerCompleteness(stored)
	if stored.Completeness.State != contractsv1.ContextFabricAnswerCompletenessComplete {
		t.Fatalf("fixture: stored state = %q, want the pre-fix `complete`", stored.Completeness.State)
	}
	return stored
}

// The by-id read serves a stored result through ApplyServedStatusFloors
// (api/context_fabric_result_routes.go), so that is where a stored member read
// row is restated: appended, the stored row kept.
func TestAStoredSatisfiedMemberReadOverACutCohortIsRestatedWhenServed(t *testing.T) {
	t.Parallel()
	stored := storedPreFixCutAnswer(t)
	storedRow := assembledMemberReadRow(t, stored)
	served := ApplyServedStatusFloors(stored)
	if got := served.Completeness.State; got == contractsv1.ContextFabricAnswerCompletenessComplete {
		t.Fatalf("served completeness.state = %q over a stored member set authorization cut by %d", got, cohortAuthorizationCutDenied)
	}
	rows := memberReadRows(served)
	if len(rows) != 2 || !reflect.DeepEqual(rows[0], storedRow) {
		t.Fatalf("assembled member read rows = %+v, want the stored row kept, then the restated cut", rows)
	}
	restated := rows[1]
	if restated.Outcome != contractsv1.ContextFabricRequirementNarrowed ||
		restated.CauseCoverage != contractsv1.ContextFabricCoverageDetailGraphCohortDeniedByAuthorization || !restated.CauseObserved ||
		restated.Served != 4 || restated.Declared != 4+cohortAuthorizationCutDenied {
		t.Fatalf("restated row = %+v, want narrowed 4/%d naming the authorization cut", restated, 4+cohortAuthorizationCutDenied)
	}
	if err := served.Validate(); err != nil {
		t.Fatalf("served result does not validate: %v", err)
	}
	// Idempotent: a second serving pass appends nothing.
	if again := ApplyServedStatusFloors(served); len(memberReadRows(again)) != 2 {
		t.Fatalf("a second pass appended again: %d rows", len(memberReadRows(again)))
	}
}

// The restatement directly, over every shape it must leave alone.
func TestRestateCohortAuthorizationCutMemberReadsTouchesOnlyAStaleSatisfiedMemberRead(t *testing.T) {
	t.Parallel()
	for name, mutate := range map[string]func(*InvestigationResult){
		"no cohort":        func(r *InvestigationResult) { r.Cohort = nil },
		"work item cohort": func(r *InvestigationResult) { r.Cohort.Kind = SubjectWorkItem },
		"no plan":          func(r *InvestigationResult) { r.AnswerPlan = nil },
		"no denial":        func(r *InvestigationResult) { r.Coverage = Coverage{} },
		"denial not degrading": func(r *InvestigationResult) {
			r.Coverage = cohortAuthorizationCutCoverage(cohortAuthorizationCutDenied, false)
		},
		"member read already narrowed": func(r *InvestigationResult) {
			for index := range r.Completeness.Outcomes {
				row := &r.Completeness.Outcomes[index]
				if row.Requirement == memberReadsRequirement && row.Stage == contractsv1.ContextFabricOutcomeStageAssembledResult {
					row.Outcome = contractsv1.ContextFabricRequirementNarrowed
				}
			}
		},
		"a member read of another kind": func(r *InvestigationResult) { r.Cohort.Kind = SubjectProject },
		// The work-item guard on its own: the plan's member read names the
		// work-item kind too, so only the guard keeps it untouched.
		"work item cohort and plan": func(r *InvestigationResult) {
			r.Cohort.Kind = SubjectWorkItem
			mutatePlanRequirement(r, func(q *contractsv1.ContextFabricPlanRequirement) { q.Subject = SubjectWorkItem })
		},
		"not a member role": func(r *InvestigationResult) {
			mutatePlanRequirement(r, func(q *contractsv1.ContextFabricPlanRequirement) { q.Role = string(SubjectRoleSubject) })
		},
		"not a read": func(r *InvestigationResult) {
			mutatePlanRequirement(r, func(q *contractsv1.ContextFabricPlanRequirement) { q.Kind = string(ObligationKindComputed) })
		},
		"planning row only": func(r *InvestigationResult) {
			kept := r.Completeness.Outcomes[:0:0]
			for _, row := range r.Completeness.Outcomes {
				if !(row.Requirement == memberReadsRequirement && row.Stage == contractsv1.ContextFabricOutcomeStageAssembledResult) {
					kept = append(kept, row)
				}
			}
			r.Completeness.Outcomes = kept
		},
		// Counted in fact kinds, not members: raising Declared by members
		// would mix units.
		"a kind-counted satisfied row": func(r *InvestigationResult) {
			for index := range r.Completeness.Outcomes {
				row := &r.Completeness.Outcomes[index]
				if row.Requirement == memberReadsRequirement && row.Stage == contractsv1.ContextFabricOutcomeStageAssembledResult {
					row.Served, row.Declared = 1, 2
				}
			}
		},
	} {
		result := storedPreFixCutAnswer(t)
		mutate(&result)
		before := len(result.Completeness.Outcomes)
		if got, changed := restateCohortAuthorizationCutMemberReads(result); changed || len(got.Completeness.Outcomes) != before {
			t.Fatalf("%s: restate changed %v rows %d -> %d, want untouched", name, changed, before, len(got.Completeness.Outcomes))
		}
	}
	// And it DOES restate the unmutated stored answer, so the table above is
	// not passing because nothing ever restates.
	if _, changed := restateCohortAuthorizationCutMemberReads(storedPreFixCutAnswer(t)); !changed {
		t.Fatal("the stale satisfied member read of a cut cohort was not restated")
	}
}

// A cut cohort whose answer also left a carried member unread names both: the
// cut is the row's coverage cause and the chain's first step, and the unread
// member is the chain's last step with its own code.
func TestACutCohortWithAnUnreadMemberNamesBothParts(t *testing.T) {
	t.Parallel()
	result := runAuthorizationCutCohortQuestionReading(t, 4, cohortAuthorizationCutCoverage(cohortAuthorizationCutDenied, true),
		[]InvestigationGoal{GoalAssessState}, 3)
	row := assembledMemberReadRow(t, result)
	if row.Outcome != contractsv1.ContextFabricRequirementNarrowed || row.Served != 3 || row.Declared != 4+cohortAuthorizationCutDenied {
		t.Fatalf("member read row = %q %d/%d, want narrowed 3/%d", row.Outcome, row.Served, row.Declared, 4+cohortAuthorizationCutDenied)
	}
	if row.CauseCoverage != contractsv1.ContextFabricCoverageDetailGraphCohortDeniedByAuthorization {
		t.Fatalf("cause_coverage = %q, want the authorization cut to stay the row's cause", row.CauseCoverage)
	}
	if len(row.Refinements) != 2 ||
		row.Refinements[0].Coverage != contractsv1.ContextFabricCoverageDetailGraphCohortDeniedByAuthorization ||
		row.Refinements[0].Before != 4+cohortAuthorizationCutDenied || row.Refinements[0].After != 4 ||
		row.Refinements[1].Before != 4 || row.Refinements[1].After != 3 ||
		row.Refinements[1].Coverage == contractsv1.ContextFabricCoverageDetailGraphCohortDeniedByAuthorization {
		t.Fatalf("refinements = %+v, want the cut %d->4, then the unread member 4->3 with its own code", row.Refinements, 4+cohortAuthorizationCutDenied)
	}
	if err := contractsv1.ValidateContextFabricPlanRequirementOutcomeRow(row); err != nil {
		t.Fatalf("member read row does not validate: %v", err)
	}
}

// The reuse path serves a stored pre-fix answer: the member read row is
// restated on the served copy and the reused answer is not complete.
func TestAReusedStoredSatisfiedMemberReadOverACutCohortIsRestated(t *testing.T) {
	t.Parallel()
	stored := storedPreFixCutAnswer(t)
	storedRow := assembledMemberReadRow(t, stored)
	committed := []SubjectRef{{Kind: SubjectOrganization, CanonicalID: "org_1", Label: "Org"}}
	for _, member := range stored.Cohort.Members {
		committed = append(committed, member.Subject)
	}
	engine := mustReuseTestEngine(t, EngineDependencies{
		Graph:     graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: committed}},
		Results:   &resultStoreStub{},
		ReuseGate: readingReuseGate{stored: stored, frame: frameWithPointer([]InvestigationGoal{GoalAssessState}, scopedExpression(SubjectTeam))},
	})
	served, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if !served.Reused {
		t.Fatal("the fixture did not take the reuse path, so it proves nothing about it")
	}
	if got := served.Completeness.State; got == contractsv1.ContextFabricAnswerCompletenessComplete {
		t.Fatalf("reused completeness.state = %q over a stored member set authorization cut by %d", got, cohortAuthorizationCutDenied)
	}
	rows := memberReadRows(served)
	if len(rows) != 2 || !reflect.DeepEqual(rows[0], storedRow) || rows[1].Outcome != contractsv1.ContextFabricRequirementNarrowed ||
		rows[1].Declared != 4+cohortAuthorizationCutDenied {
		t.Fatalf("reused member read rows = %+v, want the stored row then the restated cut", rows)
	}
	if err := served.Validate(); err != nil {
		t.Fatalf("reused result does not validate: %v", err)
	}
}

// mutatePlanRequirement edits the published plan's member read requirement.
func mutatePlanRequirement(r *InvestigationResult, edit func(*contractsv1.ContextFabricPlanRequirement)) {
	plan := *r.AnswerPlan
	plan.Requirements = append([]contractsv1.ContextFabricPlanRequirement(nil), plan.Requirements...)
	for index := range plan.Requirements {
		if plan.Requirements[index].Requirement == memberReadsRequirement {
			edit(&plan.Requirements[index])
		}
	}
	r.AnswerPlan = &plan
}

// A stored answer whose status already reads partial: the status floor
// changes nothing, so the served completeness must be recomputed because the
// member read was restated, not because the status moved.
func TestARestatedMemberReadIsRecomputedWhenTheStatusDoesNotMove(t *testing.T) {
	t.Parallel()
	stored := storedPreFixCutAnswer(t)
	stored.Status = InvestigationPartial
	served := ApplyServedStatusFloors(stored)
	if served.Status != InvestigationPartial {
		t.Fatalf("fixture: served status = %q, want it unchanged at partial", served.Status)
	}
	if got := served.Completeness.State; got == contractsv1.ContextFabricAnswerCompletenessComplete {
		t.Fatalf("served completeness.state = %q: the restated member read was appended but the state was not recomputed", got)
	}
}

// The partially-read arm when the plan's own member chain did NOT reconcile:
// the cut is the row's cause, inferred, and no chain is stated.
func TestAnUnreconciledCutPopulationNamesTheCutAsInferred(t *testing.T) {
	t.Parallel()
	health := contractsv1.ContextFabricFactHealth
	alpha, beta := teamRef("team_alpha"), teamRef("team_beta")
	// Three members found, two carried, and no recorded step for the third:
	// the plan's chain does not reconcile.
	population := withMemberAuthorizationCut(readPopulation{Census: populationEnumerated, Declared: 3, Subjects: []SubjectRef{alpha, beta}}, cohortAuthorizationCutDenied)
	if len(population.Narrowing) != 0 || population.Declared != 3+cohortAuthorizationCutDenied {
		t.Fatalf("fixture: population = %+v, want Declared %d and no chain", population, 3+cohortAuthorizationCutDenied)
	}
	row := readPopulationOutcomeRow(
		RequirementOutcomeRow{Stage: contractsv1.ContextFabricOutcomeStageAssembledResult, Requirement: memberReadsRequirement, Obligation: string(ObligationState)},
		population, readPopulationEvidence{Present: true, coverage: map[string]map[FactKind]SourceState{
			SubjectMapKey(alpha): {health: SourceAvailable},
			SubjectMapKey(beta):  {health: SourceAvailable},
		}},
		[]FactKind{health}, 1, SubjectTeam)
	if row.Outcome != contractsv1.ContextFabricRequirementNarrowed || row.Served != 2 || row.Declared != 3+cohortAuthorizationCutDenied {
		t.Fatalf("row = %q %d/%d, want narrowed 2/%d", row.Outcome, row.Served, row.Declared, 3+cohortAuthorizationCutDenied)
	}
	if row.CauseCoverage != contractsv1.ContextFabricCoverageDetailGraphCohortDeniedByAuthorization || row.CauseObserved || len(row.Refinements) != 0 {
		t.Fatalf("row cause %q observed=%v refinements %d, want the cut, inferred, no chain", row.CauseCoverage, row.CauseObserved, len(row.Refinements))
	}
	if err := contractsv1.ValidateContextFabricPlanRequirementOutcomeRow(row); err != nil {
		t.Fatalf("row does not validate: %v", err)
	}
}

// atOutcomeCap pads a stored answer to the contract's outcome cap with copies
// of its own planning member-read row, keeping it valid.
func atOutcomeCap(t *testing.T, stored InvestigationResult) InvestigationResult {
	t.Helper()
	var planning RequirementOutcomeRow
	for _, row := range stored.Completeness.Outcomes {
		if row.Requirement == memberReadsRequirement && row.Stage == contractsv1.ContextFabricOutcomeStagePlanning {
			planning = row
		}
	}
	if planning.Requirement == "" {
		t.Fatal("fixture: no planning member read row to pad with")
	}
	for len(stored.Completeness.Outcomes) < contractsv1.ContextFabricPlanRequirementOutcomeMaxCount {
		stored.Completeness.Outcomes = append([]RequirementOutcomeRow{planning}, stored.Completeness.Outcomes...)
	}
	stored.Completeness = ComputeAnswerCompleteness(stored)
	if err := stored.Validate(); err != nil {
		t.Fatalf("fixture: the stored answer at the cap does not validate: %v", err)
	}
	return stored
}

// Codex #1807 r1 P2: a stored answer already at the contract's outcome cap.
// The restated row must not make the served document invalid: at the cap the
// lowest-ranked row gives way and the eviction is disclosed (D6080), the
// served answer validates, and it is not complete.
func TestARestatementAtTheOutcomeCapIsServedValidAndDisclosed(t *testing.T) {
	t.Parallel()
	for name, stored := range map[string]InvestigationResult{
		"member read": atOutcomeCap(t, storedPreFixCutAnswer(t)),
		"count":       atOutcomeCap(t, storedPreFixCutCountAnswer(t)),
	} {
		t.Run(name, func(t *testing.T) {
			served := ApplyServedStatusFloors(stored)
			if got := len(served.Completeness.Outcomes); got > contractsv1.ContextFabricPlanRequirementOutcomeMaxCount {
				t.Fatalf("served outcomes = %d, over the cap %d", got, contractsv1.ContextFabricPlanRequirementOutcomeMaxCount)
			}
			if err := served.Validate(); err != nil {
				t.Fatalf("the served answer at the cap does not validate: %v", err)
			}
			if got := served.Completeness.State; got == contractsv1.ContextFabricAnswerCompletenessComplete {
				t.Fatalf("served completeness.state = %q over an authorization cut member set", got)
			}
			if outcomeRowsEvicted(served.Limitations) < 1 {
				t.Fatalf("a row gave way at the cap but no eviction is disclosed: %v", served.Limitations)
			}
		})
	}
}

// storedPreFixCutCountAnswer is storedPreFixCutAnswer for a question that
// also counts the members: a stale satisfied count row beside the member read.
func storedPreFixCutCountAnswer(t *testing.T) InvestigationResult {
	t.Helper()
	stored := runAuthorizationCutCohortQuestion(t, 4, Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
		[]InvestigationGoal{GoalAssessState, GoalCountOrAggregate})
	cut := cohortAuthorizationCutCoverage(cohortAuthorizationCutDenied, true)
	stored.Coverage.Details = append(stored.Coverage.Details, cut.Details...)
	stored.Coverage.DegradedReasons = append(stored.Coverage.DegradedReasons, cut.DegradedReasons...)
	stored.Coverage.Partial = true
	stored.Completeness = ComputeAnswerCompleteness(stored)
	return stored
}
