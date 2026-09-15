package contextfabric

import (
	"encoding/json"
	"reflect"
	"testing"

	v1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func retainedRankingResult(t *testing.T) InvestigationResult {
	t.Helper()
	cohort := &Cohort{Kind: SubjectTeam, Complete: true, Members: []CohortMember{rankTestMember("one")}}
	facts := []CanonicalFact{investmentFact("one", balancedThemes(), 0.05), healthFact("one", "low"), deficiencyFact("one", "high"), readinessFact("one", 0.9), workloadFact("one", 8)}
	cohort, _, _ = RankCohort(cohort, facts, availableCoverage())
	derived := registryDeriver{}.DeriveRequirements(*rankingFrameOverACohort())
	requirements := PlanRequirementsFromDerived(derived)
	for _, r := range requirements {
		if r.Obligation == "ranking" {
			return InvestigationResult{Cohort: cohort, AnswerPlan: &v1.ContextFabricAnswerPlan{Requirements: []v1.ContextFabricPlanRequirement{r}}}
		}
	}
	t.Fatal("real derivation produced no ranking requirement")
	return InvestigationResult{}
}

func TestRetainedRankingQualificationDomain(t *testing.T) {
	cases := []struct {
		name    string
		edit    func(*InvestigationResult)
		want    bool
		outcome RequirementOutcome
	}{
		{"qualified", func(*InvestigationResult) {}, true, v1.ContextFabricRequirementSatisfied},
		{"provisional", func(r *InvestigationResult) { r.Cohort.Members[0].Outcome = CohortOutcomeProvisional }, true, v1.ContextFabricRequirementNarrowed},
		{"insufficient", func(r *InvestigationResult) {
			r.Cohort.Members[0].Outcome = CohortOutcomeInsufficientEvidence
			r.Cohort.Members[0].Score = nil
		}, true, v1.ContextFabricRequirementUnavailable},
		{"not_applicable_recorded", func(r *InvestigationResult) {
			r.Cohort.Members[0].Outcome = CohortOutcomeNotApplicable
			r.Cohort.Members[0].Score = nil
		}, true, v1.ContextFabricRequirementUnavailable},
		{"nil_cohort", func(r *InvestigationResult) { r.Cohort = nil }, false, ""},
		{"empty_members", func(r *InvestigationResult) { r.Cohort.Members = nil }, false, ""},
		{"wrong_cohort_kind", func(r *InvestigationResult) { r.Cohort.Kind = SubjectIncident }, false, ""},
		{"wrong_member_kind", func(r *InvestigationResult) { r.Cohort.Members[0].Subject.Kind = SubjectIncident }, false, ""},
		{"not_computed", func(r *InvestigationResult) { r.Cohort.Members[0].RankingComputed = false }, false, ""},
		{"absent_outcome", func(r *InvestigationResult) { r.Cohort.Members[0].Outcome = "" }, false, ""},
		{"unknown_outcome", func(r *InvestigationResult) { r.Cohort.Members[0].Outcome = "future" }, false, ""},
		{"not_applicable", func(r *InvestigationResult) { r.Cohort.Members[0].Outcome = CohortOutcomeNotApplicable }, false, ""},
		{"qualified_without_score", func(r *InvestigationResult) { r.Cohort.Members[0].Score = nil }, false, ""},
		{"provisional_without_score", func(r *InvestigationResult) {
			r.Cohort.Members[0].Outcome = CohortOutcomeProvisional
			r.Cohort.Members[0].Score = nil
		}, false, ""},
		{"insufficient_with_score", func(r *InvestigationResult) { r.Cohort.Members[0].Outcome = CohortOutcomeInsufficientEvidence }, false, ""},
		{"mixed_recorded_and_missing", func(r *InvestigationResult) { r.Cohort.Members = append(r.Cohort.Members, rankTestMember("two")) }, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer retainedRankingPanicFailsCell(t)
			result := retainedRankingResult(t)
			tc.edit(&result)
			before, _ := json.Marshal(result)
			got, events := AccountForRetainedRanking(result)
			if len(events) != 1 {
				t.Fatalf("accounting events=%+v", events)
			}
			e := events[0]
			if e.QualificationRecorded != tc.want || e.RowAdded != tc.want || e.ExistingRow || e.AssembledOutcome != tc.outcome {
				t.Fatalf("retained qualification decision=%+v; want admitted=%v outcome=%s", e, tc.want, tc.outcome)
			}
			if e.Qualified+e.Provisional+e.InsufficientEvidence+e.NotApplicable+e.UnrecordedMembers != e.RetainedMembers || e.Index != 1 || e.Total != 1 {
				t.Fatalf("retained counts or event bounds=%+v", e)
			}
			if tc.want {
				if len(got.Completeness.Outcomes) != 1 {
					t.Fatal("missing assembled row")
				}
			} else if len(got.Completeness.Outcomes) != 0 {
				t.Fatal("invented legacy account without qualification")
			}
			after, _ := json.Marshal(result)
			if string(before) != string(after) {
				t.Fatal("stored input changed")
			}
		})
	}
}

