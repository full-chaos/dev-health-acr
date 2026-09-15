package answerprojection

import (
	"fmt"
	"reflect"
	"slices"
	"testing"

	v1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func displayTuple(members int) v1.ContextFabricInvestigationResult {
	r := richResult()
	r.Drivers = nil
	r.ClaimedFacts = nil
	r.AnswerPlan = &v1.ContextFabricAnswerPlan{Family: v1.ContextFabricQuestionFamilyScopedCohortStatus, MemberKind: v1.ContextFabricSubjectWorkItem}
	r.Cohort = &v1.ContextFabricCohort{Kind: v1.ContextFabricSubjectWorkItem, Complete: true}
	for i := 0; i < members; i++ {
		subject := v1.ContextFabricSubjectRef{Kind: v1.ContextFabricSubjectWorkItem, CanonicalID: fmt.Sprintf("wi_%02d", i), Label: fmt.Sprintf("Title %02d", i)}
		r.Cohort.Members = append(r.Cohort.Members, v1.ContextFabricCohortMember{Subject: subject, Rank: i + 1, EvidenceRefIDs: []string{fmt.Sprintf("evidence_%02d", i)}})
		status := "open"
		if i == members-1 {
			status = "waiting"
		}
		title := subject.Label
		r.ClaimedFacts = append(r.ClaimedFacts, v1.ContextFabricClaimedFact{ClaimID: fmt.Sprintf("status_%02d", i), Kind: v1.ContextFabricFactStatus, Subject: subject, Field: "status", Value: v1.ContextFabricScalarValue{String: &status}}, v1.ContextFabricClaimedFact{ClaimID: fmt.Sprintf("work_%02d", i), Kind: v1.ContextFabricFactWork, Subject: subject, Field: "title", Value: v1.ContextFabricScalarValue{String: &title}})
	}
	n := int64(members)
	r.ClaimedFacts = append(r.ClaimedFacts, v1.ContextFabricClaimedFact{ClaimID: "count_work_items", Kind: v1.ContextFabricFactCardinality, Subject: r.SubjectResolution.Committed[0], Field: "work_item_count", Value: v1.ContextFabricScalarValue{Integer: &n}})
	return r
}

func TestWorkItemDisplayClosure(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		budget                  Budget
		members, facts, omitted int
	}{
		{"default_twelve", Budget{}, 12, 25, 0},
		{"fact_pair_boundary", Budget{MaxFacts: 4}, 1, 3, 22},
		{"member_boundary", Budget{MaxCohortMembers: 2}, 2, 5, 20},
		{"evidence_boundary", Budget{MaxEvidenceRefs: 2}, 2, 5, 20},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := displayTuple(12)
			p := Project(r, tc.budget)
			if len(p.Cohort.Members) != tc.members || len(p.KeyFacts) != tc.facts || p.ProjectionBudget.FactsOmitted != tc.omitted || p.ProjectionBudget.CohortMembersOmitted != 12-tc.members {
				t.Errorf("members/facts/omitted=%d/%d/%d member omissions=%d", len(p.Cohort.Members), len(p.KeyFacts), p.ProjectionBudget.FactsOmitted, p.ProjectionBudget.CohortMembersOmitted)
			}
			for i, m := range p.Cohort.Members {
				if m.Subject != r.Cohort.Members[i].Subject {
					t.Error("canonical order changed")
				}
				for _, ref := range m.EvidenceRefIDs {
					if !slices.Contains(p.EvidenceRefIDs, ref) {
						t.Errorf("missing evidence %s", ref)
					}
				}
				for _, want := range r.ClaimedFacts {
					if want.Subject.Kind != m.Subject.Kind || want.Subject.CanonicalID != m.Subject.CanonicalID {
						continue
					}
					found := false
					for _, got := range p.KeyFacts {
						if got.ClaimID == want.ClaimID {
							found = true
							if !reflect.DeepEqual(got.Value, want.Value) {
								t.Errorf("changed %s", want.ClaimID)
							}
						}
					}
					if !found {
						t.Errorf("displayed member lost %s", want.ClaimID)
					}
				}
				if m.RankingComputed || m.Score != nil || m.AttentionRank != 0 {
					t.Error("invented ranking")
				}
			}
			if len(p.PrincipalDrivers) != 0 || len(p.Cohort.RankingTable) != 0 {
				t.Error("invented driver/table")
			}
		})
	}
}

