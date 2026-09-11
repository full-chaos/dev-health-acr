package contextfabric

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// groupAuthorizingGraph is the graph double for the stage-2 pins.
//
// It answers the MEMBER resolution exactly as the existing grouped fixtures
// do, and it answers a resolution rooted on GROUP identities by committing
// only the ones it admits. Modelling authorization as "the graph does not
// return what you may not see" is not a convenience for the test: it is how
// authorization already works everywhere else in this package -- a subject the
// principal cannot see does not come back committed, and no separate
// permission verdict exists to consult.
//
// It records every resolution it was asked for, so a pin can distinguish
// "the group was denied" from "the group was never asked about", which are
// different states that produce the same served answer.
type groupAuthorizingGraph struct {
	graphReaderStub
	// denied is keyed by canonical id. A group named here resolves to
	// nothing, exactly as an unauthorized subject does.
	denied map[string]struct{}
	// hinted records, in order, the subject hints of every resolution this
	// double was asked to perform.
	hinted [][]SubjectHint
	// hintedCaps records, per resolution, the MaxSubjectCandidates it ran
	// with, so a pin can prove no authorization call was handed more hints
	// than it could commit.
	hintedCaps []int
	// authorizationErr, when set, is returned by every HINTED resolution --
	// an authorizer that could not answer, as distinct from one that said no.
	authorizationErr error
}

func (g *groupAuthorizingGraph) ResolveSubjects(ctx context.Context, principal storage.Principal, request InvestigationRequest, interpreted InterpretedQuestion, binding ResolvedGraphBinding, confirmedKind *ConfirmedExpectedKind, confirmedAnchor *ConfirmedAnchorSelection, frame *QuestionFrame, scopeAnchorKind SubjectKind) (SubjectResolution, StructureOfferMaterial, CommitBasisSet, CommitDecisionDigestSet, error) {
	hints := request.RequestedScope.SubjectHints
	g.hinted = append(g.hinted, hints)
	g.hintedCaps = append(g.hintedCaps, request.Options.MaxSubjectCandidates)
	if len(hints) == 0 {
		return g.graphReaderStub.ResolveSubjects(ctx, principal, request, interpreted, binding, confirmedKind, confirmedAnchor, frame, scopeAnchorKind)
	}
	if g.authorizationErr != nil {
		return SubjectResolution{}, StructureOfferMaterial{}, nil, nil, g.authorizationErr
	}
	// A hinted resolution is the authorization question: return the hinted
	// subjects that are admitted, and nothing for the ones that are not.
	committed := make([]SubjectRef, 0, len(hints))
	for _, hint := range hints {
		if _, refused := g.denied[hint.ID]; refused {
			continue
		}
		committed = append(committed, SubjectRef{Kind: hint.Kind, CanonicalID: hint.ID, Label: hint.Label})
	}
	// THE RESOLVER'S CANDIDATE CAP, modelled rather than ignored. The real
	// caller-hint exit finalizes at request.Options.MaxSubjectCandidates and
	// drops every resolvable hint past it, exactly as it drops an
	// unauthorized one -- measured on the real resolver by
	// graphrank.TestTheCallerHintExitCommitsNoMoreThanItsCandidateCap. A
	// double without the cap admitted all 250 groups of a legal cohort while
	// production admitted 50, which is how a turn that read one group in five
	// passed every pin here.
	if limit := request.Options.MaxSubjectCandidates; limit > 0 && len(committed) > limit {
		committed = committed[:limit]
	}
	return SubjectResolution{Candidates: []SubjectCandidate{}, Committed: committed},
		StructureOfferMaterial{}, provenCommitBases(committed...), nil, nil
}

