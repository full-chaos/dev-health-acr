package contextfabric

import (
	"context"
	"reflect"
	"slices"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12710 Fix A: a committed single subject's turn reads the kinds its
// frame's own obligations REQUIRE. The askdev-1 forecast turn (F1: one named
// team, assess_state, the model's workload requirement) planned workload only;
// health/subject/team ended unavailable with requirement_read_not_planned and
// the answer was corrected partial -> degraded on that row.

const subjectHealthRequirement = "health/subject/team"

// subjectObligationCapabilities mirrors the production seed for a team
// (testdata/obligation_seed.txt): health is served by the health kind alone,
// state by several kinds, so health is REQUIRED by its obligation and no state
// kind is required by the state obligation on its own.
func subjectObligationCapabilities() []FactCapability {
	stateOnly := func(kind FactKind) FactCapability {
		return FactCapability{Kind: kind, SupportedSubjectKinds: []SubjectKind{SubjectTeam}, Obligations: map[SubjectKind][]AnswerObligation{SubjectTeam: {ObligationState}}}
	}
	return []FactCapability{
		{Kind: FactHealth, SupportedSubjectKinds: []SubjectKind{SubjectTeam}, Obligations: map[SubjectKind][]AnswerObligation{SubjectTeam: {ObligationHealth, ObligationState}}},
		stateOnly(FactWorkload), stateOnly(FactFlow), stateOnly(FactInvestment),
	}
}

// subjectObligationTurn is one finished single-team turn: the served result,
// the kinds each fact read asked for, and the telemetry.
type subjectObligationTurn struct {
	result    InvestigationResult
	reads     [][]FactKind
	telemetry *recordingTelemetry
}

// runSubjectObligationTurn drives Engine.Investigate for one named team with
// goals [assess_state] and the model's workload requirement only (no health
// requirement). The fact reader serves every kind it is asked for; the health
// kind reports healthState, so a test can make the team's health missing.
func runSubjectObligationTurn(t *testing.T, team SubjectRef, healthState SourceState) subjectObligationTurn {
	t.Helper()
	return runSubjectObligationTurnWith(t, team, healthState, []FactRequirement{{Kind: FactWorkload}})
}

// runSubjectObligationTurnWith is runSubjectObligationTurn with the model's
// fact requirements given (CHAOS-12821: nil is the askdev-1 ML team shape,
// where health is the only kind the turn plans).
func runSubjectObligationTurnWith(t *testing.T, team SubjectRef, healthState SourceState, requirements []FactRequirement) subjectObligationTurn {
	t.Helper()
	turn := subjectObligationTurn{telemetry: &recordingTelemetry{}}
	frame := frameWith([]InvestigationGoal{GoalAssessState}, namedExpression(SubjectTeam), TemporalIntentCurrent, nil)
	engine, err := NewEngine(EngineDependencies{
		Interpreter: familyInterpreter{
			interpreted: InterpretedQuestion{
				Shape: ShapeOpen, RequestedJudgment: "status",
				TimeContext: TimeContext{Axis: TemporalCurrent}, FactRequirements: requirements,
			},
			outcome: QuestionFamilyOutcome{
				Frame: &frame, FrameObligations: frame.Obligations,
				Family: QuestionFamilySubjectInvestigation, Source: QuestionFamilySourceModel,
			},
		},
		Graph: graphReaderStub{
			resolution: SubjectResolution{
				Candidates: []SubjectCandidate{scopeAnchorMatch(team, "s")},
				Committed:  []SubjectRef{team},
			},
			bases: provenCommitBases(team),
			context: GraphContext{
				Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{}, FactRequirements: []FactRequirement{}, EvidenceRefIDs: []string{},
				Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
			},
		},
		Facts: factReaderFunc(func(_ context.Context, _ storage.Principal, request CanonicalFactRequest) (CanonicalFactBundle, error) {
			bundle := CanonicalFactBundle{
				Facts: []CanonicalFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				Version: "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{},
			}
			var kinds []FactKind
			for _, requirement := range request.Requirements {
				kinds = append(kinds, requirement.Kind)
				state := SourceAvailable
				if requirement.Kind == FactHealth {
					state = healthState
				}
				observation := SourceObservation{Source: canonicalFactSourcePrefix + string(requirement.Kind), State: state}
				if state != SourceAvailable {
					observation.Reason = "the provider returned no rows for the subject"
				}
				bundle.Coverage.Sources = append(bundle.Coverage.Sources, observation)
			}
			turn.reads = append(turn.reads, kinds)
			return bundle, nil
		}),
		Synthesizer: synthesizerFunc(func(_ context.Context, principal storage.Principal, input SynthesisInput) (InvestigationResult, error) {
			return InvestigationResult{
				Status: InvestigationComplete, DirectJudgment: "The team appears steady.",
				CurrentState: "Steady.", StrongestPressures: []string{}, Drivers: []DriverJudgment{},
				RemainingWork: []Finding{}, ReadinessGaps: []Finding{}, Paths: []RelationshipPath{},
				Conflicts: []Finding{}, Limitations: []string{}, EvidenceRefIDs: []string{},
				ClaimedFacts: []ClaimedFact{},
				// The production runtime's own coverage composition
				// (model_runtime.go): the graph's and the fact read's.
				Coverage:            MergeCoverage(principal.OrgID, input.Graph.Coverage, input.Facts.Coverage),
				DeterministicAnswer: "The team appears steady.", Warnings: []string{},
				Versions: VersionSet{
					Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
					InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1",
				},
			}, nil
		}),
		Results:      &resultStoreStub{},
		Telemetry:    turn.telemetry,
		Requirements: registryDeriver{capabilities: subjectObligationCapabilities()},
	}, EngineOptions{
		ServiceVersion: "acr-test",
		Now:            func() time.Time { return time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC) },
		NewResultID:    func() string { return "result_12710subjectobl01" },
		// askdev-1 serves the completeness authority's correction, so the
		// served status is the derived one (the live result named
		// health/subject/team as the deciding requirement).
		ServerCompletenessAuthorityEnabled:          true,
		ServerCompletenessAuthoritySymmetricEnabled: true,
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	turn.result, err = engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if len(turn.reads) != 1 {
		t.Fatalf("fact reads = %d, want 1 (the turn must reach the canonical fact read)", len(turn.reads))
	}
	return turn
}

// assembledRow returns the one assembled-result row for identity.
func assembledRow(t *testing.T, result InvestigationResult, identity string) RequirementOutcomeRow {
	t.Helper()
	var found []RequirementOutcomeRow
	for _, row := range result.Completeness.Outcomes {
		if row.Requirement == identity && row.Stage == contractsv1.ContextFabricOutcomeStageAssembledResult {
			found = append(found, row)
		}
	}
	if len(found) != 1 {
		t.Fatalf("assembled rows for %q = %d, want 1: %+v", identity, len(found), result.Completeness.Outcomes)
	}
	return found[0]
}

// TestASingleTeamTurnReadsTheHealthItsObligationRequires is the CHAOS-12710
// repro: one named team, goals [assess_state], no health requirement from the
// model. The health obligation's only serving kind must be planned AND read,
// and the answer must not be degraded by a cell the turn never planned.
func TestASingleTeamTurnReadsTheHealthItsObligationRequires(t *testing.T) {
	t.Parallel()
	team := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:linear:CHAOS", Label: "CHAOS"}
	turn := runSubjectObligationTurn(t, team, SourceAvailable)
	if turn.result.AnswerPlan == nil || !slices.Contains(turn.result.AnswerPlan.FactKinds, FactHealth) {
		var planned []FactKind
		if turn.result.AnswerPlan != nil {
			planned = turn.result.AnswerPlan.FactKinds
		}
		t.Errorf("plan fact kinds = %v, want health (the health obligation's only serving kind)", planned)
	}
	if !slices.Contains(turn.reads[0], FactHealth) {
		t.Errorf("fact request kinds = %v, want health", turn.reads[0])
	}
	row := assembledRow(t, turn.result, subjectHealthRequirement)
	if row.Outcome != contractsv1.ContextFabricRequirementSatisfied {
		t.Errorf("%s = %s/%s, want satisfied", subjectHealthRequirement, row.Outcome, row.CauseCoverage)
	}
	if turn.result.Completeness.State == contractsv1.ContextFabricAnswerCompletenessDegraded || turn.result.Status == InvestigationDegraded {
		t.Errorf("status = %s, completeness = %s, correction = %+v, want not degraded", turn.result.Status, turn.result.Completeness.State, turn.result.Completeness.StatusCorrection)
	}
	want := []recordedSubjectObligationReads{{SubjectRows: 1, KindsNamed: 1, KindsAdded: []FactKind{FactHealth}}}
	if !reflect.DeepEqual(turn.telemetry.subjectObligationReads, want) {
		t.Errorf("subject obligation read lines = %+v, want %+v", turn.telemetry.subjectObligationReads, want)
	}
}

// TestOnlyRequiredKindsOfACommittedSubjectWidenThePlan pins the rule: a
// committed subject's served read row adds its kinds only when its quantifier
// cannot be met without every one of them. A row that any subset of its kinds
// can satisfy, a member, group or operand row, an unavailable row, a computed
// row and a quantifier this package does not recognise add nothing.
func TestOnlyRequiredKindsOfACommittedSubjectWidenThePlan(t *testing.T) {
	t.Parallel()
	row := func(obligation AnswerObligation, quantifier CompletionQuantifier, kinds ...FactKind) DerivedRequirement {
		return DerivedRequirement{
			RequirementCoordinate: RequirementCoordinate{Obligation: obligation, Role: SubjectRoleSubject, Subject: SubjectTeam},
			Kind:                  ObligationKindRead, FactKinds: kinds, Quantifier: quantifier,
		}
	}
	health := row(ObligationHealth, CompletionQuantifierAtLeastOne, FactHealth)
	pair := row(ObligationState, CompletionQuantifierCorroborated, FactFlow, FactWorkload)
	member := health
	member.Role = SubjectRoleMember
	group := health
	group.Role = SubjectRoleGroup
	operand := health
	operand.Role = SubjectRoleOperand
	unavailable := health
	unavailable.Unavailable = RequirementReasonNoDeclaringProducer
	computed := health
	computed.Kind = ObligationKindComputed
	unknown := health
	unknown.Quantifier = CompletionQuantifier("most")
	for _, cell := range []struct {
		name string
		row  DerivedRequirement
		want []FactKind
	}{
		{"health, one kind, at_least_one", health, []FactKind{FactHealth}},
		{"two kinds, corroborated", pair, []FactKind{FactWorkload, FactFlow}},
		{"two kinds, at_least_one", row(ObligationState, CompletionQuantifierAtLeastOne, FactFlow, FactWorkload), nil},
		{"six kinds, corroborated", row(ObligationState, CompletionQuantifierCorroborated, FactFlow, FactHealth, FactInvestment, FactLandscape, FactReadiness, FactWorkload), nil},
		{"member role", member, nil},
		{"group role", group, nil},
		{"operand role", operand, nil},
		{"unavailable", unavailable, nil},
		{"computed", computed, nil},
		{"unrecognised quantifier", unknown, nil},
	} {
		rows := []DerivedRequirement{cell.row}
		if got := SubjectObligationRequiredKinds(rows); !slices.Equal(got, cell.want) {
			t.Errorf("%s: required kinds = %v, want %v", cell.name, got, cell.want)
		}
		if got, want := SubjectObligationRowCount(rows), min(len(cell.want), 1); got != want {
			t.Errorf("%s: row count = %d, want %d", cell.name, got, want)
		}
		if got := subjectObligationFactRequirements(rows, true); got != nil {
			t.Errorf("%s: work-item tuple requirements = %v, want none", cell.name, got)
		}
	}

	// The plan: the widening runs last and first-kind-wins, so a kind the
	// model already named keeps the model's entry and is not added twice.
	definition, _ := LookupQuestionFamily(QuestionFamilySubjectInvestigation)
	model := InterpretedQuestion{FactRequirements: []FactRequirement{{Kind: FactWorkload}}}
	if got := planFactKinds(definition, model, []DerivedRequirement{health, pair}); !slices.Equal(got, []FactKind{FactWorkload, FactHealth, FactFlow}) {
		t.Fatalf("plan = %v, want [workload health flow]: the model's kind first, then the required kinds once", got)
	}
	requirements := subjectObligationFactRequirements([]DerivedRequirement{health, pair}, false)
	if added := requirementKindsAbsentFrom(requirements, []FactRequirement{{Kind: FactWorkload}}); !slices.Equal(added, []FactKind{FactHealth, FactFlow}) {
		t.Fatalf("kinds added = %v, want [health flow]: workload was already requested", added)
	}
}

// providerMatrixTeams is one team per provider. Attribution and the obligation
// seed are provider-agnostic: a team's health obligation is the same cell on
// every provider, so the planned and read kinds must be the same too.
var providerMatrixTeams = []SubjectRef{
	{Kind: SubjectTeam, CanonicalID: "team:jira:PLAT", Label: "PLAT"},
	{Kind: SubjectTeam, CanonicalID: "team:gl:full.chaos", Label: "full.chaos"},
	{Kind: SubjectTeam, CanonicalID: "team:gh:ops-team", Label: "ops-team"},
	{Kind: SubjectTeam, CanonicalID: "team:linear:CHAOS", Label: "CHAOS"},
}

// TestATeamWhoseHealthIsMissingIsNeverServedHealthy is North Star check 12 on
// the new read, over the provider matrix: health is planned and read, the
// health provider reports nothing for the team, and the health cell ends
// unavailable or narrowed, never satisfied, with the answer short of complete.
// With health served, the same team on every provider reads the same kinds and
// satisfies the cell.
func TestATeamWhoseHealthIsMissingIsNeverServedHealthy(t *testing.T) {
	t.Parallel()
	for _, team := range providerMatrixTeams {
		team := team
		t.Run(team.CanonicalID, func(t *testing.T) {
			t.Parallel()
			served := runSubjectObligationTurn(t, team, SourceAvailable)
			if !slices.Equal(served.reads[0], []FactKind{FactWorkload, FactHealth}) {
				t.Fatalf("fact request kinds = %v, want [workload health]", served.reads[0])
			}
			if row := assembledRow(t, served.result, subjectHealthRequirement); row.Outcome != contractsv1.ContextFabricRequirementSatisfied {
				t.Fatalf("served health: %s = %s/%s, want satisfied", subjectHealthRequirement, row.Outcome, row.CauseCoverage)
			}
			for _, missing := range []SourceState{SourceNoData, SourceUnavailable} {
				turn := runSubjectObligationTurn(t, team, missing)
				if !slices.Contains(turn.reads[0], FactHealth) {
					t.Fatalf("health %s: fact request kinds = %v, want health read", missing, turn.reads[0])
				}
				row := assembledRow(t, turn.result, subjectHealthRequirement)
				if row.Outcome != contractsv1.ContextFabricRequirementUnavailable && row.Outcome != contractsv1.ContextFabricRequirementNarrowed {
					t.Errorf("health %s: %s = %s/%s, want unavailable or narrowed", missing, subjectHealthRequirement, row.Outcome, row.CauseCoverage)
				}
				if row.CauseCoverage == contractsv1.ContextFabricCoverageDetailRequirementReadNotPlanned {
					t.Errorf("health %s: cause = %s, want the provider's own cause: the kind was read", missing, row.CauseCoverage)
				}
				if turn.result.Completeness.State == contractsv1.ContextFabricAnswerCompletenessComplete || turn.result.Status == InvestigationComplete {
					t.Errorf("health %s: status = %s, completeness = %s, want short of complete", missing, turn.result.Status, turn.result.Completeness.State)
				}
			}
		})
	}
}
