package contextfabric

import (
	"strings"
	"testing"
)

// Quick answer first (chris 2026-10-30: "always lean towards whatever the
// quick answer is with details afterwards"): the lead prose fields open with
// the quick answer, then the status sentence, then the details.

func quickAnswerDriver(id, title string, affected ...SubjectRef) DriverJudgment {
	return DriverJudgment{DriverID: id, Standing: DriverPrincipal, Category: "health", Title: title, AffectedSubjects: affected}
}

func TestPrincipalDriversInAnswerOrderKeepsServedOrderNotDriverID(t *testing.T) {
	drivers := []DriverJudgment{
		quickAnswerDriver("driver_z", "Z first in served order"),
		quickAnswerDriver("driver_a", "A second in served order"),
		{DriverID: "driver_contributing", Standing: DriverContributing, Title: "Not principal"},
		quickAnswerDriver("driver_untitled", "  "),
	}
	if got, want := driverTitles(principalDriversInAnswerOrder(drivers, nil)), "Z first in served order|A second in served order"; got != want {
		t.Errorf("order = %q, want %q (served order, principal and titled only)", got, want)
	}
}

func TestPrincipalDriversInAnswerOrderPutsEngineRanksFirstByRank(t *testing.T) {
	second := quickAnswerDriver("project-status-1-workload", "Forecast backlog")
	second.Rank = 2
	first := quickAnswerDriver("project-status-0-open-items", "Open items")
	first.Rank = 1
	drivers := []DriverJudgment{quickAnswerDriver("driver_model", "Model driver"), second, first}
	if got, want := driverTitles(principalDriversInAnswerOrder(drivers, nil)), "Open items|Forecast backlog|Model driver"; got != want {
		t.Errorf("order = %q, want %q (ranked by rank, then unranked)", got, want)
	}
}

func TestPrincipalDriversInAnswerOrderLeadsARankedCohortWithItsTopMember(t *testing.T) {
	top := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:top", Label: "Top"}
	other := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:other", Label: "Other"}
	drivers := []DriverJudgment{
		quickAnswerDriver("driver_other", "Other has a gap", other),
		quickAnswerDriver("driver_top", "Top needs attention", top),
	}
	ranked := &Cohort{Kind: SubjectTeam, Members: []CohortMember{
		{Subject: other, Rank: 1, RankingComputed: true, AttentionRank: 2},
		{Subject: top, Rank: 2, RankingComputed: true, AttentionRank: 1},
	}}
	if got, want := driverTitles(principalDriversInAnswerOrder(drivers, ranked)), "Top needs attention|Other has a gap"; got != want {
		t.Errorf("ranked cohort order = %q, want %q (the attention-rank-1 member first)", got, want)
	}
	sameIDOtherKind := SubjectRef{Kind: SubjectProject, CanonicalID: top.CanonicalID, Label: "Not the team"}
	kindless := []DriverJudgment{quickAnswerDriver("driver_other", "Other has a gap", other), quickAnswerDriver("driver_project", "About a project", sameIDOtherKind)}
	if got, want := driverTitles(principalDriversInAnswerOrder(kindless, ranked)), "Other has a gap|About a project"; got != want {
		t.Errorf("a subject of another kind with the top member's id = %q, want %q (a member is matched by kind and id)", got, want)
	}
	unranked := &Cohort{Kind: SubjectTeam, Members: []CohortMember{{Subject: other, Rank: 1}, {Subject: top, Rank: 2}}}
	if got, want := driverTitles(principalDriversInAnswerOrder(drivers, unranked)), "Other has a gap|Top needs attention"; got != want {
		t.Errorf("unranked cohort order = %q, want %q (served order: no ranking ran)", got, want)
	}
	stale := quickAnswerDriver("driver_top_ranked", "Ranked first", other)
	stale.Rank = 1
	if got := driverTitles(principalDriversInAnswerOrder(append([]DriverJudgment{drivers[1]}, stale), ranked)); got != "Ranked first|Top needs attention" {
		t.Errorf("engine rank versus cohort top = %q, want the engine rank first", got)
	}
}

