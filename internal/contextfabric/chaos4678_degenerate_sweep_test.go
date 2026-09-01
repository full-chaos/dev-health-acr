package contextfabric

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-4678 degenerate-input sweep, chris's order (08:10, after the
// round-4 empty-group finding made the floor invariant a CLASS with two
// prior violations in this PR): the same rows
// TestSelectGroupCoverMembersDegenerateInputSweep exercises against the
// shared core, run here through NarrowGroupedCohort -- the ENGINE call
// site -- to prove the wrapping (group reconstruction, Rank renumbering,
// member-list filtering) does not reintroduce a defect the core itself does
// not have.

type degenerateSweepGroup struct {
	id      string
	members []string
}

type degenerateSweepRow struct {
	name       string
	groups     []degenerateSweepGroup
	ungrouped  []string
	maxMembers int
	checkFloor bool
}

func disjointSingletonGroupsCF(n int) []degenerateSweepGroup {
	groups := make([]degenerateSweepGroup, n)
	for i := 0; i < n; i++ {
		id := string(rune('A' + i))
		groups[i] = degenerateSweepGroup{id: id, members: []string{id + "_m"}}
	}
	return groups
}

func degenerateSweepRowsCF() []degenerateSweepRow {
	return []degenerateSweepRow{
		{
			name:       "single empty group, nothing else",
			groups:     []degenerateSweepGroup{{id: "A", members: nil}},
			ungrouped:  []string{"orphan"},
			maxMembers: 1, checkFloor: true, // vacuous for the group; orphan is the only real member
		},
		{
			name:       "mixed empty and non-empty groups",
			groups:     []degenerateSweepGroup{{id: "A", members: nil}, {id: "B", members: []string{"b1"}}, {id: "C", members: []string{"c1", "c2"}}},
			maxMembers: 1, checkFloor: true,
		},
		{
			name:       "ALL groups empty",
			groups:     []degenerateSweepGroup{{id: "A", members: nil}, {id: "B", members: nil}},
			ungrouped:  []string{"orphan"}, // keeps the cohort non-trivial; vacuous floor, real determinism check
			maxMembers: 3, checkFloor: true,
		},
		{
			name:       "budget 1, single group with several members",
			groups:     []degenerateSweepGroup{{id: "A", members: []string{"a1", "a2", "a3"}}},
			maxMembers: 1, checkFloor: true,
		},
		{
			name:       "single group, budget larger than the group",
			groups:     []degenerateSweepGroup{{id: "A", members: []string{"a1"}}},
			maxMembers: 5, checkFloor: true,
		},
		{
			name:       "exactly at the guard (12 groups), tight budget",
			groups:     disjointSingletonGroupsCF(contractsv1.ContextFabricSetCoverGroupGuard),
			maxMembers: 6, checkFloor: true,
		},
		{
			name:       "one beyond the guard (13 groups), tight budget",
			groups:     disjointSingletonGroupsCF(contractsv1.ContextFabricSetCoverGroupGuard + 1),
			maxMembers: 6, checkFloor: true,
		},
		{
			name: "empty group beyond the guard alongside real ones",
			groups: append(
				[]degenerateSweepGroup{{id: "EMPTY", members: nil}},
				disjointSingletonGroupsCF(contractsv1.ContextFabricSetCoverGroupGuard+1)...,
			),
			maxMembers: 6, checkFloor: true,
		},
		{
			name: "duplicate member shared by every group",
			groups: []degenerateSweepGroup{
				{id: "A", members: []string{"shared"}}, {id: "B", members: []string{"shared"}}, {id: "C", members: []string{"shared"}},
			},
			maxMembers: 1, checkFloor: true,
		},
		{
			name: "a group whose every member is shared with another group",
			groups: []degenerateSweepGroup{
				{id: "A", members: []string{"x", "y"}}, {id: "B", members: []string{"x"}}, {id: "C", members: []string{"y"}},
			},
			maxMembers: 2, checkFloor: true,
		},
		{
			name:       "ungrouped members alongside a group, tight budget",
			groups:     []degenerateSweepGroup{{id: "A", members: []string{"a1"}}},
			ungrouped:  []string{"orphan1", "orphan2"},
			maxMembers: 1, checkFloor: true,
		},
		{
			name:       "budget 0",
			groups:     []degenerateSweepGroup{{id: "A", members: []string{"a1"}}},
			maxMembers: 0, checkFloor: false, // documented boundary: the cohort is returned unchanged (narrowed=false), not emptied
		},
	}
}

