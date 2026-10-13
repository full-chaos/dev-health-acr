package contextfabric

import (
	"strings"
	"testing"
)

func projectStatusFacts(project SubjectRef) []CanonicalFact {
	mk := func(kind FactKind, ref string, fields map[string]FactValue) CanonicalFact {
		return CanonicalFact{Kind: kind, Subject: project, Fields: fields, EvidenceRefIDs: []string{ref}, SourceState: SourceAvailable, Source: "ops", SourceVersion: "v1"}
	}
	return []CanonicalFact{
		mk(FactStatus, "evidence_ps_status", map[string]FactValue{"status": StringFactValue("in_progress")}),
		mk(FactWorkload, "evidence_ps_workload", map[string]FactValue{"backlog_size": IntegerFactValue(40)}),
		mk(FactReadiness, "evidence_ps_readiness", map[string]FactValue{"estimated_count": IntegerFactValue(18), "backlog_size": IntegerFactValue(40)}),
		mk(FactHealth, "evidence_ps_health", map[string]FactValue{"severity": StringFactValue("medium")}),
	}
}

func projectStatusRig(t *testing.T, requirements []FactRequirement, facts func(SubjectRef) []CanonicalFact) *clientRig {
	return newClientRig(t, func(r *clientRig) {
		r.facts = facts(r.project)
		r.runtime.interpreted = InterpretedQuestion{
			Shape: ShapeSingleSubject, RequestedJudgment: "status", SubjectTerms: []string{"Ask Dev"},
			TimeContext: TimeContext{Axis: TemporalCurrent}, FactRequirements: requirements,
		}
		draft := bootstrapDraft(r.project)
		draft.Status, draft.Drivers, draft.ClaimedFacts = InvestigationPartial, []DriverJudgment{}, []ClaimedFact{}
		draft.EvidenceRefIDs = []string{}
		r.runtime.draft = draft
	})
}

var projectStatusRequirements = []FactRequirement{{Kind: FactStatus}, {Kind: FactWorkload}, {Kind: FactReadiness}, {Kind: FactHealth}}

func driverTitles(drivers []DriverJudgment) string {
	titles := make([]string, 0, len(drivers))
	for _, d := range drivers {
		titles = append(titles, d.Title)
	}
	return strings.Join(titles, "|")
}

func TestProjectStatusServerModeServesStatusFactsAsDrivers(t *testing.T) {
	rig := projectStatusRig(t, projectStatusRequirements, projectStatusFacts)
	result, _, err := rig.investigate(serverRequest())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := driverTitles(result.Drivers), "Backlog: 40 items|Estimate coverage: 18 of 40 items estimated|Risk severity: medium"; got != want {
		t.Errorf("drivers = %q, want %q", got, want)
	}
	if len(result.ClaimedFacts) != 3 {
		t.Errorf("claimed facts = %d, want 3", len(result.ClaimedFacts))
	}
	if !strings.Contains(result.DirectJudgment, "Principal driver: Backlog: 40 items") {
		t.Errorf("judgment = %q", result.DirectJudgment)
	}
}

func TestProjectStatusClientModeResultCarriesStatusDrivers(t *testing.T) {
	rig := projectStatusRig(t, projectStatusRequirements, projectStatusFacts)
	result, bundle, err := rig.investigate(clientRequest())
	if err != nil {
		t.Fatal(err)
	}
	if bundle == nil {
		t.Fatal("no synthesis input")
	}
	if len(rig.encoded) == 0 || strings.Count(string(rig.encoded[0]), "project-status-") != 3 {
		t.Errorf("synthesis_input carries %d project status driver candidates, want 3", strings.Count(string(rig.encoded[0]), "project-status-"))
	}
	if len(result.Drivers) != 3 || len(result.ClaimedFacts) != 3 {
		t.Errorf("drivers=%d claims=%d, want 3 and 3", len(result.Drivers), len(result.ClaimedFacts))
	}
}