func TestComposeLeadProseStatesTheQuickAnswerFirstOnlyForAnAnswer(t *testing.T) {
	drivers := []DriverJudgment{quickAnswerDriver("driver_z", "Quick answer"), quickAnswerDriver("driver_a", "Detail")}
	partial := statusSentence(InvestigationPartial, SubjectResolution{})
	for _, row := range []struct {
		name   string
		status InvestigationStatus
		label  string
		want   string
	}{
		{"partial with details", InvestigationPartial, "Principal driver: ", "Quick answer. " + partial + " Principal driver: Detail."},
		{"complete, no details label", InvestigationComplete, "", "Quick answer. " + statusSentence(InvestigationComplete, SubjectResolution{})},
		{"degraded", InvestigationDegraded, "Principal driver(s): ", "Quick answer. " + statusSentence(InvestigationDegraded, SubjectResolution{}) + " Principal driver(s): Detail."},
		{"no_match keeps the status first", InvestigationNoMatch, "Principal driver: ", statusSentence(InvestigationNoMatch, SubjectResolution{}) + " Principal driver: Quick answer; Detail."},
		{"clarification keeps the status first", InvestigationClarificationRequired, "Principal driver: ", statusSentence(InvestigationClarificationRequired, SubjectResolution{}) + " Principal driver: Quick answer; Detail."},
	} {
		if got := composeLeadProse(row.status, drivers, nil, SubjectResolution{}, row.label); got != row.want {
			t.Errorf("%s: got %q, want %q", row.name, got, row.want)
		}
	}
	if got := composeLeadProse(InvestigationPartial, nil, nil, SubjectResolution{}, "Principal driver: "); got != partial {
		t.Errorf("no drivers: got %q, want the status sentence alone", got)
	}
}

func TestLeadDriverSentenceKeepsTheQualifierInsideOneSentence(t *testing.T) {
	for _, row := range []struct {
		title string
		state SourceState
		want  string
	}{
		{"Open items: 3 (2026-10-30)", "", "Open items: 3 (2026-10-30)."},
		{"Fullchaos is the sole qualified team.", "", "Fullchaos is the sole qualified team."},
		{"Fullchaos has elevated health risk.", SourceTruncated, "Fullchaos has elevated health risk (partial: the source read was truncated)."},
		{"Is it ready?", "", "Is it ready?"},
		{"Forecast backlog: 1 item", SourceStale, "Forecast backlog: 1 item (stale: the source read is out of date)."},
	} {
		if got := leadDriverSentence(DriverJudgment{Title: row.title, SourceState: row.state}); got != row.want {
			t.Errorf("%q/%q: got %q, want %q", row.title, row.state, got, row.want)
		}
	}
}

func TestProjectStatusDriversCarryTheEngineRankInObservationOrder(t *testing.T) {
	project := acceptanceProject()
	drivers, _, _ := projectStatusDrivers(InterpretedQuestion{FactRequirements: projectStatusRequirements}, []SubjectRef{project}, projectStatusFacts(project), Coverage{}, nil, nil, ItemAllocation{})
	for index, driver := range drivers {
		if driver.Rank != index+1 {
			t.Errorf("driver %q rank = %d, want %d", driver.DriverID, driver.Rank, index+1)
		}
	}
}

// TestProjectStatusAnswerLeadsWithTheOpenItemsAheadOfTheModelDrivers runs the
// engine with a model draft that has a principal driver of its own: the served
// lead prose opens with the open items, the engine's quick answer, and states
// the model's driver among the details.
func TestProjectStatusAnswerLeadsWithTheOpenItemsAheadOfTheModelDrivers(t *testing.T) {
	rig := projectStatusRig(t, projectStatusRequirements, projectStatusFacts)
	model := DriverJudgment{
		DriverID: "driver-a-model-health", Standing: DriverPrincipal, Category: "health",
		Title: "Health severity is medium", Summary: "The project's health severity is medium.",
		AffectedSubjects: []SubjectRef{rig.project}, EvidenceRefIDs: []string{"evidence_ps_health"},
		ClaimedFactIDs: []string{"claim-model-health"},
		Derivation:     DerivationCanonicalStructured, EpistemicStatus: EpistemicObserved, Confidence: 0.9, Current: true,
	}
	rig.runtime.draft.Drivers = []DriverJudgment{model}
	rig.runtime.draft.ClaimedFacts = []ClaimedFact{{ClaimID: "claim-model-health", Kind: FactHealth, Subject: rig.project, Field: "severity", Value: convertFactValueScalar(StringFactValue("medium"))}}
	rig.runtime.draft.EvidenceRefIDs = []string{"evidence_ps_health"}
	result, _, err := rig.investigate(serverRequest())
	if err != nil {
		t.Fatal(err)
	}
	lead := "Open items: 40 (2026-10-30). " + statusSentence(result.Status, result.SubjectResolution)
	for name, text := range map[string]string{"direct_judgment": result.DirectJudgment, "deterministic_answer": result.DeterministicAnswer} {
		if !strings.HasPrefix(text, lead) {
			t.Errorf("%s = %q, want it to open with %q", name, text, lead)
		}
		if !strings.Contains(text, "Health severity is medium") {
			t.Errorf("%s = %q, want the model's driver among the details", name, text)
		}
	}
}