func TestWorkItemDisplayEligibilityAndOmissions(t *testing.T) {
	r := displayTuple(2)
	// Same opaque ID in another kind is not a member match.
	other := r.ClaimedFacts[0]
	other.ClaimID = "nonmember"
	other.Subject.Kind = v1.ContextFabricSubjectTeam
	r.ClaimedFacts = append(r.ClaimedFacts, other)
	foreign := r.ClaimedFacts[0]
	foreign.ClaimID, foreign.Subject.CanonicalID = "outside_member_scope", "another_work_item"
	r.ClaimedFacts = append(r.ClaimedFacts, foreign)
	// Missing status is preserved as missing; explicit unknown stays a value.
	r.ClaimedFacts = r.ClaimedFacts[1:]
	unknown := "unknown"
	r.ClaimedFacts[1].Value.String = &unknown
	p := Project(r, Budget{})
	if len(p.Cohort.Members) != 2 || len(p.KeyFacts) != 4 || p.ProjectionBudget.FactsOmitted != 0 {
		t.Errorf("missing/unknown selection: %+v", p.ProjectionBudget)
	}
	for _, f := range p.KeyFacts {
		if f.ClaimID == "nonmember" || f.ClaimID == "outside_member_scope" {
			t.Error("claim outside full member identity admitted")
		}
	}
	// All eligible dropped counts, including uncited server cardinality.
	r = displayTuple(1)
	r.Drivers = []v1.ContextFabricDriverJudgment{driver("cited", v1.ContextFabricDriverPrincipal, "status", "status", []string{"status_00"}, r.Cohort.Members[0].Subject)}
	p = Project(r, Budget{MaxFacts: 1})
	if len(p.KeyFacts) != 1 || p.ProjectionBudget.FactsOmitted != 2 || len(p.Cohort.Members) != 0 {
		t.Errorf("dedup/drop=%d/%d/%d", len(p.KeyFacts), p.ProjectionBudget.FactsOmitted, len(p.Cohort.Members))
	}
	r.AnswerPlan = nil
	p = Project(r, Budget{})
	if len(p.KeyFacts) != 2 {
		t.Error("direct-fact exception escaped tuple")
	}
}

func TestWorkItemDisplayExceptionDomain(t *testing.T) {
	for _, mode := range []string{"plan_absent", "other_family", "other_member_kind", "other_cohort_kind", "ranked", "rows", "table", "time_rows", "time_table"} {
		t.Run(mode, func(t *testing.T) {
			r := displayTuple(1)
			switch mode {
			case "plan_absent":
				r.AnswerPlan = nil
			case "other_family":
				r.AnswerPlan.Family = v1.ContextFabricQuestionFamilySubjectInvestigation
			case "other_member_kind":
				r.AnswerPlan.MemberKind = v1.ContextFabricSubjectTeam
			case "other_cohort_kind":
				r.Cohort.Kind = v1.ContextFabricSubjectTeam
			case "ranked":
				r.Cohort.Members[0].RankingComputed = true
			case "rows":
				r.ClaimedFacts[0].Rows = []v1.ContextFabricClaimedFactRow{{}}
			case "table":
				r.ClaimedFacts[0].Table = &v1.ContextFabricClaimedFactTable{}
			case "time_rows":
				r.ClaimedFacts[0].TimeSeriesRows = []v1.ContextFabricClaimedFactRow{{}}
			case "time_table":
				r.ClaimedFacts[0].TimeSeriesTable = &v1.ContextFabricClaimedFactTable{}
			}
			p := Project(r, Budget{})
			for _, f := range p.KeyFacts {
				if f.ClaimID == "status_00" {
					t.Error("scalar member exception escaped its domain")
				}
			}
		})
	}
}

func TestWorkItemDisplayRetainsMultipleObservationsAndDeduplicatesDrivers(t *testing.T) {
	r := displayTuple(1)
	second := r.ClaimedFacts[0]
	second.ClaimID = "second_status"
	unknown := "unknown"
	second.Value.String = &unknown
	r.ClaimedFacts = append(r.ClaimedFacts, second)
	r.Drivers = []v1.ContextFabricDriverJudgment{driver("direct_citation", v1.ContextFabricDriverPrincipal, "status", "status", []string{"status_00"}, r.Cohort.Members[0].Subject)}
	p := Project(r, Budget{MaxFacts: 4})
	ids := map[string]int{}
	for _, f := range p.KeyFacts {
		ids[f.ClaimID]++
	}
	for _, f := range r.ClaimedFacts {
		if ids[f.ClaimID] != 1 {
			t.Errorf("claim %s copies=%d", f.ClaimID, ids[f.ClaimID])
		}
	}
	if len(p.Cohort.Members) != 1 || p.ProjectionBudget.FactsOmitted != 0 {
		t.Error("driver dedup falsely omitted member/fact")
	}
}