// groupReadMemberFacts is the two-member, two-team fixture the row-3 and row-5
// pins share: project_a under team_security, project_b under team_platform.
// Two groups, not one, so "the read was rooted on the admitted groups" is a
// claim a single-group fixture could not discriminate.
// groupReadRequirementDeriver hands the engine the requirement rows a grouped
// question actually demands, so the pins drive the REAL derivation consumer
// (PlanRequirementsFromDerived -> plan.Requirements -> the group read's own
// row selection) rather than a plan somebody hand-stamped.
//
// The rows are TRANSCRIBED FROM A DEPLOYED ANSWER, not invented: the live
// store's grouped `qa-grouped-clean` result carries exactly this coordinate --
// obligation `state`, role `group`, subject `team`, kind `read`, scope
// `each_group`, quantifier `corroborated` -- with that six-kind server list.
// A member row rides alongside it for the same reason the real plan has one:
// the group read must select the group row and leave the member row to the
// member read, and a fixture with only the group row could not tell a correct
// selection from one that takes every row it sees.
// groupReadFramedInterpreter reports a grouped family AND the validated frame
// that family was read off, because the requirement derivation runs on the
// FRAME and produces nothing without one.
//
// The shared groupedFamilyInterpreter carries no frame, which is correct for
// the pins it was written for -- they are about the grouping refusal, which is
// decided before requirements matter. A grouped turn in production always has
// a frame here, so a fixture without one would be measuring a state the
// deployed engine never reaches.
type groupReadFramedInterpreter struct {
	interpretation InterpretedQuestion
	groupKind      SubjectKind
	memberKind     SubjectKind
}

func (i groupReadFramedInterpreter) Interpret(_ context.Context, _ storage.Principal, _ InvestigationRequest) (InterpretedQuestion, QuestionFamilyOutcome, error) {
	frame := QuestionFrame{
		Version: QuestionFrameVersion,
		SubjectExpression: SubjectExpression{
			Kind:    SubjectExpressionGroupedMembers,
			Grouped: &GroupedSetExpression{GroupKind: i.groupKind, MemberKind: i.memberKind},
		},
		Obligations: []AnswerObligation{ObligationState},
		Goals:       []InvestigationGoal{GoalAssessState},
		Temporal:    TemporalIntentCurrent,
	}
	validated := ValidateFrame(frame, nil, ShapeDiscoveredCohort)
	gate := DecideFrameGate(validated, true)
	accepted := validated.Frame
	return i.interpretation, QuestionFamilyOutcome{
		Family:        QuestionFamilyGroupedCohortStatus,
		Source:        QuestionFamilySourceModel,
		WinningSample: FamilySample{GroupKind: i.groupKind},
		Frame:         &accepted,
		Gate:          gate,
	}, nil
}

type groupReadRequirementDeriver struct{}

func (groupReadRequirementDeriver) DeriveRequirements(QuestionFrame) []DerivedRequirement {
	return []DerivedRequirement{
		{
			RequirementCoordinate: RequirementCoordinate{Obligation: ObligationState, Role: SubjectRoleGroup, Subject: SubjectTeam},
			Kind:                  ObligationKindRead,
			FactKinds:             []FactKind{FactFlow, FactHealth, FactInvestment, FactLandscape, FactReadiness, FactWorkload},
			Scope:                 CompletionScopeEachGroup,
			Quantifier:            CompletionQuantifierCorroborated,
		},
		{
			RequirementCoordinate: RequirementCoordinate{Obligation: ObligationState, Role: SubjectRoleMember, Subject: SubjectProject},
			Kind:                  ObligationKindRead,
			FactKinds:             []FactKind{FactMetrics},
			Scope:                 CompletionScopeEachMember,
			Quantifier:            CompletionQuantifierAtLeastOne,
		},
	}
}

func groupReadMemberFacts() []CanonicalFact {
	return []CanonicalFact{
		teamScopedFact("project_a", "team_security", "Security"),
		teamScopedFact("project_b", "team_platform", "Platform"),
	}
}

// groupReadOverBoundMemberFacts places every member under its OWN team, so the
// discovered group list exceeds the contract's 250-group bound. 251 is
// deliberately one over rather than comfortably over: a bound tested far from
// its edge cannot tell `>` from `>=`.
func groupReadOverBoundMemberFacts() []CanonicalFact { return groupReadBoundedMemberFacts(251) }

// groupReadBoundedMemberFacts places each of count members under its own team,
// so the discovered group list has exactly count entries.
func groupReadBoundedMemberFacts(count int) []CanonicalFact {
	facts := make([]CanonicalFact, 0, count)
	for index := 0; index < count; index++ {
		member := fmt.Sprintf("project_%03d", index)
		team := fmt.Sprintf("team_%03d", index)
		facts = append(facts, teamScopedFact(member, team, team))
	}
	return facts
}

