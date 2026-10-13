package contextfabric

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/answerprojection"
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

func TestTeamCohortClientSynthesisInputCarriesTheBlockersDriverCandidate(t *testing.T) {
	a, b, _ := teamsAB()
	rig := newClientRig(t, func(r *clientRig) {
		r.facts = []CanonicalFact{blockersFact(a, SourceAvailable, intp(30), nil), blockersFact(b, SourceAvailable, intp(2), nil)}
		r.runtime.interpreted = InterpretedQuestion{
			Shape: ShapeDiscoveredCohort, RequestedJudgment: "teams_blocked",
			TimeContext: TimeContext{Axis: TemporalCurrent}, FactRequirements: []FactRequirement{{Kind: FactBlockers}},
		}
		draft := bootstrapDraft(r.project)
		draft.Status, draft.Drivers, draft.ClaimedFacts = InvestigationPartial, []DriverJudgment{}, []ClaimedFact{}
		draft.EvidenceRefIDs = []string{}
		r.runtime.draft = draft
		r.graph.resolution = SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}}
		r.graph.context.Cohort = &Cohort{Kind: SubjectTeam, Rationale: "kind census match", Members: []CohortMember{
			{Subject: a, Rank: 1, InclusionReasons: []string{"matched"}, EvidenceRefIDs: []string{"evidence_team_roster"}},
			{Subject: b, Rank: 2, InclusionReasons: []string{"matched"}, EvidenceRefIDs: []string{"evidence_team_roster"}},
		}}
	})
	if _, _, err := rig.investigate(clientRequest()); err != nil {
		t.Fatal(err)
	}
	if len(rig.encoded) == 0 {
		t.Fatal("no synthesis input was encoded")
	}
	var input struct {
		DriverCandidates []DriverJudgment `json:"driver_candidates"`
	}
	if err := json.Unmarshal(rig.encoded[0], &input); err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, c := range input.DriverCandidates {
		if c.Category == "blockers" {
			titles = append(titles, c.Title)
			if len(c.ClaimedFactIDs) != 0 || !strings.HasSuffix(c.DriverID, "-candidate") || len(c.EvidenceRefIDs) == 0 {
				t.Errorf("candidate = %+v", c)
			}
		}
	}
	if got, want := strings.Join(titles, "|"), "Blocked work items: 30|Blocked work items: 2"; got != want {
		t.Fatalf("blockers candidates = %q, want %q", got, want)
	}
}

func TestTeamCohortProjectionKeepsTheTeamsWithTheMostBlockedWork(t *testing.T) {
	teams := make([]SubjectRef, 0, 14)
	facts := make([]CanonicalFact, 0, 14)
	for i := 0; i < 14; i++ {
		team := SubjectRef{Kind: SubjectTeam, CanonicalID: fmt.Sprintf("team:%02d", (i*5)%14), Label: fmt.Sprintf("Team %02d", (i*5)%14)}
		teams = append(teams, team)
		facts = append(facts, blockersFact(team, SourceAvailable, intp(int64(100-(i*5)%14)), nil))
	}
	result := investigateTeamBlockers(t, teams, facts)
	projection := answerprojection.Project(result, answerprojection.DefaultBudget)
	if len(projection.PrincipalDrivers) != answerprojection.DefaultBudget.MaxDrivers {
		t.Fatalf("projected drivers = %d, want %d", len(projection.PrincipalDrivers), answerprojection.DefaultBudget.MaxDrivers)
	}
	for i, driver := range projection.PrincipalDrivers {
		if want := fmt.Sprintf("Blocked work items: %d", 100-i); driver.Title != want {
			t.Fatalf("projected driver %d = %q, want %q", i, driver.Title, want)
		}
	}
}

func TestTeamBlockersLimitationCountsTeamsAlreadyCoveredAndStaysSilentWithoutACut(t *testing.T) {
	a, b, _ := teamsAB()
	cohort := &Cohort{Kind: SubjectTeam, Members: []CohortMember{{Subject: a}, {Subject: b}}}
	facts := []CanonicalFact{blockersFact(a, SourceAvailable, intp(5), nil), blockersFact(b, SourceAvailable, intp(6), nil)}
	existing := []DriverJudgment{{DriverID: "model-minted-driver", Category: "blockers", AffectedSubjects: []SubjectRef{a}}}
	got := teamBlockersDrivers(InterpretedQuestion{FactRequirements: []FactRequirement{{Kind: FactBlockers}}}, cohort, facts, existing, nil, ItemAllocation{}, 0)
	if got.Limitation != "" {
		t.Fatalf("limitation = %q, want none without a budget cut", got.Limitation)
	}
}

func TestTeamBlockersCoveredCheckKeysOnSubjectKindAndID(t *testing.T) {
	a, _, _ := teamsAB()
	cohort := &Cohort{Kind: SubjectTeam, Members: []CohortMember{{Subject: a}}}
	facts := []CanonicalFact{blockersFact(a, SourceAvailable, intp(5), nil)}
	sameIDOtherKind := SubjectRef{Kind: SubjectProject, CanonicalID: a.CanonicalID, Label: "Project"}
	existing := []DriverJudgment{{DriverID: "project-blockers", Category: "blockers", AffectedSubjects: []SubjectRef{sameIDOtherKind}}}
	got := teamBlockersDrivers(InterpretedQuestion{FactRequirements: []FactRequirement{{Kind: FactBlockers}}}, cohort, facts, existing, nil, ItemAllocation{}, 0)
	if len(got.Drivers) != 1 {
		t.Fatalf("drivers = %d, want the team driver kept", len(got.Drivers))
	}
}

