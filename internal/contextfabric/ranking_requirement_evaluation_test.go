package contextfabric

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The engine must evaluate the declared ranking after its real rank step.
func TestRankingRequirementEvaluatesExecutedTeamCohort(t *testing.T) {
	frame := rankingFrameOverACohort()
	cohort := &Cohort{
		Kind:      SubjectTeam,
		Rationale: "synthetic cohort",
		Members: []CohortMember{{
			Subject:          SubjectRef{Kind: SubjectTeam, CanonicalID: "team:synthetic", Label: "Synthetic"},
			Rank:             1,
			InclusionReasons: []string{"synthetic"},
		}},
		Complete: true,
	}

	result, observed, reads := rankingInvestigation(t, cohort, frame, QuestionFamilyDiscoveredCohortRanking, ShapeDiscoveredCohort)
	if result.Cohort == nil || len(result.Cohort.Members) != 1 {
		t.Fatalf("served result cohort = %#v; want one synthetic member", result.Cohort)
	}
	member := result.Cohort.Members[0]
	if !member.RankingComputed {
		t.Fatalf("real rank step did not execute: ranking_computed=%v attention_rank=%d", member.RankingComputed, member.AttentionRank)
	}

	planRow, ok := planRequirementForObligation(result, ObligationRanking)
	if !ok {
		t.Fatal("served plan has no ranking requirement")
	}
	planning := outcomeRowsForObligation(result, ObligationRanking, contractsv1.ContextFabricOutcomeStagePlanning)
	assembled := outcomeRowsForObligation(result, ObligationRanking, contractsv1.ContextFabricOutcomeStageAssembledResult)
	if len(planning) != 1 {
		t.Fatalf("ranking planning rows = %d; want one", len(planning))
	}
	if planning[0].Outcome != contractsv1.ContextFabricRequirementSatisfied {
		t.Fatalf("ranking planning outcome = %q; want satisfied before evaluating the missing assembled row", planning[0].Outcome)
	}
	if len(assembled) != 1 {
		t.Fatalf("assembled ranking rows = %d; want exactly one", len(assembled))
	}
	if assembled[0].Outcome != contractsv1.ContextFabricRequirementUnavailable {
		t.Fatalf("missing signals must not satisfy ranking: %+v", assembled[0])
	}
	t.Logf("ranking: reads=%d observed=%v plan_step=%q input_fact_kinds=%v planning=%q assembled_rows=%d ranking_computed=%v", reads, sortedObservedKinds(observed), planRow.Step, planRow.InputFactKinds, planning[0].Outcome, len(assembled), member.RankingComputed)
}

// The issue's incident cohort path is the same engine seam. This companion
// pin makes the member kind explicit so a future fix cannot cover only teams.
func TestRankingRequirementEvaluatesExecutedIncidentCohort(t *testing.T) {
	frame := frameWith(
		[]InvestigationGoal{GoalRankOrSurvey},
		discoveredExpression(SubjectIncident),
		TemporalIntentCurrent,
		nil,
	)
	cohort := &Cohort{
		Kind:      SubjectIncident,
		Rationale: "synthetic incident cohort",
		Members: []CohortMember{{
			Subject:          SubjectRef{Kind: SubjectIncident, CanonicalID: "incident:synthetic", Label: "Synthetic incident"},
			Rank:             1,
			InclusionReasons: []string{"synthetic"},
		}},
		Complete: true,
	}

	result, _, _ := rankingInvestigation(t, cohort, &frame, QuestionFamilyDiscoveredCohortRanking, ShapeDiscoveredCohort)
	if result.Cohort == nil || len(result.Cohort.Members) != 1 {
		t.Fatalf("served incident cohort = %#v; want one synthetic member", result.Cohort)
	}
	if !result.Cohort.Members[0].RankingComputed {
		t.Fatal("real incident rank step did not execute")
	}
	planning := outcomeRowsForObligation(result, ObligationRanking, contractsv1.ContextFabricOutcomeStagePlanning)
	assembled := outcomeRowsForObligation(result, ObligationRanking, contractsv1.ContextFabricOutcomeStageAssembledResult)
	if len(planning) != 1 || planning[0].Outcome != contractsv1.ContextFabricRequirementSatisfied {
		t.Fatalf("incident ranking planning rows = %#v; want one satisfied planning row", planning)
	}
	if len(assembled) != 1 {
		t.Fatalf("incident ranking assembled rows = %d; want exactly one", len(assembled))
	}
	if assembled[0].Outcome != contractsv1.ContextFabricRequirementUnavailable {
		t.Fatalf("missing signals must not satisfy ranking: %+v", assembled[0])
	}
	t.Logf("incident ranking: member_kind=%q planning=%q assembled_rows=%d ranking_computed=%v", SubjectIncident, planning[0].Outcome, len(assembled), result.Cohort.Members[0].RankingComputed)
}

