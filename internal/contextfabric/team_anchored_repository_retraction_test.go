package contextfabric

import (
	"testing"
)

// A team anchor whose own reach served ten repositories but that no driver
// stands on is retracted by the commit gate. The repositories stay served and
// the retraction is recorded; the gate never re-discovers or edits the cohort.
func TestCommitAffirmationKeepsAnAnchoredRepositoryCohortWhenItRetractsTheTeam(t *testing.T) {
	result := teamCommitResult()
	members := make([]CohortMember, 0, 10)
	for i := 0; i < 10; i++ {
		id := string(rune('a' + i))
		members = append(members, CohortMember{Subject: SubjectRef{Kind: SubjectRepository, CanonicalID: "repository:" + id, Label: "full-chaos/" + id}, Rank: i + 1})
	}
	result.Cohort = &Cohort{Kind: SubjectRepository, Members: members, Complete: true}
	result.Drivers = nil
	applyCommitAffirmation(&result, scopedInputs(result, emptyAffirmationFacts(), SubjectRepository, SubjectTeam))

	if result.SubjectResolution.Committed == nil || len(result.SubjectResolution.Committed) != 0 {
		t.Fatalf("Committed = %#v, want the unaffirmed team retracted to an empty non-nil slice", result.SubjectResolution.Committed)
	}
	if result.Cohort == nil || len(result.Cohort.Members) != 10 || !result.Cohort.Complete {
		t.Fatalf("Cohort = %+v, want the ten anchored repositories untouched", result.Cohort)
	}
	found := false
	for _, limitation := range result.Limitations {
		found = found || limitation == commitRetractionLimitation
	}
	if !found {
		t.Fatalf("Limitations = %v, want the retraction disclosure", result.Limitations)
	}
	if !result.Coverage.Partial {
		t.Fatal("Coverage.Partial = false, want the retraction to mark the answer partial")
	}
	for _, candidate := range result.SubjectResolution.Candidates {
		if candidate.State == ResolutionCommitted {
			t.Fatalf("candidate %+v still committed after the retraction", candidate)
		}
	}
}