func TestTeamBlockersDriverIDStaysTheSameWhenItsCountRankChanges(t *testing.T) {
	a, b, _ := teamsAB()
	cohort := &Cohort{Kind: SubjectTeam, Members: []CohortMember{{Subject: a}, {Subject: b}}}
	interpretation := InterpretedQuestion{FactRequirements: []FactRequirement{{Kind: FactBlockers}}}
	idsAndRanks := func(countA, countB int64) map[string][2]int {
		got := teamBlockersDrivers(interpretation, cohort, []CanonicalFact{blockersFact(a, SourceAvailable, intp(countA), nil), blockersFact(b, SourceAvailable, intp(countB), nil)}, nil, nil, ItemAllocation{}, 0)
		out := map[string][2]int{}
		for _, d := range got.Drivers {
			out[d.AffectedSubjects[0].CanonicalID+"|"+d.DriverID] = [2]int{d.Rank, 0}
		}
		return out
	}
	before, after := idsAndRanks(9, 3), idsAndRanks(3, 9)
	for key, rank := range before {
		if _, same := after[key]; !same {
			t.Fatalf("driver id changed with the count rank: %q not in %v", key, after)
		}
		if after[key] == rank {
			t.Fatalf("rank did not follow the counts for %q: %v -> %v", key, rank, after[key])
		}
	}
}

func TestServedDriverRankIsTheServersEvenWhenASynthesizerStatesOne(t *testing.T) {
	a, _, _ := teamsAB()
	cohort := &Cohort{Kind: SubjectTeam, Rationale: "kind census match", Members: []CohortMember{{Subject: a, Rank: 1, InclusionReasons: []string{"matched"}, EvidenceRefIDs: []string{"evidence_team_roster"}}}}
	interpretation := InterpretedQuestion{
		Shape: ShapeDiscoveredCohort, RequestedJudgment: "teams_blocked",
		TimeContext: TimeContext{Axis: TemporalCurrent}, FactRequirements: []FactRequirement{{Kind: FactBlockers}},
	}
	graph := graphReaderStub{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
		context: GraphContext{Cohort: cohort, Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{},
			FactRequirements: []FactRequirement{}, EvidenceRefIDs: []string{"evidence_model_1"},
			Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}},
	}
	modelDriver := DriverJudgment{
		DriverID: "model_driver_0001", Standing: DriverContributing, Category: "relationship", Title: "A model driver", Summary: "A model driver.",
		AffectedSubjects: []SubjectRef{a}, EvidenceRefIDs: []string{"evidence_model_1"}, Derivation: DerivationRuleInferred,
		EpistemicStatus: EpistemicInferred, Confidence: 0.5, Current: true, Rank: 1,
	}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return interpretation, nil
		}),
		Graph: graph,
		Facts: factReaderFunc(func(_ context.Context, _ storage.Principal, _ CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{Facts: []CanonicalFact{blockersFact(a, SourceAvailable, intp(9), nil)},
				Coverage: Coverage{Sources: []SourceObservation{{Source: "canonical_fact:blockers", State: SourceAvailable}}, DegradedReasons: []string{}},
				Version:  "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{}}, nil
		}),
		Synthesizer: synthesizerFunc(func(_ context.Context, _ storage.Principal, _ SynthesisInput) (InvestigationResult, error) {
			return InvestigationResult{Status: InvestigationPartial, DirectJudgment: "This investigation is partial: some canonical or graph coverage was unavailable.",
				CurrentState: "x", DeterministicAnswer: "x", StrongestPressures: []string{}, Drivers: []DriverJudgment{modelDriver},
				RemainingWork: []Finding{}, ReadinessGaps: []Finding{}, Paths: []RelationshipPath{}, Conflicts: []Finding{}, Limitations: []string{},
				EvidenceRefIDs: []string{"evidence_model_1"}, ClaimedFacts: []ClaimedFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}, Warnings: []string{},
				Versions: VersionSet{Backend: "test", ProjectionVersion: "p", QueryVersion: "q", InterpretationVersion: "i", SynthesisVersion: "s"}}, nil
		}),
		Results: &resultStoreStub{}, Telemetry: &recordingTelemetry{},
	}, EngineOptions{ServiceVersion: "acr-test", Now: func() time.Time { return time.Unix(300, 0).UTC() }, NewResultID: func() string { return "result_rank0001" }})
	if err != nil {
		t.Fatal(err)
	}
	request := validInvestigationRequestWithConfirmedWindow()
	request.RequestID = "request_rank0001"
	request.Question = "which teams are blocked?"
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org-1"}, request)
	if err != nil {
		t.Fatal(err)
	}
	for _, driver := range result.Drivers {
		switch {
		case driver.DriverID == "model_driver_0001" && driver.Rank != 0:
			t.Errorf("a stated rank survived on the model's driver: %+v", driver)
		case driver.Category == "blockers" && driver.Rank != 1:
			t.Errorf("the server's blockers driver rank = %d, want 1", driver.Rank)
		}
	}
}