func TestRankingRequirementUsesActualMemberQualification(t *testing.T) {
	for _, tc := range []struct {
		name   string
		alter  func(*Cohort, *MembershipCardinality)
		mode   string
		want   RequirementOutcome
		impact AnswerImpactKind
	}{
		{name: "qualified", want: contractsv1.ContextFabricRequirementSatisfied, impact: contractsv1.ContextFabricAnswerImpactNone},
		{name: "provisional", mode: "provisional", want: contractsv1.ContextFabricRequirementNarrowed, impact: contractsv1.ContextFabricAnswerImpactDepth},
		{name: "insufficient", mode: "insufficient", want: contractsv1.ContextFabricRequirementUnavailable, impact: contractsv1.ContextFabricAnswerImpactDimension},
		{name: "no_signals", mode: "none", want: contractsv1.ContextFabricRequirementUnavailable, impact: contractsv1.ContextFabricAnswerImpactDimension},
		{name: "not_executed", alter: func(c *Cohort, _ *MembershipCardinality) { c.Members[0].RankingComputed = false }, want: contractsv1.ContextFabricRequirementUnavailable, impact: contractsv1.ContextFabricAnswerImpactDimension},
		{name: "missing_score", alter: func(c *Cohort, _ *MembershipCardinality) { c.Members[0].Score = nil }, want: contractsv1.ContextFabricRequirementUnavailable, impact: contractsv1.ContextFabricAnswerImpactDimension},
		{name: "unknown_member_outcome", alter: func(c *Cohort, _ *MembershipCardinality) { c.Members[0].Outcome = "future_outcome" }, want: contractsv1.ContextFabricRequirementUnavailable, impact: contractsv1.ContextFabricAnswerImpactDimension},
		{name: "wrong_cohort_kind", alter: func(c *Cohort, _ *MembershipCardinality) { c.Kind = SubjectIncident }, want: contractsv1.ContextFabricRequirementUnavailable, impact: contractsv1.ContextFabricAnswerImpactDimension},
		{name: "wrong_member_kind", alter: func(c *Cohort, _ *MembershipCardinality) { c.Members[0].Subject.Kind = SubjectIncident }, want: contractsv1.ContextFabricRequirementUnavailable, impact: contractsv1.ContextFabricAnswerImpactDimension},
		{name: "incomplete", alter: func(c *Cohort, _ *MembershipCardinality) { c.Complete = false }, want: contractsv1.ContextFabricRequirementNarrowed, impact: contractsv1.ContextFabricAnswerImpactScope},
		{name: "truncated", alter: func(c *Cohort, _ *MembershipCardinality) { c.Truncated = true }, want: contractsv1.ContextFabricRequirementNarrowed, impact: contractsv1.ContextFabricAnswerImpactScope},
		{name: "known_larger_population", alter: func(_ *Cohort, m *MembershipCardinality) { m.Declared = 3 }, want: contractsv1.ContextFabricRequirementNarrowed, impact: contractsv1.ContextFabricAnswerImpactScope},
		{name: "empty", alter: func(c *Cohort, _ *MembershipCardinality) { c.Members = nil }, want: contractsv1.ContextFabricRequirementUnavailable, impact: contractsv1.ContextFabricAnswerImpactDimension},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cohort := &Cohort{Kind: SubjectTeam, Complete: true, Members: []CohortMember{rankTestMember("one")}}
			facts := []CanonicalFact{investmentFact("one", balancedThemes(), 0.05), healthFact("one", "low"), deficiencyFact("one", "high"), readinessFact("one", 0.9), workloadFact("one", 8)}
			switch tc.mode {
			case "provisional":
				facts = facts[:2]
			case "insufficient":
				facts = facts[1:2]
			case "none":
				facts = nil
			}
			coverage := availableCoverage()
			if tc.mode != "" {
				coverage = deficiencyPrunedCoverage()
			}
			cohort, _, _ = RankCohort(cohort, facts, coverage)
			cardinality, _ := ComputeMembershipCardinality(cohort, 0, nil)
			if tc.alter != nil {
				tc.alter(cohort, &cardinality)
			}
			frame := rankingFrameOverACohort()
			derived := registryDeriver{}.DeriveRequirements(*frame)
			plan := AnswerPlan{Requirements: PlanRequirementsFromDerived(derived)}
			engine := &Engine{requirements: registryDeriver{}}
			result := InvestigationResult{Cohort: cohort, Coverage: coverage}
			var firstOutcomes []RequirementOutcomeRow
			for pass := 1; pass <= 3; pass++ {
				result = engine.finalizeResult(context.Background(), storage.Principal{}, result, plan, frame, CanonicalFactBundle{Facts: facts, Coverage: coverage}, nil, pass, cardinality)
				if pass == 1 {
					firstOutcomes = append([]RequirementOutcomeRow(nil), result.Completeness.Outcomes...)
				} else if !reflect.DeepEqual(firstOutcomes, result.Completeness.Outcomes) {
					t.Fatalf("pass %d changed sibling outcomes: before=%+v after=%+v", pass, firstOutcomes, result.Completeness.Outcomes)
				}
				rows := outcomeRowsForObligation(result, ObligationRanking, contractsv1.ContextFabricOutcomeStageAssembledResult)
				if len(rows) != 1 {
					t.Fatalf("pass %d assembled ranking rows=%+v", pass, rows)
				}
				if rows[0].Outcome != tc.want || rows[0].Impact != tc.impact {
					t.Fatalf("pass %d row=%+v want %s/%s member=%+v", pass, rows[0], tc.want, tc.impact, cohort.Members)
				}
				if tc.want == contractsv1.ContextFabricRequirementUnavailable && tc.name != "empty" && tc.name != "wrong_cohort_kind" || tc.impact == contractsv1.ContextFabricAnswerImpactDepth {
					if rows[0].CauseCoverage != contractsv1.ContextFabricCoverageDetailFactProviderReported || rows[0].CauseObserved {
						t.Fatalf("evidence fallback must be defaulted: %+v", rows[0])
					}
				}
				if err := contractsv1.ValidateContextFabricPlanRequirementOutcomeRow(rows[0]); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestRankingRequirementEvaluatesSuccessfulEngineRanking(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprintf("missing_second_member=%v", missing), func(t *testing.T) {
			cohort := &Cohort{Kind: SubjectTeam, Rationale: "bounded team cohort", Complete: true, Members: []CohortMember{rankTestMember("one"), rankTestMember("two")}}
			cohort.Members[1].Rank = 2
			facts := []CanonicalFact{}
			for _, id := range []string{"one", "two"} {
				if missing && id == "two" {
					continue
				}
				facts = append(facts, investmentFact(id, balancedThemes(), 0.05), healthFact(id, "low"), deficiencyFact(id, "high"), readinessFact(id, 0.9), workloadFact(id, 8))
			}
			result, _, _ := rankingInvestigation(t, cohort, rankingFrameOverACohort(), QuestionFamilyDiscoveredCohortRanking, ShapeDiscoveredCohort, CanonicalFactBundle{Facts: facts, Coverage: availableCoverage(), Version: "ops-v1"})
			rows := outcomeRowsForObligation(result, ObligationRanking, contractsv1.ContextFabricOutcomeStageAssembledResult)
			want := contractsv1.ContextFabricRequirementSatisfied
			if missing {
				want = contractsv1.ContextFabricRequirementNarrowed
			}
			if len(rows) != 1 || rows[0].Outcome != want {
				t.Fatalf("ranking outcomes=%+v want %s", rows, want)
			}
			if result.Cohort.Members[0].Outcome != CohortOutcomeQualified || result.Cohort.Members[0].Score == nil {
				t.Fatalf("first actual rank=%+v", result.Cohort.Members[0])
			}
			if missing && result.Cohort.Members[1].Score != nil {
				t.Fatal("missing member unexpectedly received a score")
			}
		})
	}
}

func TestRankingRequirementPreservesSiblingOutcomes(t *testing.T) {
	frame := rankingFrameOverACohort()
	derived := registryDeriver{}.DeriveRequirements(*frame)
	var ranking contractsv1.ContextFabricPlanRequirement
	for _, r := range PlanRequirementsFromDerived(derived) {
		if r.Obligation == string(ObligationRanking) {
			ranking = r
		}
	}
	if ranking.Requirement == "" {
		t.Fatal("no published ranking requirement")
	}
	planning := SeedRequirementOutcomes(derived)
	for _, tc := range []struct {
		name string
		edit func(*contractsv1.ContextFabricPlanRequirement)
		want int
	}{
		{name: "ranking", want: 1},
		{name: "read_sibling", edit: func(r *contractsv1.ContextFabricPlanRequirement) { r.Kind = string(ObligationKindRead) }, want: 0},
		{name: "count_sibling", edit: func(r *contractsv1.ContextFabricPlanRequirement) {
			r.Obligation = string(ObligationCount)
			r.Requirement = "count/member/team"
		}, want: 0},
		{name: "unavailable", edit: func(r *contractsv1.ContextFabricPlanRequirement) {
			r.Unavailable = string(RequirementReasonComputedPopulationAbsent)
		}, want: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requirement := ranking
			if tc.edit != nil {
				tc.edit(&requirement)
			}
			before := append([]RequirementOutcomeRow(nil), planning...)
			rows := appendRankingRequirementEvaluations(before, []contractsv1.ContextFabricPlanRequirement{requirement}, nil, MembershipCardinality{})
			if len(rows) != len(before)+tc.want {
				t.Fatalf("outcomes %d -> %d, want added=%d", len(before), len(rows), tc.want)
			}
			if !reflect.DeepEqual(rows[:len(before)], before) {
				t.Fatal("sibling/planning rows were rewritten")
			}
			again := appendRankingRequirementEvaluations(rows, []contractsv1.ContextFabricPlanRequirement{requirement, requirement}, nil, MembershipCardinality{})
			if !reflect.DeepEqual(rows, again) {
				t.Fatal("repeated/duplicate publication appended another row")
			}
		})
	}
}

