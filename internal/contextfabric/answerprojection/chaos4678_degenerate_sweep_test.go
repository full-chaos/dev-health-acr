package answerprojection

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-4678 degenerate-input sweep, chris's order (08:10, after the
// round-4 empty-group finding made the floor invariant a CLASS with two
// prior violations in this PR): the same rows
// TestSelectGroupCoverMembersDegenerateInputSweep exercises against the
// shared core, run here through groupAwareMemberAllowance -- the
// PROJECTION call site -- to prove its own wrapping (the ungrouped-member
// derivation from cohort.Members minus claimed ids) does not reintroduce a
// defect the core itself does not have.

type degenerateSweepGroupAP struct {
	id      string
	members []string
}

type degenerateSweepRowAP struct {
	name       string
	groups     []degenerateSweepGroupAP
	ungrouped  []string
	maxMembers int
	checkFloor bool
}

func disjointSingletonGroupsAP(n int) []degenerateSweepGroupAP {
	groups := make([]degenerateSweepGroupAP, n)
	for i := 0; i < n; i++ {
		id := string(rune('A' + i))
		groups[i] = degenerateSweepGroupAP{id: id, members: []string{id + "_m"}}
	}
	return groups
}

func degenerateSweepRowsAP() []degenerateSweepRowAP {
	return []degenerateSweepRowAP{
		{
			name:       "single empty group, nothing else",
			groups:     []degenerateSweepGroupAP{{id: "A", members: nil}},
			ungrouped:  []string{"orphan"},
			maxMembers: 1, checkFloor: true,
		},
		{
			name:       "mixed empty and non-empty groups",
			groups:     []degenerateSweepGroupAP{{id: "A", members: nil}, {id: "B", members: []string{"b1"}}, {id: "C", members: []string{"c1", "c2"}}},
			maxMembers: 1, checkFloor: true,
		},
		{
			name:       "ALL groups empty",
			groups:     []degenerateSweepGroupAP{{id: "A", members: nil}, {id: "B", members: nil}},
			ungrouped:  []string{"orphan"},
			maxMembers: 3, checkFloor: true,
		},
		{
			name:       "budget 1, single group with several members",
			groups:     []degenerateSweepGroupAP{{id: "A", members: []string{"a1", "a2", "a3"}}},
			maxMembers: 1, checkFloor: true,
		},
		{
			name:       "single group, budget larger than the group",
			groups:     []degenerateSweepGroupAP{{id: "A", members: []string{"a1"}}},
			maxMembers: 5, checkFloor: true,
		},
		{
			name:       "exactly at the guard (12 groups), tight budget",
			groups:     disjointSingletonGroupsAP(contractsv1.ContextFabricSetCoverGroupGuard),
			maxMembers: 6, checkFloor: true,
		},
		{
			name:       "one beyond the guard (13 groups), tight budget",
			groups:     disjointSingletonGroupsAP(contractsv1.ContextFabricSetCoverGroupGuard + 1),
			maxMembers: 6, checkFloor: true,
		},
		{
			name: "empty group beyond the guard alongside real ones",
			groups: append(
				[]degenerateSweepGroupAP{{id: "EMPTY", members: nil}},
				disjointSingletonGroupsAP(contractsv1.ContextFabricSetCoverGroupGuard+1)...,
			),
			maxMembers: 6, checkFloor: true,
		},
		{
			name: "duplicate member shared by every group",
			groups: []degenerateSweepGroupAP{
				{id: "A", members: []string{"shared"}}, {id: "B", members: []string{"shared"}}, {id: "C", members: []string{"shared"}},
			},
			maxMembers: 1, checkFloor: true,
		},
		{
			name: "a group whose every member is shared with another group",
			groups: []degenerateSweepGroupAP{
				{id: "A", members: []string{"x", "y"}}, {id: "B", members: []string{"x"}}, {id: "C", members: []string{"y"}},
			},
			maxMembers: 2, checkFloor: true,
		},
		{
			name:       "ungrouped members alongside a group, tight budget",
			groups:     []degenerateSweepGroupAP{{id: "A", members: []string{"a1"}}},
			ungrouped:  []string{"orphan1", "orphan2"},
			maxMembers: 1, checkFloor: true,
		},
		{
			name:       "budget 0",
			groups:     []degenerateSweepGroupAP{{id: "A", members: []string{"a1"}}, {id: "B", members: []string{"b1"}}},
			maxMembers: 0, checkFloor: false, // documented boundary: nil ("no restriction"), not "nothing admitted"
		},
	}
}

// TestGroupAwareMemberAllowanceDegenerateInputSweep runs the sweep through
// the projection's own groupAwareMemberAllowance.
func TestGroupAwareMemberAllowanceDegenerateInputSweep(t *testing.T) {
	t.Parallel()
	for _, row := range degenerateSweepRowsAP() {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			groups := make([]contractsv1.ContextFabricCohortGroup, len(row.groups))
			claimed := map[string]bool{}
			var members []contractsv1.ContextFabricCohortMember
			rank := 1
			for i, g := range row.groups {
				groups[i] = contractsv1.ContextFabricCohortGroup{
					Subject:            subject(contractsv1.ContextFabricSubjectTeam, g.id, g.id),
					MemberCanonicalIDs: g.members,
					Complete:           true,
					Total:              len(g.members),
				}
				for _, id := range g.members {
					if claimed[id] {
						continue
					}
					claimed[id] = true
					members = append(members, contractsv1.ContextFabricCohortMember{
						Subject: subject(contractsv1.ContextFabricSubjectProject, id, id), Rank: rank,
						InclusionReasons: []string{"Graph retrieval associated this subject with the requested condition."},
					})
					rank++
				}
			}
			for _, id := range row.ungrouped {
				members = append(members, contractsv1.ContextFabricCohortMember{
					Subject: subject(contractsv1.ContextFabricSubjectProject, id, id), Rank: rank,
					InclusionReasons: []string{"Graph retrieval associated this subject with the requested condition."},
				})
				rank++
			}
			cohort := contractsv1.ContextFabricCohort{
				Kind: contractsv1.ContextFabricSubjectProject, Rationale: "sweep fixture", Complete: true,
				Members: members, Groups: groups,
			}

			var first map[string]struct{}
			for i := 0; i < 5; i++ {
				got := groupAwareMemberAllowance(cohort, row.maxMembers)
				if i == 0 {
					first = got
					continue
				}
				if !sameStringSet(got, first) {
					t.Fatalf("run %d: got %v, want %v -- identical requests must select identical members", i, got, first)
				}
			}
			if !row.checkFloor {
				return
			}
			if first == nil {
				// nil means "no restriction, every member admitted" (its
				// own doc comment) -- the floor trivially holds whenever
				// the whole cohort already fit within budget.
				return
			}
			for _, g := range row.groups {
				if len(g.members) == 0 {
					continue
				}
				var covered bool
				for _, id := range g.members {
					if _, ok := first[id]; ok {
						covered = true
					}
				}
				if !covered {
					t.Fatalf("group %q lost every member; allowed = %v -- the floor must hold for every non-empty group", g.id, first)
				}
			}
		})
	}
}

func sameStringSet(a, b map[string]struct{}) bool {
	if len(a) != len(b) {
		return false
	}
	for id := range a {
		if _, ok := b[id]; !ok {
			return false
		}
	}
	return true
}