// TestNarrowGroupedCohortDegenerateInputSweep runs the sweep through the
// engine's own NarrowGroupedCohort, calling it with each row's OWN
// maxMembers unmodified -- including budget 0, whose floor semantics differ
// (see that row's own comment) and must not be silently coerced into a
// narrowing case.
func TestNarrowGroupedCohortDegenerateInputSweep(t *testing.T) {
	t.Parallel()
	for _, row := range degenerateSweepRowsCF() {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			var allMembers []string
			claimed := map[string]bool{}
			for _, g := range row.groups {
				for _, id := range g.members {
					if !claimed[id] {
						claimed[id] = true
						allMembers = append(allMembers, id)
					}
				}
			}
			for _, id := range row.ungrouped {
				allMembers = append(allMembers, id)
			}
			if len(allMembers) == 0 {
				t.Fatal("row has no members at all -- every row must carry at least one real member (e.g. via ungrouped) so this surface is actually exercised")
			}
			cohort := planFixtureCohort(allMembers...)
			groups := make([]contractsv1.ContextFabricCohortGroup, len(row.groups))
			for i, g := range row.groups {
				groups[i] = contractsv1.ContextFabricCohortGroup{
					Subject:            SubjectRef{Kind: SubjectTeam, CanonicalID: g.id, Label: g.id},
					MemberCanonicalIDs: g.members,
					Complete:           true,
					Total:              len(g.members),
				}
			}
			cohort.Groups = groups
			target := row.maxMembers
			var firstKept []CohortMember
			var firstBasis contractsv1.ContextFabricNarrowingBasis
			for i := 0; i < 5; i++ {
				kept, _, narrowed, basis := NarrowGroupedCohort(cohort, target)
				if !narrowed {
					// Already fits (or nothing could be narrowed): the
					// ORIGINAL member set survives unchanged, exactly as
					// engine.go treats narrowed=false -- kept=nil here does
					// NOT mean "nothing survived".
					kept = cohort.Members
				}
				if i == 0 {
					firstKept, firstBasis = kept, basis
					continue
				}
				if basis != firstBasis || !sameCohortMemberIDs(kept, firstKept) {
					t.Fatalf("run %d: got (%v, %q), want (%v, %q) -- identical requests must select identical members", i, canonicalIDsOf(kept), basis, canonicalIDsOf(firstKept), firstBasis)
				}
			}
			if !row.checkFloor {
				return
			}
			keptIDs := map[string]bool{}
			for _, m := range firstKept {
				keptIDs[m.Subject.CanonicalID] = true
			}
			for _, g := range row.groups {
				if len(g.members) == 0 {
					continue
				}
				var covered bool
				for _, id := range g.members {
					if keptIDs[id] {
						covered = true
					}
				}
				if !covered {
					t.Fatalf("group %q lost every member; kept = %v -- the floor must hold for every non-empty group", g.id, canonicalIDsOf(firstKept))
				}
			}
		})
	}
}

func sameCohortMemberIDs(a, b []CohortMember) bool {
	if len(a) != len(b) {
		return false
	}
	setA := map[string]bool{}
	for _, m := range a {
		setA[m.Subject.CanonicalID] = true
	}
	for _, m := range b {
		if !setA[m.Subject.CanonicalID] {
			return false
		}
	}
	return true
}