func TestRankingRequirementRecordsScopeCause(t *testing.T) {
	frame := rankingFrameOverACohort()
	var requirement contractsv1.ContextFabricPlanRequirement
	for _, r := range PlanRequirementsFromDerived(registryDeriver{}.DeriveRequirements(*frame)) {
		if r.Obligation == string(ObligationRanking) {
			requirement = r
		}
	}
	cohort := &Cohort{Kind: SubjectTeam, Complete: true, Members: []CohortMember{rankTestMember("one")}}
	facts := []CanonicalFact{investmentFact("one", balancedThemes(), 0.05), healthFact("one", "low"), deficiencyFact("one", "high"), readinessFact("one", 0.9), workloadFact("one", 8)}
	cohort, _, _ = RankCohort(cohort, facts, availableCoverage())
	for _, basis := range []contractsv1.ContextFabricNarrowingBasis{"", contractsv1.ContextFabricNarrowingBasisAttentionRank} {
		cardinality := MembershipCardinality{Resolved: true, Kind: SubjectTeam, Served: 1, Declared: 3, Basis: basis}
		if basis != "" {
			cardinality.Overrun = contractsv1.ContextFabricBudgetOverrunItems
		}
		row := rankingRequirementOutcome(requirement, cohort, cardinality)
		if row.Outcome != contractsv1.ContextFabricRequirementNarrowed || row.Impact != contractsv1.ContextFabricAnswerImpactScope || !row.CauseObserved {
			t.Fatalf("row=%+v", row)
		}
		if row.CauseNarrowing != basis || row.CauseOverrun != cardinality.Overrun {
			t.Fatalf("lost actual narrowing cause: %+v", row)
		}
		wantCoverage := contractsv1.ContextFabricCoverageDetailPopulationTruncated
		if basis != "" {
			wantCoverage = ""
		}
		if row.CauseCoverage != wantCoverage {
			t.Fatalf("cause=%s want %s", row.CauseCoverage, wantCoverage)
		}
	}
}