func TestRetainedRankingRequiresPublishedServedComputedRanking(t *testing.T) {
	for _, name := range []string{"no_plan", "no_requirements", "other_obligation", "read_requirement", "unavailable"} {
		t.Run(name, func(t *testing.T) {
			defer retainedRankingPanicFailsCell(t)
			r := retainedRankingResult(t)
			switch name {
			case "no_plan":
				r.AnswerPlan = nil
			case "no_requirements":
				r.AnswerPlan.Requirements = nil
			case "other_obligation":
				r.AnswerPlan.Requirements[0].Obligation = "count"
			case "read_requirement":
				r.AnswerPlan.Requirements[0].Kind = "read"
			case "unavailable":
				r.AnswerPlan.Requirements[0].Step = ""
				r.AnswerPlan.Requirements[0].Unavailable = "computed_population_absent"
			}
			got, events := AccountForRetainedRanking(r)
			if len(got.Completeness.Outcomes) != 0 || len(events) != 0 {
				t.Fatalf("accounted an ineligible stored plan: %+v %+v", got.Completeness.Outcomes, events)
			}
		})
	}
}

func TestRetainedRankingCarriesExistingRowsAndDoesNotAliasStorage(t *testing.T) {
	r := retainedRankingResult(t)
	r.Completeness.Outcomes = make([]RequirementOutcomeRow, 1, 8)
	r.Completeness.Outcomes[0] = RequirementOutcomeRow{Requirement: "health/member/team", Obligation: "health", Stage: v1.ContextFabricOutcomeStagePlanning, Outcome: v1.ContextFabricRequirementUnavailable}
	backing := r.Completeness.Outcomes[:cap(r.Completeness.Outcomes)]
	before := append([]RequirementOutcomeRow(nil), backing...)
	got, events := AccountForRetainedRanking(r)
	if !events[0].RowAdded || len(got.Completeness.Outcomes) != 2 {
		t.Fatal("missing detached append")
	}
	if !reflect.DeepEqual(backing, before) {
		t.Fatal("accounting overwrote stored backing capacity")
	}
	if !reflect.DeepEqual(got.Cohort, r.Cohort) || !reflect.DeepEqual(got.AnswerPlan, r.AnswerPlan) {
		t.Fatal("retained evidence or plan changed")
	}
	for i := 0; i < 3; i++ {
		next, events := AccountForRetainedRanking(got)
		if !reflect.DeepEqual(next, got) || !events[0].ExistingRow || events[0].RowAdded {
			t.Fatalf("repeated accounting rewrote rows: %+v", events)
		}
	}
	// An existing account stays authoritative even when legacy qualification
	// is absent. This negative helper cell makes no production reachability claim.
	got.Cohort = nil
	next, events := AccountForRetainedRanking(got)
	if !reflect.DeepEqual(next, got) || !events[0].ExistingRow || events[0].QualificationRecorded {
		t.Fatal("rewrote existing account with absent qualification")
	}
}

func TestRetainedRankingScopeUsesOnlyRecordedMemberEvidence(t *testing.T) {
	for _, name := range []string{"complete", "incomplete", "truncated", "member_cut", "ceiling", "group_cut", "no_reduction"} {
		t.Run(name, func(t *testing.T) {
			defer retainedRankingPanicFailsCell(t)
			r := retainedRankingResult(t)
			step := v1.ContextFabricPlanNarrowing{Stage: v1.ContextFabricPlanNarrowingAssembledResult, Before: 3, After: 1, Basis: v1.ContextFabricNarrowingBasisCanonicalIDLexical, Overrun: v1.ContextFabricBudgetOverrunItems}
			switch name {
			case "incomplete":
				r.Cohort.Complete = false
			case "truncated":
				r.Cohort.Truncated = true
			case "member_cut":
				r.AnswerPlan.Narrowing = []v1.ContextFabricPlanNarrowing{step}
			case "ceiling":
				step.Stage = v1.ContextFabricPlanNarrowingCardinality
				r.AnswerPlan.Narrowing = []v1.ContextFabricPlanNarrowing{step}
			case "group_cut":
				step.Groups = true
				r.AnswerPlan.Narrowing = []v1.ContextFabricPlanNarrowing{step}
			case "no_reduction":
				step.Before = 1
				r.AnswerPlan.Narrowing = []v1.ContextFabricPlanNarrowing{step}
			}
			got, _ := AccountForRetainedRanking(r)
			row := got.Completeness.Outcomes[0]
			narrowed := name == "incomplete" || name == "truncated" || name == "member_cut"
			if (row.Outcome == v1.ContextFabricRequirementNarrowed) != narrowed {
				t.Fatalf("recorded scope not preserved: %+v", row)
			}
			if row.Served != 0 || row.Declared != 0 {
				t.Fatalf("invented numeric population: %+v", row)
			}
			if name == "member_cut" && (row.CauseNarrowing != step.Basis || row.CauseOverrun != step.Overrun) {
				t.Fatalf("lost stored narrowing cause: %+v", row)
			}
			if (name == "incomplete" || name == "truncated") && row.CauseCoverage != v1.ContextFabricCoverageDetailPopulationTruncated {
				t.Fatalf("lost retained scope flags: %+v", row)
			}
		})
	}
}