func groupReadCohortMembers(count int) []CohortMember {
	members := make([]CohortMember, 0, count)
	for index := 0; index < count; index++ {
		id := fmt.Sprintf("project_%03d", index)
		members = append(members, CohortMember{
			Subject:          SubjectRef{Kind: SubjectProject, CanonicalID: id, Label: id},
			Rank:             index + 1,
			InclusionReasons: []string{"matched"},
		})
	}
	return members
}

func groupReadEngineFixture(t *testing.T, telemetry EngineTelemetry, facts CanonicalFactReader) (*Engine, InvestigationRequest) {
	t.Helper()
	return groupReadEngineFixtureWith(t, telemetry, facts, []CohortMember{
		{Subject: SubjectRef{Kind: SubjectProject, CanonicalID: "project_a", Label: "project_a"}, Rank: 1, InclusionReasons: []string{"matched"}},
		{Subject: SubjectRef{Kind: SubjectProject, CanonicalID: "project_b", Label: "project_b"}, Rank: 2, InclusionReasons: []string{"matched"}},
	}, nil)
}

func groupReadEngineFixtureDenying(t *testing.T, telemetry EngineTelemetry, facts CanonicalFactReader, deniedGroupIDs ...string) (*Engine, InvestigationRequest) {
	t.Helper()
	denied := make(map[string]struct{}, len(deniedGroupIDs))
	for _, id := range deniedGroupIDs {
		denied[id] = struct{}{}
	}
	return groupReadEngineFixtureWith(t, telemetry, facts, []CohortMember{
		{Subject: SubjectRef{Kind: SubjectProject, CanonicalID: "project_a", Label: "project_a"}, Rank: 1, InclusionReasons: []string{"matched"}},
		{Subject: SubjectRef{Kind: SubjectProject, CanonicalID: "project_b", Label: "project_b"}, Rank: 2, InclusionReasons: []string{"matched"}},
	}, denied)
}

// groupReadEngineFixtureSelfGroup builds a turn whose cohort comes back as
// TEAMS while the plan's group axis is also `team`, so the axis collapses onto
// the members at the plan seam -- with a perfectly legal frame, which is the
// point. The frame gate cannot catch this: the plan's member kind is stamped
// from the cohort the graph actually returned, and that is not known when the
// frame is validated.
func groupReadEngineFixtureSelfGroup(t *testing.T, telemetry EngineTelemetry, facts CanonicalFactReader) (*Engine, InvestigationRequest) {
	t.Helper()
	return groupReadEngineFixtureWithKinds(t, telemetry, facts, []CohortMember{
		{Subject: SubjectRef{Kind: SubjectTeam, CanonicalID: TeamCanonicalID("team_security"), Label: "Security"}, Rank: 1, InclusionReasons: []string{"matched"}},
		{Subject: SubjectRef{Kind: SubjectTeam, CanonicalID: TeamCanonicalID("team_platform"), Label: "Platform"}, Rank: 2, InclusionReasons: []string{"matched"}},
	}, nil, SubjectTeam)
}

func groupReadEngineFixtureOverBound(t *testing.T, telemetry EngineTelemetry, facts CanonicalFactReader) (*Engine, InvestigationRequest) {
	t.Helper()
	return groupReadEngineFixtureWith(t, telemetry, facts, groupReadCohortMembers(251), nil)
}

func groupReadEngineFixtureWith(t *testing.T, telemetry EngineTelemetry, facts CanonicalFactReader, members []CohortMember, denied map[string]struct{}) (*Engine, InvestigationRequest) {
	t.Helper()
	return groupReadEngineFixtureWithKinds(t, telemetry, facts, members, denied, SubjectProject)
}

func groupReadEngineFixtureWithKinds(t *testing.T, telemetry EngineTelemetry, facts CanonicalFactReader, members []CohortMember, denied map[string]struct{}, cohortKind SubjectKind) (*Engine, InvestigationRequest) {
	t.Helper()
	return groupReadEngineFixtureFull(t, telemetry, facts, members, denied, cohortKind, nil, nil)
}

