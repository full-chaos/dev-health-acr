package falkorgraph

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

func TestCountKindTotalCountsEveryNodeOfTheKindWithoutTheOwnedFilter(t *testing.T) {
	var captured []string
	fake := &fakeConn{queryFunc: func(_ context.Context, _, q string, _ map[string]interface{}, _ bool) ([]row, error) {
		captured = append(captured, q)
		return []row{{"total": int64(7)}}, nil
	}}
	adapter := newFakeAdapter(t, fake)
	total, err := adapter.CountKindTotal(context.Background(), "org-1", contextfabric.SubjectPullRequest)
	if err != nil || total != 7 {
		t.Fatalf("CountKindTotal = %d, %v; want 7", total, err)
	}
	if len(captured) != 1 || strings.Contains(captured[0], propProjectionOwned) || !strings.Contains(captured[0], "count(n)") {
		t.Fatalf("CountKindTotal must be one aggregate with no owned filter: %v", captured)
	}
}

// The source counts ACTIVE teams only (devhealthsource teamsProjectsKindCountStatements,
// ActiveTeamPredicate), so the graph total holds the same population: the
// active-team predicate stays on the total. Teams the graph stores as inactive
// are not counted on either side.
func TestCountKindTotalForTeamsComparesLikeWithLike(t *testing.T) {
	type teamNode struct{ active bool }
	graph := func(nodes []teamNode) *fakeConn {
		return &fakeConn{queryFunc: func(_ context.Context, _, q string, _ map[string]interface{}, _ bool) ([]row, error) {
			filtered := strings.Contains(q, activeTeamCypher("n"))
			var total int64
			for _, n := range nodes {
				if n.active || !filtered {
					total++
				}
			}
			return []row{{"total": total}}, nil
		}}
	}
	// graphCountTolerance: max(3, source/50) once the graph holds any node.
	deficitBeyondTolerance := func(source, total int64) bool {
		tolerance := int64(3)
		if t := source / 50; t > tolerance {
			tolerance = t
		}
		return source-total > tolerance
	}
	build := func(active, inactive int) []teamNode {
		nodes := make([]teamNode, 0, active+inactive)
		for i := 0; i < active; i++ {
			nodes = append(nodes, teamNode{active: true})
		}
		for i := 0; i < inactive; i++ {
			nodes = append(nodes, teamNode{})
		}
		return nodes
	}
	const sourceActiveTeams = 5
	for name, tc := range map[string]struct {
		active, inactive int
		wantGap          bool
	}{
		"active plus inactive teams in the graph, no missing active team: no false gap": {5, 3, false},
		"a real missing active team still shows the gap":                                {1, 4, true},
	} {
		adapter := newFakeAdapter(t, graph(build(tc.active, tc.inactive)))
		total, err := adapter.CountKindTotal(context.Background(), "org-1", contextfabric.SubjectTeam)
		if err != nil {
			t.Fatal(err)
		}
		if got := deficitBeyondTolerance(sourceActiveTeams, total); got != tc.wantGap {
			t.Fatalf("%s: total %d against %d source teams: gap %v, want %v", name, total, sourceActiveTeams, got, tc.wantGap)
		}
	}
}
