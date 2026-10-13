package contextfabric

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func blockersFact(team SubjectRef, state SourceState, count *int64, extra map[string]FactValue) CanonicalFact {
	fields := map[string]FactValue{}
	if count != nil {
		fields[teamBlockersField] = IntegerFactValue(*count)
	}
	for k, v := range extra {
		fields[k] = v
	}
	return CanonicalFact{Kind: FactBlockers, Subject: team, SourceState: state, EvidenceRefIDs: []string{"evidence_blk_" + team.Label}, Fields: fields}
}

func intp(n int64) *int64 { return &n }

func investigateTeamBlockers(t *testing.T, teams []SubjectRef, facts []CanonicalFact) InvestigationResult {
	t.Helper()
	members := make([]CohortMember, 0, len(teams))
	for i, team := range teams {
		members = append(members, CohortMember{Subject: team, Rank: i + 1, InclusionReasons: []string{"matched"}, EvidenceRefIDs: []string{"evidence_team_roster"}})
	}
	cohort := &Cohort{Kind: SubjectTeam, Rationale: "kind census match", Members: members}
	interpretation := InterpretedQuestion{
		Shape: ShapeDiscoveredCohort, RequestedJudgment: "teams_blocked",
		TimeContext: TimeContext{Axis: TemporalCurrent}, FactRequirements: []FactRequirement{{Kind: FactBlockers}},
	}
	graph := graphReaderStub{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
		context: GraphContext{Cohort: cohort, Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{},
			FactRequirements: []FactRequirement{}, EvidenceRefIDs: []string{},
			Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}},
	}
	store := &resultStoreStub{}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return interpretation, nil
		}),
		Graph: graph,
		Facts: factReaderFunc(func(_ context.Context, _ storage.Principal, _ CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{Facts: facts,
				Coverage: Coverage{Sources: []SourceObservation{{Source: "canonical_fact:blockers", State: SourceAvailable}}, DegradedReasons: []string{}},
				Version:  "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{}}, nil
		}),
		Synthesizer: synthesizerFunc(func(_ context.Context, _ storage.Principal, _ SynthesisInput) (InvestigationResult, error) {
			return InvestigationResult{Status: InvestigationPartial, DirectJudgment: "This investigation is partial: some canonical or graph coverage was unavailable.",
				CurrentState: "x", DeterministicAnswer: "x", StrongestPressures: []string{}, Drivers: []DriverJudgment{},
				RemainingWork: []Finding{}, ReadinessGaps: []Finding{}, Paths: []RelationshipPath{}, Conflicts: []Finding{}, Limitations: []string{},
				EvidenceRefIDs: []string{}, ClaimedFacts: []ClaimedFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}, Warnings: []string{},
				Versions: VersionSet{Backend: "test", ProjectionVersion: "p", QueryVersion: "q", InterpretationVersion: "i", SynthesisVersion: "s"}}, nil
		}),
		Results: store, Telemetry: &recordingTelemetry{},
	}, EngineOptions{ServiceVersion: "acr-test", Now: func() time.Time { return time.Unix(300, 0).UTC() }, NewResultID: func() string { return "result_probe1" }})
	if err != nil {
		t.Fatal(err)
	}
	request := validInvestigationRequestWithConfirmedWindow()
	request.RequestID = "request_probe1"
	request.Question = "which teams are blocked?"
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org-1"}, request)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func teamsAB() (SubjectRef, SubjectRef, SubjectRef) {
	return SubjectRef{Kind: SubjectTeam, CanonicalID: "team:A", Label: "Alpha"},
		SubjectRef{Kind: SubjectTeam, CanonicalID: "team:B", Label: "Beta"},
		SubjectRef{Kind: SubjectTeam, CanonicalID: "team:C", Label: "Gamma"}
}

func TestTeamCohortServesBlockersAsKeyFactAndDriver(t *testing.T) {
	a, b, c := teamsAB()
	result := investigateTeamBlockers(t, []SubjectRef{a, b, c}, []CanonicalFact{
		blockersFact(a, SourceAvailable, intp(3), nil),
		blockersFact(b, SourceAvailable, intp(30), map[string]FactValue{"project_blocked_work_items_current": IntegerFactValue(4), "repository_link_basis": StringFactValue("explicit_text")}),
		blockersFact(c, SourceAvailable, intp(0), nil),
	})
	if got, want := driverTitles(result.Drivers), "Blocked work items: 30, 4 of them reached through project-linked repositories (weakest link tier over its counted links: explicit_text)|Blocked work items: 3"; got != want {
		t.Fatalf("drivers = %q, want %q", got, want)
	}
	if len(result.ClaimedFacts) != 2 {
		t.Fatalf("claims = %d, want 2", len(result.ClaimedFacts))
	}
	for _, d := range result.Drivers {
		if d.Category != "blockers" || len(d.EvidenceRefIDs) == 0 || len(d.ClaimedFactIDs) != 1 {
			t.Errorf("driver = %+v", d)
		}
	}
}