// TestSingleSubjectAnswerLeadsWithTheFirstServedDriverNotTheLowestID: with no
// engine rank, the quick answer is the synthesis's first principal driver,
// never the one whose id sorts first.
func TestSingleSubjectAnswerLeadsWithTheFirstServedDriverNotTheLowestID(t *testing.T) {
	rig := projectStatusRig(t, []FactRequirement{{Kind: FactHealth}}, projectStatusFacts)
	driver := func(id, title string) DriverJudgment {
		return DriverJudgment{
			DriverID: id, Standing: DriverPrincipal, Category: "health", Title: title, Summary: title + ".",
			AffectedSubjects: []SubjectRef{rig.project}, EvidenceRefIDs: []string{"evidence_ps_health"},
			ClaimedFactIDs: []string{"claim-model-health"},
			Derivation:     DerivationCanonicalStructured, EpistemicStatus: EpistemicObserved, Confidence: 0.9, Current: true,
		}
	}
	rig.runtime.draft.Drivers = []DriverJudgment{driver("driver_z_first", "Stated first"), driver("driver_a_second", "Stated second")}
	rig.runtime.draft.ClaimedFacts = []ClaimedFact{{ClaimID: "claim-model-health", Kind: FactHealth, Subject: rig.project, Field: "severity", Value: convertFactValueScalar(StringFactValue("medium"))}}
	rig.runtime.draft.EvidenceRefIDs = []string{"evidence_ps_health"}
	result, _, err := rig.investigate(serverRequest())
	if err != nil {
		t.Fatal(err)
	}
	want := "Stated first. " + statusSentence(result.Status, result.SubjectResolution) + " Principal driver: Stated second."
	if result.DirectJudgment != want {
		t.Errorf("direct_judgment = %q, want %q", result.DirectJudgment, want)
	}
}

// TestAStatusFloorKeepsTheCountLeadAndTheNarratedForm: a floor that changes the
// served status recomposes the lead prose for it, keeping the count sentence
// the server put first and, for a narrated cohort, the form with no driver list.
func TestAStatusFloorKeepsTheCountLeadAndTheNarratedForm(t *testing.T) {
	drivers := []DriverJudgment{quickAnswerDriver("driver_lead", "Lead"), quickAnswerDriver("driver_detail", "Detail")}
	partial := statusSentence(InvestigationPartial, SubjectResolution{})
	count := "Counted 3 teams."

	counted := InvestigationResult{Status: InvestigationNoMatch, Drivers: drivers}
	counted.DirectJudgment = leadWithCountSentence(composeDirectJudgmentFrom(counted.Status, drivers, nil, counted.SubjectResolution), count, directJudgmentMaxLength)
	counted.DeterministicAnswer = leadWithCountSentence(composeDeterministicAnswerFrom(counted.Status, drivers, nil, nil, counted.SubjectResolution), count, deterministicAnswerMaxLength)
	floorNoMatchTo(&counted, InvestigationPartial)
	if want := count + " Lead. " + partial + " Principal driver: Detail."; counted.DirectJudgment != want {
		t.Errorf("floored direct_judgment = %q, want %q", counted.DirectJudgment, want)
	}
	if want := count + " Lead. " + partial + " Principal driver(s): Detail."; counted.DeterministicAnswer != want {
		t.Errorf("floored deterministic_answer = %q, want %q", counted.DeterministicAnswer, want)
	}

	narrated := InvestigationResult{Status: InvestigationComplete, Drivers: drivers}
	narrated.DirectJudgment, narrated.DeterministicAnswer = recomposeCohortAnswerNarrative(narrated.Status, drivers, nil, narrated.SubjectResolution)
	floorNoMatchTo(&narrated, InvestigationPartial)
	for name, text := range map[string]string{"direct_judgment": narrated.DirectJudgment, "deterministic_answer": narrated.DeterministicAnswer} {
		if want := "Lead. " + partial; text != want {
			t.Errorf("floored narrated %s = %q, want %q (no driver list)", name, text, want)
		}
	}

	fixed := InvestigationResult{Status: InvestigationNoMatch, Drivers: drivers, DirectJudgment: "A client-written answer.", DeterministicAnswer: "A client-written answer."}
	floorNoMatchTo(&fixed, InvestigationPartial)
	if want := composeDirectJudgmentFrom(InvestigationPartial, drivers, nil, SubjectResolution{}); fixed.DirectJudgment != want {
		t.Errorf("a head of neither form = %q, want it composed afresh %q", fixed.DirectJudgment, want)
	}
}