func TestProjectStatusMissingKindServesNoDriver(t *testing.T) {
	rig := projectStatusRig(t, projectStatusRequirements, func(p SubjectRef) []CanonicalFact {
		facts := projectStatusFacts(p)
		facts[1].SourceState = SourceNoData
		facts[3].Fields = map[string]FactValue{"severity": {Null: true}}
		return facts
	})
	result, _, err := rig.investigate(serverRequest())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := driverTitles(result.Drivers), "Estimate coverage: 18 of 40 items estimated"; got != want {
		t.Errorf("drivers = %q, want %q", got, want)
	}
}

func TestProjectNonStatusQuestionServesNoStatusDrivers(t *testing.T) {
	rig := projectStatusRig(t, []FactRequirement{{Kind: FactWorkload}, {Kind: FactReadiness}}, projectStatusFacts)
	result, _, err := rig.investigate(serverRequest())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Drivers) != 0 {
		t.Errorf("drivers = %q, want none", driverTitles(result.Drivers))
	}
}

func TestProjectStatusDriversStayUnderTheDefaultItemBudget(t *testing.T) {
	project := acceptanceProject()
	allocation := AllocateItems(AnswerPlan{Budget: AnswerPlanBudget{MaxItems: 30}}, 0, 0)
	interpretation := InterpretedQuestion{FactRequirements: projectStatusRequirements}
	drivers, claims, _ := projectStatusDrivers(interpretation, []SubjectRef{project}, projectStatusFacts(project), Coverage{}, nil, nil, allocation)
	if len(drivers)+len(claims) > allocation.NarrationGrant {
		t.Errorf("items %d exceed narration grant %d", len(drivers)+len(claims), allocation.NarrationGrant)
	}
}

func TestTeamStatusQuestionServesNoProjectStatusDrivers(t *testing.T) {
	team := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:CHAOS", Label: "Fullchaos"}
	drivers, claims, _ := projectStatusDrivers(InterpretedQuestion{FactRequirements: projectStatusRequirements}, []SubjectRef{team}, projectStatusFacts(team), Coverage{}, nil, nil, ItemAllocation{})
	if len(drivers) != 0 || len(claims) != 0 {
		t.Errorf("team drivers=%d claims=%d, want 0", len(drivers), len(claims))
	}
}

func TestProjectStatusCandidatesKeepTheTrueCategoryAndEvidenceWithoutClaims(t *testing.T) {
	project := acceptanceProject()
	drivers, _, _ := projectStatusDrivers(InterpretedQuestion{FactRequirements: projectStatusRequirements}, []SubjectRef{project}, projectStatusFacts(project), Coverage{}, nil, nil, ItemAllocation{})
	candidates := projectStatusDriverCandidates(drivers)
	if len(candidates) != 3 {
		t.Fatalf("candidates = %d, want 3", len(candidates))
	}
	for index, candidate := range candidates {
		if len(candidate.ClaimedFactIDs) != 0 {
			t.Errorf("candidate %q holds claim ids %v", candidate.DriverID, candidate.ClaimedFactIDs)
		}
		if candidate.Category != drivers[index].Category || candidate.Category == "narrative" {
			t.Errorf("candidate %q category = %q, want the observation's %q", candidate.DriverID, candidate.Category, drivers[index].Category)
		}
		if candidate.Derivation != DerivationCanonicalStructured || candidate.EpistemicStatus != EpistemicObserved {
			t.Errorf("candidate %q derivation/epistemic = %q/%q", candidate.DriverID, candidate.Derivation, candidate.EpistemicStatus)
		}
		if len(candidate.EvidenceRefIDs) == 0 {
			t.Errorf("candidate %q carries no evidence ref", candidate.DriverID)
		}
	}
}

