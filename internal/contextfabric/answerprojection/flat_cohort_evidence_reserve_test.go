package answerprojection

import (
	"fmt"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// ownedListing is a flat cohort of n projects each citing one reference,
// beside a driver citing a wide set of references.
func ownedListing(n, driverRefs int) contractsv1.ContextFabricInvestigationResult {
	result := richResult()
	members := make([]contractsv1.ContextFabricCohortMember, 0, n)
	for i := 0; i < n; i++ {
		members = append(members, contractsv1.ContextFabricCohortMember{
			Subject:          subject(contractsv1.ContextFabricSubjectProject, fmt.Sprintf("project_%02d", i), fmt.Sprintf("Project %02d", i)),
			Rank:             i + 1,
			InclusionReasons: []string{"Project the named team owns."},
			EvidenceRefIDs:   []string{fmt.Sprintf("evidence_member_%02d", i)},
		})
	}
	result.Cohort = &contractsv1.ContextFabricCohort{Kind: contractsv1.ContextFabricSubjectProject, Members: members, Rationale: "Owned projects.", Complete: true}
	refs := make([]string, 0, driverRefs)
	for i := 0; i < driverRefs; i++ {
		refs = append(refs, fmt.Sprintf("evidence_wide_%02d", i))
	}
	for i := range result.Drivers {
		if result.Drivers[i].Standing == contractsv1.ContextFabricDriverPrincipal {
			result.Drivers[i].EvidenceRefIDs = refs
			break
		}
	}
	return result
}

func TestFlatListingMembersAreServedBeforeDriverReferencesSpendTheBudget(t *testing.T) {
	projection := Project(ownedListing(19, 19), Budget{MaxCohortMembers: 25, MaxEvidenceRefs: 25})
	if got := len(projection.Cohort.Members); got != 19 {
		t.Fatalf("served %d members, want all 19", got)
	}
	if projection.ProjectionBudget.CohortMembersOmitted != 0 {
		t.Fatalf("cohort_members_omitted = %d, want 0", projection.ProjectionBudget.CohortMembersOmitted)
	}
	for _, member := range projection.Cohort.Members {
		if len(member.EvidenceRefIDs) != 1 {
			t.Fatalf("%s carries %d references, want its own 1: the drivers spent the budget first", member.Subject.CanonicalID, len(member.EvidenceRefIDs))
		}
	}
	if err := projection.Validate(); err != nil {
		t.Fatalf("projection invalid: %v", err)
	}
}

func TestFlatListingKeepsEveryMemberWhenTheReferenceBudgetIsSmaller(t *testing.T) {
	projection := Project(ownedListing(19, 3), Budget{MaxCohortMembers: 25, MaxEvidenceRefs: 5})
	if got := len(projection.Cohort.Members); got != 19 {
		t.Fatalf("served %d members, want all 19", got)
	}
	if got := len(projection.EvidenceRefIDs); got > 5 {
		t.Fatalf("indexed %d references, budget 5", got)
	}
	if projection.ProjectionBudget.EvidenceRefsOmitted == 0 {
		t.Fatalf("the references that did not fit are not counted: %+v", projection.ProjectionBudget)
	}
	if err := projection.Validate(); err != nil {
		t.Fatalf("projection invalid: %v", err)
	}
}

func TestCallerMemberBudgetStillCutsAFlatListing(t *testing.T) {
	projection := Project(ownedListing(19, 3), Budget{MaxCohortMembers: 5, MaxEvidenceRefs: 25})
	if got := len(projection.Cohort.Members); got != 5 || projection.Cohort.Total != 19 {
		t.Fatalf("served %d of total %d, want 5 of 19", got, projection.Cohort.Total)
	}
}

func TestFlatListingReservesOnlyForTheMembersItWillServe(t *testing.T) {
	projection := Project(ownedListing(19, 15), Budget{MaxCohortMembers: 5, MaxEvidenceRefs: 25})
	if len(projection.Cohort.Members) != 5 {
		t.Fatalf("served %d members, want 5", len(projection.Cohort.Members))
	}
	if projection.ProjectionBudget.DriversOmitted != 0 {
		t.Fatalf("drivers_omitted = %d: references were reserved for members that are not served", projection.ProjectionBudget.DriversOmitted)
	}
}

func TestGroupedCohortKeepsTheWholeMemberRule(t *testing.T) {
	result := ownedListing(4, 1)
	result.Cohort.Groups = []contractsv1.ContextFabricCohortGroup{{Subject: subject(contractsv1.ContextFabricSubjectTeam, "team_g", "Team G"), MemberCanonicalIDs: []string{"project_00"}}}
	if isFlatListing(result) {
		t.Fatal("a grouped cohort is not a flat listing")
	}
	if got := flatCohortEvidenceReserve(result, Budget{MaxCohortMembers: 25, MaxEvidenceRefs: 25}); len(got) != 0 {
		t.Fatalf("reserve for a grouped cohort = %d, want 0", len(got))
	}
	flat := ownedListing(4, 1)
	if got := flatCohortEvidenceReserve(flat, Budget{MaxCohortMembers: 25, MaxEvidenceRefs: 25}); len(got) != 4 {
		t.Fatalf("reserve for a flat listing = %d, want 4", len(got))
	}
}

func TestMatchedPairCohortKeepsTheWholeMemberRule(t *testing.T) {
	result := ownedListing(2, 3)
	result.AnswerPlan = &contractsv1.ContextFabricAnswerPlan{Family: contractsv1.ContextFabricQuestionFamilyExplicitComparison}
	if isFlatListing(result) {
		t.Fatal("a matched-pair cohort is not a flat listing")
	}
	if got := flatCohortEvidenceReserve(result, Budget{MaxCohortMembers: 25, MaxEvidenceRefs: 25}); len(got) != 0 {
		t.Fatalf("reserve for a matched pair = %d, want 0", len(got))
	}
}

func TestADriverCitingTheMemberReferencesIsNotOmittedForThem(t *testing.T) {
	result := ownedListing(19, 0)
	shared := make([]string, 0, 19)
	for _, member := range result.Cohort.Members {
		shared = append(shared, member.EvidenceRefIDs...)
	}
	for i := range result.Drivers {
		if result.Drivers[i].Standing == contractsv1.ContextFabricDriverPrincipal {
			result.Drivers[i].EvidenceRefIDs = shared
		}
	}
	projection := Project(result, Budget{MaxCohortMembers: 25, MaxEvidenceRefs: 25})
	if projection.ProjectionBudget.DriversOmitted != 0 {
		t.Fatalf("drivers_omitted = %d: a driver citing the 19 member references needs no room beyond them", projection.ProjectionBudget.DriversOmitted)
	}
	if got := len(projection.Cohort.Members); got != 19 {
		t.Fatalf("served %d members, want 19", got)
	}
}

func TestTheReserveIsOneReferencePerServedMember(t *testing.T) {
	result := ownedListing(5, 0)
	for i := range result.Cohort.Members {
		result.Cohort.Members[i].EvidenceRefIDs = []string{fmt.Sprintf("evidence_member_%02d", i), fmt.Sprintf("evidence_extra_%02d", i), fmt.Sprintf("evidence_more_%02d", i)}
	}
	if got := flatCohortEvidenceReserve(result, Budget{MaxCohortMembers: 25, MaxEvidenceRefs: 25}); len(got) != 5 {
		t.Fatalf("reserve = %d, want one per member (5)", len(got))
	}
}

func TestGroupedFamilyWithoutGroupsIsNotAFlatListing(t *testing.T) {
	result := ownedListing(4, 1)
	result.AnswerPlan = &contractsv1.ContextFabricAnswerPlan{Family: contractsv1.ContextFabricQuestionFamilyGroupedCohortStatus}
	if isFlatListing(result) {
		t.Fatal("a grouped-family cohort is not a flat listing")
	}
}

func TestTheReserveNeverExceedsTheReferenceBudgetNorTheServedMembers(t *testing.T) {
	result := ownedListing(19, 0)
	if got := flatCohortEvidenceReserve(result, Budget{MaxCohortMembers: 25, MaxEvidenceRefs: 5}); len(got) != 5 {
		t.Fatalf("reserve = %d, want the reference budget 5", len(got))
	}
	if got := flatCohortEvidenceReserve(result, Budget{MaxCohortMembers: 3, MaxEvidenceRefs: 25}); len(got) != 3 {
		t.Fatalf("reserve = %d, want the served members 3", len(got))
	}
}

func TestACitationRepeatingAReferenceCostsItOnce(t *testing.T) {
	index := newEvidenceIndex(3)
	if !index.admit([]string{"a", "a", "b", "b"}) {
		t.Fatal("two distinct references fit a budget of 3")
	}
	if got := len(index.ids()); got != 2 {
		t.Fatalf("indexed %d references, want 2", got)
	}
}