// A retained population basis must not masquerade as the cause of an
// independently incomplete cohort when that cardinality was not narrowed.
func TestRankingRequirementIgnoresUnappliedPopulationBasis(t *testing.T) {
	frame := rankingFrameOverACohort()
	var requirement contractsv1.ContextFabricPlanRequirement
	for _, r := range PlanRequirementsFromDerived(registryDeriver{}.DeriveRequirements(*frame)) {
		if r.Obligation == string(ObligationRanking) {
			requirement = r
		}
	}
	cohort := &Cohort{Kind: SubjectTeam, Complete: false, Members: []CohortMember{rankTestMember("one")}}
	facts := []CanonicalFact{investmentFact("one", balancedThemes(), 0.05), healthFact("one", "low"), deficiencyFact("one", "high"), readinessFact("one", 0.9), workloadFact("one", 8)}
	cohort, _, _ = RankCohort(cohort, facts, availableCoverage())
	cardinality := MembershipCardinality{Resolved: true, Kind: SubjectTeam, Served: 1, Declared: 1, Basis: contractsv1.ContextFabricNarrowingBasisAttentionRank}
	row := rankingRequirementOutcome(requirement, cohort, cardinality)
	if row.CauseCoverage != contractsv1.ContextFabricCoverageDetailPopulationTruncated || row.CauseNarrowing != "" || row.CauseOverrun != "" {
		t.Fatalf("unapplied basis replaced actual incomplete-population cause: %+v", row)
	}
}
