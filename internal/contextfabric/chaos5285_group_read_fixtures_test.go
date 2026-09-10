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
}

func (g *groupAuthorizingGraph) ResolveSubjects(ctx context.Context, principal storage.Principal, request InvestigationRequest, interpreted InterpretedQuestion, binding ResolvedGraphBinding, confirmedKind *ConfirmedExpectedKind, confirmedAnchor *ConfirmedAnchorSelection, frame *QuestionFrame, scopeAnchorKind SubjectKind) (SubjectResolution, StructureOfferMaterial, CommitBasisSet, CommitDecisionDigestSet, error) {
	hints := request.RequestedScope.SubjectHints
	g.hinted = append(g.hinted, hints)
	if len(hints) == 0 {
		return g.graphReaderStub.ResolveSubjects(ctx, principal, request, interpreted, binding, confirmedKind, confirmedAnchor, frame, scopeAnchorKind)
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
	return SubjectResolution{Candidates: []SubjectCandidate{}, Committed: committed},
		StructureOfferMaterial{}, provenCommitBases(committed...), nil, nil
}

// groupReadMemberFacts is the two-member, two-team fixture the row-3 and row-5
// pins share: project_a under team_security, project_b under team_platform.
// Two groups, not one, so "the read was rooted on the admitted groups" is a
// claim a single-group fixture could not discriminate.
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

func groupReadEngineFixtureOverBound(t *testing.T, telemetry EngineTelemetry, facts CanonicalFactReader) (*Engine, InvestigationRequest) {
	t.Helper()
	return groupReadEngineFixtureWith(t, telemetry, facts, groupReadCohortMembers(251), nil)
}

func groupReadEngineFixtureWith(t *testing.T, telemetry EngineTelemetry, facts CanonicalFactReader, members []CohortMember, denied map[string]struct{}) (*Engine, InvestigationRequest) {
	t.Helper()
	cohort := &Cohort{
		Kind: SubjectProject, Rationale: "kind census match", Complete: true,
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
	engine, err := NewEngine(EngineDependencies{
		Interpreter: groupedFamilyInterpreter{interpretation: interpretation, groupKind: SubjectTeam},
		Graph:       graph,
		Facts:       facts,
		Synthesizer: synthesizerFunc(func(_ context.Context, _ storage.Principal, _ SynthesisInput) (InvestigationResult, error) {
			return InvestigationResult{
				Status:              InvestigationPartial,
				DirectJudgment:      "The available evidence does not separate these projects.",
				CurrentState:        "Projects were discovered.",
				DeterministicAnswer: "Projects were discovered and grouped by their owning team.",
				StrongestPressures:  []string{},
				Drivers:             []DriverJudgment{},
				RemainingWork:       []Finding{}, ReadinessGaps: []Finding{}, Paths: []RelationshipPath{},
				Conflicts: []Finding{}, Limitations: []string{}, EvidenceRefIDs: []string{},
				ClaimedFacts: []ClaimedFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				Warnings: []string{},
				Versions: VersionSet{
					Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
					InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1",
				},
			}, nil
		}),
		Results:   &resultStoreStub{},
		Telemetry: telemetry,
	}, EngineOptions{
		ServiceVersion: "acr-test",
		Now:            func() time.Time { return time.Unix(300, 0).UTC() },
		NewResultID:    func() string { return "result_52850001" },
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	request := validInvestigationRequestWithConfirmedWindow()
	request.RequestID = "request_52850001"
	request.Question = "how are the projects doing for each team?"
	return engine, request
}
