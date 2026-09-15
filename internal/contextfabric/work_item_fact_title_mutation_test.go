package contextfabric

import (
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"strings"
	"testing"
)

func reportWorkItemMutationPanic(t *testing.T) {
	if recovered := recover(); recovered != nil {
		t.Errorf("work-item guard panic: %v", recovered)
	}
}

func TestWorkItemGuardFactBoundaryDomain(t *testing.T) {
	for _, name := range []string{"valid", "empty", "short_roots", "three_requirements", "bad_member_identity", "bad_member_identity_consistent", "foreign_root_id", "root_kind", "foreign_requirement_id", "requirement_kind", "repeated_status", "repeated_work", "extra_requirement_subject"} {
		t.Run(name, func(t *testing.T) {
			defer reportWorkItemMutationPanic(t)
			p := workItemTuplePayloadFixture(t)
			subjects := workItemTupleSubjects(p.Cohort)
			r := CanonicalFactRequest{workItemTuple: true, Cohort: p.Cohort, Subjects: subjects, Requirements: workItemTupleFactRequirements(subjects)}
			switch name {
			case "empty":
				r.Cohort = &Cohort{Members: []CohortMember{}}
				r.Subjects = []SubjectRef{}
				r.Requirements = workItemTupleFactRequirements(nil)
			case "short_roots":
				r.Subjects = nil
			case "three_requirements":
				r.Requirements = append(r.Requirements, r.Requirements[0])
			case "bad_member_identity":
				r.Cohort.Members[0].Subject.CanonicalID = "invalid"
			case "bad_member_identity_consistent":
				malformedID := "work_item.v2:repo-1"
				for index := range r.Cohort.Members {
					r.Cohort.Members[index].Subject.CanonicalID = malformedID
				}
				for index := range r.Subjects {
					r.Subjects[index].CanonicalID = malformedID
				}
				for requirementIndex := range r.Requirements {
					for subjectIndex := range r.Requirements[requirementIndex].Subjects {
						r.Requirements[requirementIndex].Subjects[subjectIndex].CanonicalID = malformedID
					}
				}
			case "foreign_root_id":
				r.Subjects[0].CanonicalID = "work_item:repo-1:foreign"
			case "root_kind":
				r.Subjects[0].Kind = SubjectProject
			case "foreign_requirement_id":
				r.Requirements[0].Subjects[0].CanonicalID = "work_item:repo-1:foreign"
			case "requirement_kind":
				r.Requirements[0].Subjects[0].Kind = SubjectProject
			case "repeated_status":
				r.Requirements[1].Kind = FactStatus
			case "repeated_work":
				r.Requirements[0].Kind = FactWork
			case "extra_requirement_subject":
				r.Requirements[0].Subjects = append(r.Requirements[0].Subjects, subjects[0])
			}
			err := validateWorkItemTupleFactRequest(r)
			if (err == nil) != (name == "valid") {
				t.Errorf("guard error=%v valid=%v", err, name == "valid")
			}
			if name == "bad_member_identity_consistent" {
				if err == nil || err.Error() != "work-item tuple fact subject invalid" {
					t.Errorf("consistent malformed member identity error=%v, want canonical identity rejection", err)
				}
			}
		})
	}
}

func TestWorkItemGuardTitleEvidenceDomain(t *testing.T) {
	for _, name := range []string{"title", "wrong_fact_kind", "wrong_subject_kind", "nil_title", "empty_title", "blank_title", "foreign_id", "bounded_unicode"} {
		t.Run(name, func(t *testing.T) {
			defer reportWorkItemMutationPanic(t)
			p := workItemTuplePayloadFixture(t)
			cohort := p.Cohort
			cohort.Members[0].Subject.Label = "work-1"
			title := "  Human title  "
			fact := CanonicalFact{Kind: FactWork, Subject: cohort.Members[0].Subject, Fields: map[string]FactValue{"title": {String: &title}}}
			switch name {
			case "wrong_fact_kind":
				fact.Kind = FactStatus
			case "wrong_subject_kind":
				fact.Subject.Kind = SubjectProject
			case "nil_title":
				fact.Fields["title"] = FactValue{}
			case "empty_title":
				title = ""
			case "blank_title":
				title = "   "
			case "foreign_id":
				fact.Subject.CanonicalID = "work_item:repo-1:foreign"
			case "bounded_unicode":
				title = strings.Repeat("界", contractsv1.ContextFabricSubjectRefLabelMaxLength+1)
			}
			applyWorkItemTitles(cohort, []CanonicalFact{fact})
			want := "work-1"
			if name == "title" {
				want = "Human title"
			}
			if name == "bounded_unicode" {
				want = strings.Repeat("界", contractsv1.ContextFabricSubjectRefLabelMaxLength)
			}
			if cohort.Members[0].Subject.Label != want {
				t.Errorf("title=%q want=%q", cohort.Members[0].Subject.Label, want)
			}
		})
	}
}