// groupReadEngineFixtureFull is the same fixture parameterised on the engine
// OPTIONS and on a synthesis counter, so a test can drive the bounded retry and
// count fact-service calls across it. The retry is the case the two-read
// guarantee is actually about: a turn that re-synthesizes must not re-read.
func groupReadEngineFixtureFull(t *testing.T, telemetry EngineTelemetry, facts CanonicalFactReader, members []CohortMember, denied map[string]struct{}, cohortKind SubjectKind, options *EngineOptions, synthesisCalls *int) (*Engine, InvestigationRequest) {
	t.Helper()
	return groupReadEngineFixtureConfigured(t, telemetry, facts, members, denied, cohortKind, options, synthesisCalls, nil)
}

// groupReadFixtureConfig is what a configured fixture may change: the graph
// double (for an authorizer that misbehaves) and the requirement deriver (for
// a plan whose rows differ from the transcribed qa-grouped-clean pair).
type groupReadFixtureConfig struct {
	graph   *groupAuthorizingGraph
	deriver RequirementDeriver
}

// groupReadEngineFixtureConfigured is the same fixture with a hook onto the
// graph double and the deriver.
func groupReadEngineFixtureConfigured(t *testing.T, telemetry EngineTelemetry, facts CanonicalFactReader, members []CohortMember, denied map[string]struct{}, cohortKind SubjectKind, options *EngineOptions, synthesisCalls *int, configure func(*groupReadFixtureConfig)) (*Engine, InvestigationRequest) {
	t.Helper()
	cohort := &Cohort{
		Kind: cohortKind, Rationale: "kind census match", Complete: true,
		Members: members,
	}
	interpretation := InterpretedQuestion{
		Shape: ShapeDiscoveredCohort, RequestedJudgment: "project_status_by_group",
		TimeContext:      TimeContext{Axis: TemporalCurrent},
		FactRequirements: []FactRequirement{{Kind: FactMetrics}},
	}
	graph := &groupAuthorizingGraph{
		graphReaderStub: graphReaderStub{
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
			context: GraphContext{
				Cohort: cohort, Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{},
				FactRequirements: []FactRequirement{}, EvidenceRefIDs: []string{},
				Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
			},
		},
		denied: denied,
	}
	config := groupReadFixtureConfig{graph: graph, deriver: groupReadRequirementDeriver{}}
	if configure != nil {
		configure(&config)
	}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: groupReadFramedInterpreter{interpretation: interpretation, groupKind: SubjectTeam, memberKind: SubjectProject},
		// The FRAME stays legal -- projects grouped by team -- on purpose,
		// even when the cohort comes back as teams. That is the whole point
		// of the plan-seam pin: the collapse the plan sees is invisible to
		// frame validation, because the plan's member kind is stamped from
		// the cohort the graph actually returned and the frame was validated
		// long before that was known. A fixture that made the frame illegal
		// too would be re-testing the frame gate and would never reach the
		// seam under test.
		Graph:        graph,
		Facts:        facts,
		Requirements: config.deriver,
		Synthesizer: synthesizerFunc(func(_ context.Context, principal storage.Principal, input SynthesisInput) (InvestigationResult, error) {
			if synthesisCalls != nil {
				*synthesisCalls++
			}
			// The facts SYNTHESIS actually received. A probe asserting on the
			// served claims alone cannot see these: this double builds its
			// claims from the cohort, so a fact that reached synthesis and
			// was never cited is invisible in the answer -- and "reached
			// synthesis" is the property the authorization clause is about.
			groupReadSynthesisFacts = append(groupReadSynthesisFacts, input.Facts.Facts...)
			// And PER PASS, so a retry pin can tell the first synthesis's
			// input from the one the served document was built from.
			pass := groupReadSynthesisPass{Facts: append([]CanonicalFact(nil), input.Facts.Facts...)}
			if input.Graph.Cohort != nil {
				for _, member := range input.Graph.Cohort.Members {
					pass.Members = append(pass.Members, member.Subject)
				}
				for _, group := range input.Graph.Cohort.Groups {
					pass.Groups = append(pass.Groups, group.Subject)
				}
			}
			groupReadSynthesisPasses = append(groupReadSynthesisPasses, pass)
			// One claim per SURVIVING cohort member, so the answer's measured
			// size actually shrinks when narrowing drops a member. A
			// synthesizer returning a fixed-size answer cannot be retried
			// into a fit -- the engine refuses instead -- and a retry pin
			// built on one would never see a second synthesis at all.
			claims := []ClaimedFact{}
			if input.Graph.Cohort != nil {
				for _, member := range input.Graph.Cohort.Members {
					for index := 0; index < groupReadClaimsPerMember; index++ {
						claims = append(claims, ClaimedFact{
							ClaimID: fmt.Sprintf("claim_%s_%d", member.Subject.CanonicalID, index),
							Kind:    FactMetrics, Subject: member.Subject, Field: "status",
							Value: ScalarValue{String: ptrString("green")},
						})
					}
				}
			}
			return InvestigationResult{
				ClaimedFacts:        claims,
				Status:              InvestigationPartial,
				DirectJudgment:      "The available evidence does not separate these projects.",
				CurrentState:        "Projects were discovered.",
				DeterministicAnswer: "Projects were discovered and grouped by their owning team.",
				StrongestPressures:  []string{},
				Drivers:             []DriverJudgment{},
				RemainingWork:       []Finding{}, ReadinessGaps: []Finding{}, Paths: []RelationshipPath{},
				Conflicts: []Finding{}, Limitations: []string{}, EvidenceRefIDs: []string{},
				// THE PRODUCTION SYNTHESIZER'S OWN COVERAGE RULE
				// (model_runtime.go and genkitruntime both compose it this
				// way), not an empty literal. The requirement evaluator reads
				// the SERVED document's coverage, so a double that dropped it
				// made every read requirement look unplanned -- and no pin in
				// this file read a requirement row until row 4 did, which is
				// how the double stayed wrong.
				Coverage: MergeCoverage(principal.OrgID, input.Graph.Coverage, input.Facts.Coverage),
				Warnings: []string{},
				Versions: VersionSet{
					Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
					InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1",
				},
			}, nil
		}),
		Results:   &resultStoreStub{},
		Telemetry: telemetry,
	}, groupReadEngineOptions(options))
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	request := validInvestigationRequestWithConfirmedWindow()
	request.RequestID = "request_52850001"
	request.Question = "how are the projects doing for each team?"
	return engine, request
}