func TestTeamCohortBlockersMissingAndZeroServeNoDriver(t *testing.T) {
	a, b, c := teamsAB()
	result := investigateTeamBlockers(t, []SubjectRef{a, b, c}, []CanonicalFact{
		blockersFact(a, SourceStale, intp(9), nil),
		blockersFact(b, SourceAvailable, nil, nil),
		blockersFact(c, SourceAvailable, intp(0), nil),
	})
	if len(result.Drivers) != 0 || len(result.ClaimedFacts) != 0 {
		t.Fatalf("drivers=%q claims=%d, want none", driverTitles(result.Drivers), len(result.ClaimedFacts))
	}
}

func TestTeamCohortTruncatedBlockersAreServedAsALabelledLowerBound(t *testing.T) {
	a, _, _ := teamsAB()
	result := investigateTeamBlockers(t, []SubjectRef{a}, []CanonicalFact{blockersFact(a, SourceTruncated, intp(12), nil)})
	if got, want := driverTitles(result.Drivers), "At least 12 blocked work items (partial read)"; got != want {
		t.Fatalf("drivers = %q, want %q", got, want)
	}
	if len(result.ClaimedFacts) != 1 || result.Drivers[0].Qualification == "" || result.Drivers[0].Confidence != 0.95 {
		t.Fatalf("claims=%d driver=%+v, want one claim and a qualified driver with the ordinary confidence", len(result.ClaimedFacts), result.Drivers[0])
	}
}

func TestTeamBlockersDecisionTelemetryCountsEachReason(t *testing.T) {
	a, b, c := teamsAB()
	d := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:D", Label: "Delta"}
	e := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:E", Label: "Eps"}
	f := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:F", Label: "Zeta"}
	cohort := &Cohort{Kind: SubjectTeam, Members: []CohortMember{{Subject: a}, {Subject: b}, {Subject: c}, {Subject: d}, {Subject: e}, {Subject: f}}}
	facts := []CanonicalFact{
		blockersFact(a, SourceAvailable, intp(4), nil),
		blockersFact(b, SourceTruncated, intp(6), nil),
		blockersFact(c, SourceAvailable, intp(0), nil),
		blockersFact(d, SourceAvailable, nil, nil),
		blockersFact(e, SourceStale, intp(8), nil),
	}
	got := teamBlockersDrivers(InterpretedQuestion{FactRequirements: []FactRequirement{{Kind: FactBlockers}}}, cohort, facts, nil, nil, ItemAllocation{}, 0)
	want := TeamBlockersDriverEvent{Emitted: 2, TruncatedServed: 1, SkippedMissing: 2, SkippedStale: 1, SkippedZero: 1}
	if got.Event == nil || *got.Event != want {
		t.Fatalf("event = %+v, want %+v", got.Event, want)
	}
}

func TestTeamCohortBlockersOrderByCountThenLabelAndDiscloseTheCut(t *testing.T) {
	a, b, c := teamsAB()
	facts := []CanonicalFact{blockersFact(a, SourceAvailable, intp(5), nil), blockersFact(b, SourceAvailable, intp(5), nil), blockersFact(c, SourceAvailable, intp(9), nil)}
	cohort := &Cohort{Kind: SubjectTeam, Members: []CohortMember{{Subject: a}, {Subject: b}, {Subject: c}}}
	interpretation := InterpretedQuestion{FactRequirements: []FactRequirement{{Kind: FactBlockers}}}
	existing := make([]DriverJudgment, 0, 48)
	for i := 0; i < 48; i++ {
		existing = append(existing, DriverJudgment{DriverID: fmt.Sprintf("existing-%d", i)})
	}
	got := teamBlockersDrivers(interpretation, cohort, facts, existing, nil, ItemAllocation{}, 0)
	if len(got.Drivers) != 2 || got.Drivers[0].AffectedSubjects[0].Label != "Gamma" || got.Drivers[1].AffectedSubjects[0].Label != "Alpha" {
		t.Fatalf("drivers = %+v", got.Drivers)
	}
	if !strings.Contains(got.Limitation, "2 of 3 teams") || got.Event.SkippedBudget != 1 {
		t.Fatalf("limitation = %q event = %+v", got.Limitation, got.Event)
	}
}