func TestProjectStatusCandidatesNeverReachTheServedResult(t *testing.T) {
	rig := projectStatusRig(t, projectStatusRequirements, projectStatusFacts)
	result, _, err := rig.investigate(clientRequest())
	if err != nil {
		t.Fatal(err)
	}
	for _, driver := range result.Drivers {
		if strings.HasSuffix(driver.DriverID, "-candidate") {
			t.Errorf("served driver %q is a synthesis-input candidate", driver.DriverID)
		}
	}
	claims := map[string]bool{}
	for _, claim := range result.ClaimedFacts {
		claims[claim.ClaimID] = true
	}
	for _, driver := range result.Drivers {
		for _, id := range driver.ClaimedFactIDs {
			if !claims[id] {
				t.Errorf("served driver %q holds unresolved claim id %q", driver.DriverID, id)
			}
		}
	}
}

func TestProjectStatusServerModeRecomposesCurrentStateFromTheMintedClaims(t *testing.T) {
	rig := projectStatusRig(t, projectStatusRequirements, projectStatusFacts)
	result, _, err := rig.investigate(serverRequest())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.CurrentState, "No canonical facts were observed") || !strings.Contains(result.CurrentState, "backlog size: 40") {
		t.Errorf("current_state = %q, want it to restate the minted claims", result.CurrentState)
	}
}

func TestProjectStatusDecisionTelemetryNamesWhatWasOmitted(t *testing.T) {
	rig := projectStatusRig(t, projectStatusRequirements, func(p SubjectRef) []CanonicalFact {
		facts := projectStatusFacts(p)
		facts[1].SourceState = SourceNoData
		return facts
	})
	if _, _, err := rig.investigate(serverRequest()); err != nil {
		t.Fatal(err)
	}
	if len(rig.telemetry.projectStatusDrivers) != 1 {
		t.Fatalf("events = %#v, want exactly one", rig.telemetry.projectStatusDrivers)
	}
	event := rig.telemetry.projectStatusDrivers[0]
	if event.Emitted != 2 || len(event.Missing) != 1 || event.Missing[0] != FactWorkload {
		t.Errorf("event = %#v, want 2 emitted and workload missing", event)
	}
	if len(event.MissingStates) != 1 || event.MissingStates[0] != string(SourceNoData) {
		t.Errorf("missing states = %v, want [no_data]", event.MissingStates)
	}
}

func TestProjectStatusClaimIDCollisionSkipsTheDriverInsteadOfInvalidatingTheAnswer(t *testing.T) {
	project := acceptanceProject()
	interpretation := InterpretedQuestion{FactRequirements: projectStatusRequirements}
	first, claims, _ := projectStatusDrivers(interpretation, []SubjectRef{project}, projectStatusFacts(project), Coverage{}, nil, nil, ItemAllocation{})
	if len(first) != 3 {
		t.Fatalf("drivers = %d, want 3", len(first))
	}
	drivers, _, event := projectStatusDrivers(interpretation, []SubjectRef{project}, projectStatusFacts(project), Coverage{}, nil, claims[:1], ItemAllocation{})
	if len(drivers) != 2 || event.SkippedIDClash != 1 {
		t.Errorf("drivers=%d clash=%d, want 2 and 1", len(drivers), event.SkippedIDClash)
	}
}

func TestProjectStatusDriversAreBoundByTheNarrationGrant(t *testing.T) {
	project := acceptanceProject()
	allocation := AllocateItems(AnswerPlan{Budget: AnswerPlanBudget{MaxItems: 12}}, 0, 0)
	if allocation.NarrationGrant != 4 {
		t.Fatalf("narration grant = %d, want 4", allocation.NarrationGrant)
	}
	drivers, claims, event := projectStatusDrivers(InterpretedQuestion{FactRequirements: projectStatusRequirements}, []SubjectRef{project}, projectStatusFacts(project), Coverage{}, nil, nil, allocation)
	if len(drivers) != 2 || len(claims) != 2 || event.SkippedBudget != 1 {
		t.Errorf("drivers=%d claims=%d skipped_budget=%d, want 2, 2 and 1", len(drivers), len(claims), event.SkippedBudget)
	}
}