// groupReadEngineOptions returns the fixture's default options, or the
// caller's with the deterministic clock and id filled in -- a test that varies
// only the budget should not have to restate the rest and risk drifting from
// every other fixture in this file.
func groupReadEngineOptions(override *EngineOptions) EngineOptions {
	options := EngineOptions{ServiceVersion: "acr-test"}
	if override != nil {
		options = *override
		options.ServiceVersion = "acr-test"
	}
	options.Now = func() time.Time { return time.Unix(300, 0).UTC() }
	options.NewResultID = func() string { return "result_52850001" }
	return options
}

// groupReadClaimsPerMember is how many claims the fixture's synthesizer emits
// per surviving cohort member.
//
// It is a variable, not a constant, because the retry pin needs the answer's
// measured size to exceed the item budget on the first pass and fall under it
// after narrowing -- and the only lever that scales with the cohort is this
// one. A test that changes it restores it, and no test that changes it may be
// parallel.
var groupReadClaimsPerMember = 1

// groupReadSynthesisFacts accumulates every fact handed to the fixture's
// synthesizer. Reset by the test that reads it; no test that reads it may be
// parallel.
var groupReadSynthesisFacts []CanonicalFact

// groupReadSynthesisPass is what ONE synthesis call received: the facts, and
// the cohort's members and groups as that pass saw them.
type groupReadSynthesisPass struct {
	Facts   []CanonicalFact
	Members []SubjectRef
	Groups  []SubjectRef
}

// groupReadSynthesisPasses records every synthesis call's input, in order.
// Reset by the test that reads it; no test that reads it may be parallel.
var groupReadSynthesisPasses []groupReadSynthesisPass
