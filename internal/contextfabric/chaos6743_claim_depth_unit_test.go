package contextfabric

import (
	"fmt"
	"testing"
)

func chaos6743UnitResult() InvestigationResult {
	cohort := chaos6743Cohort(3)
	claims := []ClaimedFact{}
	// member 0: four claims; member 1: one claim; member 2: two claims; plus
	// two claims about a subject outside the cohort.
	for member, count := range []int{4, 1, 2} {
		for index := 0; index < count; index++ {
			claims = append(claims, ClaimedFact{ClaimID: fmt.Sprintf("m%d_c%d", member, index), Subject: cohort.Members[member].Subject})
		}
	}
	outside := SubjectRef{Kind: SubjectRepository, CanonicalID: "repository:outside"}
	claims = append(claims, ClaimedFact{ClaimID: "out_0", Subject: outside}, ClaimedFact{ClaimID: "out_1", Subject: outside})
	return InvestigationResult{Cohort: cohort, ClaimedFacts: claims}
}

func chaos6743Kept(claims []ClaimedFact) map[string]bool {
	kept := map[string]bool{}
	for _, claim := range claims {
		kept[claim.ClaimID] = true
	}
	return kept
}

// Water-filling: K caps each member; a member at or under K loses nothing;
// claims about non-members are never cut; each member keeps its FIRST claims.
func TestCHAOS6743ClaimDepthCapIsWaterFilledPerMember(t *testing.T) {
	t.Parallel()
	result := chaos6743UnitResult()
	plan := newClaimDepthPlan(result)
	if plan.declared != 7 || plan.longest != 4 || plan.members != 3 {
		t.Fatalf("plan = declared %d longest %d members %d, want 7/4/3", plan.declared, plan.longest, plan.members)
	}
	cut, served := plan.cut(result.ClaimedFacts, 2)
	kept := chaos6743Kept(cut)
	want := []string{"m0_c0", "m0_c1", "m1_c0", "m2_c0", "m2_c1", "out_0", "out_1"}
	if served != 5 || len(cut) != len(want) {
		t.Fatalf("served %d member claims, kept %d claims; want 5 and %d", served, len(cut), len(want))
	}
	for _, id := range want {
		if !kept[id] {
			t.Fatalf("%s cut; kept = %v", id, kept)
		}
	}
}

// Every citation source pins its claim: a driver, each finding list, and a
// cohort member's driver. Each case cites ONE claim of member 0 past the cap.
func TestCHAOS6743EveryCitationSourcePinsItsClaim(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*InvestigationResult){
		"driver":         func(r *InvestigationResult) { r.Drivers = []DriverJudgment{{ClaimedFactIDs: []string{"m0_c3"}}} },
		"remaining work": func(r *InvestigationResult) { r.RemainingWork = []Finding{{ClaimedFactIDs: []string{"m0_c3"}}} },
		"readiness gap":  func(r *InvestigationResult) { r.ReadinessGaps = []Finding{{ClaimedFactIDs: []string{"m0_c3"}}} },
		"conflict":       func(r *InvestigationResult) { r.Conflicts = []Finding{{ClaimedFactIDs: []string{"m0_c3"}}} },
		"member driver": func(r *InvestigationResult) {
			r.Cohort.Members[0].Drivers = []CohortMemberDriver{{SourceClaimedFactIDs: []string{"m0_c3"}}}
		},
	}
	for name, cite := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			result := chaos6743UnitResult()
			cite(&result)
			plan := newClaimDepthPlan(result)
			cut, _ := plan.cut(result.ClaimedFacts, 1)
			kept := chaos6743Kept(cut)
			// K=1 with one cited claim: member 0 keeps ONLY the cited one.
			if !kept["m0_c3"] || kept["m0_c0"] {
				t.Fatalf("kept = %v, want the cited m0_c3 and not m0_c0 (the cited claim fills the member's cap)", kept)
			}
		})
	}
}

// A member is identified by kind AND canonical id (the contract's item
// attribution key). A claim about a subject of another kind that shares a
// member's id is not a claim about that member and is never cut (codex r1 P1).
func TestCHAOS6743ClaimDepthKeysMembersByKindAndID(t *testing.T) {
	t.Parallel()
	result := chaos6743UnitResult()
	for index := 0; index < 3; index++ {
		member := result.Cohort.Members[index].Subject
		for claim := 0; claim < 3; claim++ {
			result.ClaimedFacts = append(result.ClaimedFacts, ClaimedFact{
				ClaimID: fmt.Sprintf("project_%d_%d", index, claim),
				Subject: SubjectRef{Kind: SubjectProject, CanonicalID: member.CanonicalID},
			})
		}
	}
	plan := newClaimDepthPlan(result)
	if plan.declared != 7 || plan.longest != 4 {
		t.Fatalf("plan = declared %d longest %d, want 7/4: same-id claims of another kind are not member claims", plan.declared, plan.longest)
	}
	cut, _ := plan.cut(result.ClaimedFacts, 1)
	kept := chaos6743Kept(cut)
	for index := 0; index < 3; index++ {
		for claim := 0; claim < 3; claim++ {
			if id := fmt.Sprintf("project_%d_%d", index, claim); !kept[id] {
				t.Fatalf("%s (a project claim sharing a repository member's id) was cut", id)
			}
		}
	}
}