func TestRetainedRankingExistingRowIdentity(t *testing.T) {
	for _, name := range []string{"stage", "requirement", "obligation", "exact"} {
		t.Run(name, func(t *testing.T) {
			defer retainedRankingPanicFailsCell(t)
			r := retainedRankingResult(t)
			requirement := r.AnswerPlan.Requirements[0]
			row := RequirementOutcomeRow{Requirement: requirement.Requirement, Obligation: requirement.Obligation, Stage: v1.ContextFabricOutcomeStageAssembledResult, Outcome: v1.ContextFabricRequirementUnavailable}
			switch name {
			case "stage":
				row.Stage = v1.ContextFabricOutcomeStagePlanning
			case "requirement":
				row.Requirement = "ranking/member/incident"
			case "obligation":
				row.Obligation = "count" // Deliberately inconsistent helper input; not a valid stored carrier.
			}
			r.Completeness.Outcomes = []RequirementOutcomeRow{row}
			got, events := AccountForRetainedRanking(r)
			exact := name == "exact"
			if events[0].ExistingRow != exact || events[0].RowAdded == exact || !reflect.DeepEqual(got.Completeness.Outcomes[0], row) {
				t.Fatalf("existing identity matching rewrote or suppressed accounting: %+v %+v", events, got.Completeness.Outcomes)
			}
		})
	}
}

func retainedRankingPanicFailsCell(t *testing.T) {
	t.Helper()
	if recovered := recover(); recovered != nil {
		t.Fatalf("retained ranking guard panicked: %v", recovered)
	}
}

func TestRetainedRankingQualificationCountsMatchRecordedOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name                                                string
		factCount                                           int
		outcome                                             v1.ContextFabricCohortMemberOutcome
		qualified, provisional, insufficient, notApplicable int
	}{
		{"qualified", 5, CohortOutcomeQualified, 1, 0, 0, 0},
		{"provisional", 2, CohortOutcomeProvisional, 0, 1, 0, 0},
		{"insufficient", 1, CohortOutcomeInsufficientEvidence, 0, 0, 1, 0},
		{"not_applicable", 0, CohortOutcomeNotApplicable, 0, 0, 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := retainedRankingResult(t)
			facts := []CanonicalFact{investmentFact("one", balancedThemes(), 0.05), healthFact("one", "low"), deficiencyFact("one", "high"), readinessFact("one", 0.9), workloadFact("one", 8)}
			input := &Cohort{Kind: SubjectTeam, Complete: true, Members: []CohortMember{rankTestMember("one")}}
			coverage := availableCoverage()
			if tc.factCount < 5 {
				coverage = deficiencyPrunedCoverage()
			}
			r.Cohort, _, _ = RankCohort(input, facts[:tc.factCount], coverage)
			if r.Cohort.Members[0].Outcome != tc.outcome {
				t.Fatalf("real ranking did not produce %s: %+v", tc.outcome, r.Cohort.Members[0])
			}
			_, events := AccountForRetainedRanking(r)
			if len(events) != 1 {
				t.Fatalf("events=%+v", events)
			}
			e := events[0]
			if !e.QualificationRecorded || e.RetainedMembers != 1 || e.Qualified != tc.qualified || e.Provisional != tc.provisional || e.InsufficientEvidence != tc.insufficient || e.NotApplicable != tc.notApplicable || e.UnrecordedMembers != 0 {
				t.Fatalf("retained counts disagree with actual producer outcome: %+v", e)
			}
		})
	}
}