func TestTeamCohortBlockersClientModeCarriesCandidates(t *testing.T) {
	a, _, _ := teamsAB()
	cohort := &Cohort{Kind: SubjectTeam, Members: []CohortMember{{Subject: a}}}
	got := teamBlockersDrivers(InterpretedQuestion{FactRequirements: []FactRequirement{{Kind: FactBlockers}}}, cohort, []CanonicalFact{blockersFact(a, SourceAvailable, intp(7), nil)}, nil, nil, ItemAllocation{}, 0)
	candidates := teamBlockersDriverCandidates(got.Drivers)
	if len(candidates) != 1 || !strings.HasSuffix(candidates[0].DriverID, "-candidate") || len(candidates[0].ClaimedFactIDs) != 0 {
		t.Fatalf("candidates = %+v", candidates)
	}
}

func TestNonBlockersQuestionServesNoTeamBlockersDrivers(t *testing.T) {
	a, _, _ := teamsAB()
	cohort := &Cohort{Kind: SubjectTeam, Members: []CohortMember{{Subject: a}}}
	got := teamBlockersDrivers(InterpretedQuestion{FactRequirements: []FactRequirement{{Kind: FactHealth}}}, cohort, []CanonicalFact{blockersFact(a, SourceAvailable, intp(7), nil)}, nil, nil, ItemAllocation{}, 0)
	if len(got.Drivers) != 0 {
		t.Fatalf("drivers = %+v", got.Drivers)
	}
}

func TestTeamBlockersDriversSpendOnlyWhatNarrationLeftOfTheGrant(t *testing.T) {
	a, b, c := teamsAB()
	cohort := &Cohort{Kind: SubjectTeam, Members: []CohortMember{{Subject: a}, {Subject: b}, {Subject: c}}}
	facts := []CanonicalFact{blockersFact(a, SourceAvailable, intp(5), nil), blockersFact(b, SourceAvailable, intp(6), nil), blockersFact(c, SourceAvailable, intp(7), nil)}
	interpretation := InterpretedQuestion{FactRequirements: []FactRequirement{{Kind: FactBlockers}}}
	allocation := AllocateItems(AnswerPlan{Budget: AnswerPlanBudget{MaxItems: 30}}, 0, 0)
	if !allocation.InForce() || allocation.NarrationGrant < 4 {
		t.Fatalf("allocation = %+v, want a grant of at least 4", allocation)
	}
	free := teamBlockersDrivers(interpretation, cohort, facts, nil, nil, allocation, 0)
	spent := allocation.NarrationGrant - 2
	tight := teamBlockersDrivers(interpretation, cohort, facts, nil, nil, allocation, spent)
	if got := (len(tight.Drivers) + len(tight.Claims)) + spent; got > allocation.NarrationGrant {
		t.Fatalf("items %d (narration %d + blockers %d) exceed the grant %d", got, spent, len(tight.Drivers)+len(tight.Claims), allocation.NarrationGrant)
	}
	if len(tight.Drivers) >= len(free.Drivers) && len(free.Drivers) > 1 {
		t.Fatalf("tight=%d free=%d, want the narration spend to reduce the blockers drivers", len(tight.Drivers), len(free.Drivers))
	}
}

func TestTeamBlockersDriversSkipATeamAlreadyExplainedByABlockersDriver(t *testing.T) {
	a, b, _ := teamsAB()
	cohort := &Cohort{Kind: SubjectTeam, Members: []CohortMember{{Subject: a}, {Subject: b}}}
	facts := []CanonicalFact{blockersFact(a, SourceAvailable, intp(5), nil), blockersFact(b, SourceAvailable, intp(6), nil)}
	existing := []DriverJudgment{{DriverID: "model-minted-driver", Category: "blockers", AffectedSubjects: []SubjectRef{a}}}
	got := teamBlockersDrivers(InterpretedQuestion{FactRequirements: []FactRequirement{{Kind: FactBlockers}}}, cohort, facts, existing, nil, ItemAllocation{}, 0)
	if len(got.Drivers) != 1 || got.Drivers[0].AffectedSubjects[0].Label != "Beta" || got.Event.SkippedDuplicate != 1 {
		t.Fatalf("drivers=%+v event=%+v", got.Drivers, got.Event)
	}
}

func TestTeamBlockersPartialTitleDoesNotRestateTheProjectSubcountAsExact(t *testing.T) {
	a, _, _ := teamsAB()
	extra := map[string]FactValue{"project_blocked_work_items_current": IntegerFactValue(4), "repository_link_basis": StringFactValue("heuristic")}
	cohort := &Cohort{Kind: SubjectTeam, Members: []CohortMember{{Subject: a}}}
	got := teamBlockersDrivers(InterpretedQuestion{FactRequirements: []FactRequirement{{Kind: FactBlockers}}}, cohort, []CanonicalFact{blockersFact(a, SourceTruncated, intp(12), extra)}, nil, nil, ItemAllocation{}, 0)
	if title := got.Drivers[0].Title; title != "At least 12 blocked work items (partial read)" {
		t.Fatalf("title = %q", title)
	}
}
